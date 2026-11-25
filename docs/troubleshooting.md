# Troubleshooting

## TLS errors

Symptom: a client fails with a certificate error (`x509: certificate is valid for …, not …`,
`unknown authority`).

- The host in `--broker` must be in the broker certificate (`cannot validate certificate for
  10.1.1.107 because it doesn't contain any IP SANs` means that IP is missing). See what it holds
  with `openssl x509 -in broker.pem -noout -ext subjectAltName`, then reissue it with every name
  or IP clients use, keeping the CA, and restart the broker. Clients need nothing new:

  ```sh
  podman exec agent-bus agent-busctl tls broker --san localhost --san 127.0.0.1 --san bus.example.org --san 192.168.1.10
  podman restart agent-bus     # in the bind-mount setup instead: start.sh, then monitor.sh
  ```

  With compose: `docker compose -f deploy/docker-compose.yaml exec agent-bus agent-busctl tls broker
  --san ...`, then `docker compose -f deploy/docker-compose.yaml restart agent-bus`.

- A client trusts only `ca.pem` (`--ca-cert`). Copy the current one from the broker's config
  directory.

## When the bus is down

Symptom: tools fail with "bus unavailable, reconnecting (next attempt in 4s)", or the monitor
shows "bus disconnected, reconnecting...". The broker restarts on every release of a deployment
that updates itself, so this is normal for a moment.

`agent-bus mcp` and `bus-monitor` keep running through an outage, also when they start while the
broker is down. They retry forever: the wait starts at 1 s, doubles after each failed attempt up to
30 s, with some randomness, and starts over after a successful connect
(`--reconnect-initial` and `--reconnect-max` change it). Each outage and its end are
logged once on stderr.

- Keeps working: `inbox`, `wait_for_messages`, `whoami` and `list_channels`, from the messages
  already buffered; the hooks serve that buffer too.
- Fails until the bus is back: `send`, `publish` and `mark_read`. Nothing is lost or half done: a failed
  `mark_read` leaves the message unread, so repeat it later.
- The hooks say so once when an outage lasts a minute ([claude-code.md](claude-code.md#a-broker-that-stays-out-of-reach)),
  instead of staying silent as for an empty inbox. Nothing is needed from you: the adapter reads on
  by itself when the broker is back, with no `/mcp`.
- Messages are durable. The broker keeps them on disk, a message that was delivered and not acked
  is delivered again, and an agent skips what it has handled, so after the reconnect you get each
  message once, including those sent during the outage.

The commands that run once (`agent-busctl`, `agent-bus send`, `publish`, `inbox`, `mark-read`) do not retry:
they fail at once with the connection error. If the bus stays down, see whether the broker runs
(`docker compose -f deploy/docker-compose.yaml up -d`).

## The MCP server is gone

Symptom: the Stop hook or the waiter says "this session's MCP server (agent-bus mcp) is not
running", or the `agent-bus` tools have disappeared from the session without an error.

`agent-bus mcp` is a child of Claude Code, and Claude Code does not restart a stdio server that
exited; nothing can restart it from inside the adapter. Run `/mcp` in the session to reconnect it.
Mail sent meanwhile is kept by the broker and arrives after that. The server ends when Claude Code
closes its stdin or sends SIGTERM (both release the buffered mail at once), when it is killed, when it
crashes (a Go panic, which only reaches stderr), or when its login or certificate fails at start
(`/mcp` then shows it as failed). A lost broker is none of these: the server stays and reconnects
([When the bus is down](#when-the-bus-is-down)). To find out why it ended, look at its stderr:
`claude --debug` shows the logs of MCP servers.

## The inbox is empty after a restart

Symptom: after a session restart `inbox` is empty, although a message that was sent before is on the
monitor. It shows up after about a minute.

The old `agent-bus mcp` had fetched the message and not acked it, and a killed process cannot give
it back. A process that ends cleanly (Claude Code closing its stdin or sending SIGTERM) hands its
buffered mail back to the broker at once; one that was killed leaves it with the broker as
delivered, and the broker offers it again only when the consumers' `AckWait` of one minute has run
out. Nothing is lost and nothing needs doing; the new session's reader picks it up then.
## Identity in use

Symptom: `agent-bus mcp` (or any `agent-bus` command) fails to start with "the identity is in use by another
session (adapter process `<pid>`): stop it ..."; in Claude Code the server shows as failed in `/mcp`, and the
line is in its MCP log.

Another process holds the identity: a second Claude Code session, a leftover `agent-bus mcp` of an earlier
`/mcp` reconnect or an old `--broker` (`ps -p <pid> -o args=` shows which), or an `agent-bus` command that is
still running. Stop it (`kill <pid>`), then reconnect with `/mcp`; or give this session its own identity
([claude-code.md](claude-code.md#one-session-per-identity)). The lock is released when the process
ends, so there is nothing stale to remove, and a PID in the lock file of a dead process blocks nobody.

## The session is connected to the wrong broker

Symptom: tools work, but nothing arrives and nobody sees this agent online. Call `whoami`: `broker` is the
address the session dialed. Mail goes where that broker is, so a session on a retired test broker (for
example `localhost:4222`) works perfectly and talks to nobody. The MCP log has the same address on its
`connecting to ... (broker from ...)` line, with where it came from: `--broker or BUS_BROKER`, the broker this
identity enrolled at (`<config-dir>/<agent-id>/broker`), or the default `localhost:4222`. When the identity has
a client certificate but no recorded broker, the log has a `WARNING` and `whoami` a `broker_note` saying so. Fix the `--broker` of the MCP entry (or `BUS_BROKER` in the environment Claude Code
starts it in), or drop both to use the enrolled broker, then reconnect with `/mcp`. An identity enrolled before
this file existed, or copied from another machine without it, has none: pass `--broker` once, or run `agent-bus enroll` again.
A `broker` file that is empty or not `host:port` is an error naming the file.

## Unknown agent

Symptom: `inbox` or `whoami` shows `unknown_senders` ("N messages from unknown senders are waiting")
or `send` fails with "unknown agent `<id>`: it has no entry in the directory".

The agent (the recipient, or the sender of the waiting mail) has no file
`<config-dir>/directory/<token>.json` on this machine. A running agent asks busd for the directory
every minute and, at most every five seconds, when it meets an unknown agent, so a newly enrolled
agent is known at once; the message then says if the fetch failed ("fetching the directory failed:
..."). If it keeps failing, the machine has no enrollment password in `<config-dir>/enroll/password`
(`agent-bus mcp` logs it once; `enroll` writes the file, an agent enrolled before has to get it by
hand), busd or the agent is of an older version, busd is down, or the log says "not signed by the
CA" (this machine's `ca.pem` is not busd's CA) or "before the one taken in" (busd's clock went
back; the directory is signed and dated, so an old copy is refused). Then `register` writes the file on
the machine it runs on; copy that `directory/` to this one, for example `rsync -a
other:~/.config/agent-bus/directory/ ~/.config/agent-bus/directory/`
([claude-code.md](claude-code.md#other-machines)). The waiting mail is read within a minute of the
copy.

## Messages from an agent never arrive (changed key)

Symptom: an agent sends, the monitor shows the message as verified, but the recipient's `inbox`
never lists it. `alerts.log` in the recipient's identity folder has `rejected ...: key of <id>
changed: pinned ..., message signed with ...`.

The recipient pinned the sender's old key, and the sender has a new one (enrolled again with
`--replace`, or set up anew). A new key is never accepted silently: check it and drop the old pin,
as in [known-agents.md](known-agents.md#accept-a-new-key).

## Unknown channel

Symptom: `publish` fails with "unknown channel `<name>`", or its `warning` says a target does not
read the channel.

The directory's catalogue snapshot `<config-dir>/directory/channels` does not list the group, or
does not list the target as a reader. It is written by `agent-busctl channel create` and `channel
join` on the machine they run on, and a running agent fetches it from busd every minute (an agent
without the enrollment password in `<config-dir>/enroll/password`, or of an older version, needs
`directory/` copied over, or `agent-busctl directory export` and a copy of the result). Restart the
adapter to read a newly joined group. Meanwhile DM the agent: a DM always arrives.

## Removed agent

Symptom: `send` fails with "agent removed: `<id>`", or `alerts.log` shows "rejected ... revoked
key ...".

The agent was removed with `agent-busctl agent remove` and its key is revoked. Its ID is never
registered again; register a new one. If `agent remove` printed "NOT completely removed", it names
the steps that are left: run the same command again. A running agent learns of a removal from busd
within a minute, or at once with `kill -HUP <agent-bus pid>`. One without the enrollment password in
`<config-dir>/enroll/password`, or of an older version, needs `agent-busctl directory export` on the
broker's host and the result copied over `<config-dir>/directory/`
([agent-busctl.md](agent-busctl.md#directory-export)).

## Hook is silent

The hooks print nothing on any failure. Check, in turn:

1. `agent-bus` is on the hook's path (or the command uses an absolute path).
2. The session's MCP server holds the identity (call `whoami`); a session without the lock gets
   silence.
3. The socket exists: `ls $XDG_RUNTIME_DIR/agent-bus/` (else `/tmp/agent-bus-<uid>/`).
4. You are on Linux; the hook finds its session through `/proc`.
5. There is unreported mail: each message is reported once, at least 10 seconds apart. `inbox`
   lists it all along.
6. `SessionStart` races the MCP server's start and may find an empty inbox in the first moments.

## Every agent shows offline, or sends warn "has not been online"

Presence needs the stream `PRESENCE` and the per-agent grant to publish `presence.<token>`. After
upgrading from a release without them, run `agent-busctl --direct init` once: it creates the stream and
regenerates every agent's grants, which take effect on live connections; until then the publish
is refused and the server logs a permissions violation. An agent without hooks is `idle` while its process runs; `unresponsive` needs targeted mail
unlisted for 10 minutes.

## One agent goes offline while its session is still open

The heartbeat goes out every 30 seconds while the session's `agent-bus` runs, busy or idle; the
monitor calls the agent offline after 90 seconds without one. So the process stopped, its machine
slept, or it cannot reach the broker. Look at the session's MCP log (Claude Code keeps it in
`~/.cache/claude-cli-nodejs/<project>/mcp-logs-agent-bus/`):

- `agent-bus: presence: publishing failed ... : <error>`, later `publishing works again after <time>`:
  the process ran but could not reach the broker. The connection lines next to it say why
  (`lost the connection to the bus`, `reconnected`, `closed for good: restart this process`).
- No such line around that time: the process stopped (look for it with `pgrep -af 'agent-bus mcp'`;
  a leftover one with an old `--broker` holds the identity) or the machine slept.

`agent-bus mcp` ends within about a second of SIGTERM or SIGINT, even in the middle of
`wait_for_messages`, and gives the identity back. Older builds ignored the signal until the call returned
(up to 5 minutes) and had to be killed with SIGKILL.

## An agent shows idle while it is working, or working while it is idle

Claude Code runs no `Stop` hook when you interrupt a turn with Esc, and the async tool hooks can
land after `Stop`. A session that shows `working` but sent no event for 10 minutes is shown `idle`
(its last activity stays visible); the next event shows `working` again. A subagent whose
`SubagentStop` never arrived stays in the worker list for 60 minutes.

## Consumer missing

Symptom: an agent never sees posts of a group channel.

The agent has no read cursor for it. Create the channel and join the agent, then start the adapter
with `--groups <group>`:

```sh
agent-busctl --direct channel create lib-x --description "..."
agent-busctl --direct channel join api-client@alice lib-x
```

Also, a post to a group nobody created is stored and never read.

## Certificate login fails

Symptom: `agent-bus` or `agent-busctl` reports `nats: Authorization Violation`, or a TLS error such
as `bad certificate`, on a machine whose agent folder has `client.pem` and `client.key`.

- Those files make the client use the certificate listener (`wss://<broker-host>:4223`, or
  `BUS_MAPPED_PORT`). The port must be published and reachable, and the broker must know the
  certificate: `agent-busctl --direct cert switch <agent-id>` ran (or `cert issue --switch`).
  Until then delete the two files to use the password again, or switch the agent.
- The certificate is for one agent ID and one CA. Files copied from another agent, or from before a
  `tls init --force`, are refused; issue again. An expired certificate (one year by default) is
  refused: `cert issue` again, copy the files.
- A removed agent's certificate never works again, and neither does one that was replaced: `cert
  issue` again, `cert sign`, `ca sign --replace` or a switch back to the password leave only the
  newest certificate valid, so a machine with an older `client.pem` is refused until it gets the
  current one.
- After `cert switch <agent-id> --password` (rollback) the password is back, but a machine that
  still has `client.pem` and `client.key` keeps trying the certificate: delete both files.
- A client refuses to start with an error naming `client.pem` and `client.key` when they are not a
  pair, are for another identity or have expired. It never falls back to the password; issue again
  or delete both files.
- If an admin migration (`cert issue --admin`) or rollback fails midway and a retry does not help
  (for example the reload is not confirmed), the escape hatch is offline: `agent-busctl admin init
  --force`, then restart the broker. It resets the admin users to passwords, retires the admin
  certificates (`*.retired`) and keeps the agents' grants. Automatic recovery of interrupted
  migrations is later hardening.
- The password port (`4222`) refuses a certificate agent, and the certificate port refuses
  everything without a client certificate; that is intended.

## Enrollment fails

Symptoms of `agent-bus enroll` and `agent-busctl ca`, and what they mean
([podman.md](podman.md#9-enrolling-a-new-machine)):

- `no CA certificate at ...`: copy `ca.pem` from the central machine, or pass `--ca-fingerprint`
  (`agent-busctl ca fingerprint` there).
- `sent no CA certificate with the fingerprint ...`: the fingerprint is wrong, you are talking to
  another broker, or the broker's `broker.pem` was made by an older `tls init` and lacks the CA
  after the broker certificate. Copy `ca.pem` instead, or append it once (podman.md, section 9).
- `has the fingerprint ..., not ...: it is not the owner's CA`: the `ca.pem` on this machine is not
  the one you meant; do not go on until the fingerprints agree.
- `Authorization Violation` when asking: the enrollment password is wrong, or `agent-busctl enroll
  init` never ran (or ran with `--force` since).
- `busd is not running on the broker`: busd did not start (it needs `ca.key` in the config folder,
  `BUS_BUSD` not `off`) or has stopped. `podman logs agent-bus` shows `busd:` lines; the entrypoint
  starts it again within seconds. After `cert issue --admin` run `podman exec agent-bus pkill busd`
  once so it reads the admin certificates.
- `too many ...` or `busy`: a limit was hit (10 requests a minute, 100 pending, 3 per ID); the
  machine tries again with `--waitforcert`.
- `the request ... was rejected: ...`: the owner rejected it, or it expired after 24 hours; the
  reason is printed. Run the command again for a new request.
- `no pending request`: `ca sign` and `ca reject` name a pending request by code, request ID or
  agent ID; with two requests for one ID, use the code. busd keeps them across a restart.
- `is registered with another key ... --replace`: an ID of an existing agent with a different
  signing key: only approve this if the machine really replaces that agent.
- `was removed and its ID is never used again`: pick another ID.
- A machine that was approved but cannot log in: publish port `4223`, and check
  [Certificate login fails](#certificate-login-fails).

## Monitor

- `monitor init` stops with "the monitor has no login monitor-bus here: run `agent-busctl service
  init`": the broker's config folder is from before busd. Run `agent-busctl --direct service init`,
  restart the broker, then `monitor init` again.
- `podman rm -f agent-bus` says "has dependent containers which must be removed before it": the
  monitor shares the broker's network. Use `podman rm -f --depend agent-bus`; it also removes the
  monitor, which you then start again ([podman.md](podman.md#6-the-monitor)).
- The monitor does not start and the log says it needs the web login password: set it with
  `agent-busctl monitor password` on the broker, run `monitor init` again and restart the monitor
  ([monitor.md](monitor.md#login)).
- The monitor does not trust the broker after `tls init --force`, or does not know a new agent:
  its config folder is a copy. Run `monitor init` again and restart `bus-monitor`.

## Reload failed

Symptom: `register` or `channel join` fails while testing or reloading the server.

- The test uses `nats-server -t`: `--nats-server` (or `BUS_NATS_SERVER`) must name an existing
  binary: in the container it is set by the image; outside Docker, see [development.md](development.md#running-the-broker-without-docker).
- The server must be running, with its pid file in `nats/`, and its monitoring endpoint must
  answer at `--monitor-url` (default `http://127.0.0.1:8222`). Check `nats/nats-server.log`.
- An invalid candidate is refused before it replaces `auth.conf`; the old file stays in force.
- After `admin init --force`, restart the server.

## A registration did not complete

If the directory `<config-dir>/<agent-id>/` exists but the bus has no such account, the earlier
`register` did not complete: the error names the directory to remove before you rerun it. If the
error says the server may have applied the account, the directory is kept on purpose.

## Authorization Violation after register

`register` timed out on the monitoring endpoint (`.../varz`) with "the broker may or may not have
created the account", and `agent-bus` then fails with `nats: Authorization Violation`. The broker
runs on non-default ports, but `agent-busctl` still uses the defaults. Point both tools at the real
ports and rerun `register`:

```sh
export BUS_BROKER=localhost:<nats-port>
export BUS_MONITOR_URL=http://127.0.0.1:<monitor-port>
```

See [Settings and ports](broker.md#settings-and-ports).
