# Troubleshooting

Find your symptom in the index, then follow the section: what it means, what to check, how to fix it. Commands
are given for Podman. Docker has its own form where the two differ (`docker`, or
`docker compose -f deploy/docker-compose.yaml`); the cases below say where, and the container removal in
[Monitor](#monitor) is different.

## Symptom index

| Area | Symptom | Section |
|---|---|---|
| Connection | a client fails with `x509: certificate is valid for …` or `unknown authority` | [TLS errors](#tls-errors) |
| Connection | `bus unavailable, reconnecting`, or the monitor shows "bus disconnected" | [When the bus is down](#when-the-bus-is-down) |
| Connection | tools work, nothing arrives, nobody sees the agent online | [Connected to the wrong broker](#the-session-is-connected-to-the-wrong-broker) |
| Connection | an enrolled identity logs `WARNING` and dials `localhost:4222` | [Falls back to localhost](#an-enrolled-identity-falls-back-to-localhost) |
| Connection | `nats: Authorization Violation` or `bad certificate` | [Certificate login fails](#certificate-login-fails) |
| Connection | `Authorization Violation` right after `register` | [After register](#authorization-violation-after-register) |
| Identity | "the identity is in use by another session" | [Identity in use](#identity-in-use) |
| Identity | "no identity ... in the config directory" | [No identity](#no-identity--in-the-config-directory) |
| Identity | the `agent-bus` tools disappeared from the session | [The MCP server is gone](#the-mcp-server-is-gone) |
| Identity | `register` left a directory behind | [A registration did not complete](#a-registration-did-not-complete) |
| Trust | `unknown_senders`, or "unknown agent" | [Unknown agent](#unknown-agent) |
| Trust | a sender's mail never arrives, `alerts.log` says "key changed" | [Changed key](#messages-from-an-agent-never-arrive-changed-key) |
| Trust | "agent removed" or "revoked key" | [Removed agent](#removed-agent) |
| Delivery | hooks never run | [Hooks never fire](#hooks-never-fire) |
| Delivery | hooks run and print nothing | [Hook is silent](#hook-is-silent) |
| Delivery | `inbox` is empty after a restart | [Empty after a restart](#the-inbox-is-empty-after-a-restart) |
| Delivery | "unknown channel", or posts of a group never arrive | [Unknown channel](#unknown-channel), [Consumer missing](#consumer-missing) |
| Delivery | a channel name is taken, but the channel does not exist | [A channel name stays taken](#a-channel-name-stays-taken) |
| Presence | an agent shows `unresponsive` | [Unresponsive](#an-agent-shows-unresponsive) |
| Presence | every agent shows `offline` | [Every agent shows offline](#every-agent-shows-offline-or-sends-warn-has-not-been-online) |
| Presence | one agent goes `offline` while its session is open | [One agent goes offline](#one-agent-goes-offline-while-its-session-is-still-open) |
| Presence | `idle` while working, or `working` while idle | [Wrong state](#an-agent-shows-idle-while-it-is-working-or-working-while-it-is-idle) |
| Enrollment | `agent-bus enroll` or `agent-busctl ca` fails | [Enrollment fails](#enrollment-fails) |
| Monitor | the monitor does not start, or does not trust the broker | [Monitor](#monitor) |
| Admin | `register` or `channel join` fails on the reload | [Reload failed](#reload-failed) |

## Connection

### TLS errors

**Symptom.** A client fails with a certificate error: `x509: certificate is valid for …, not …`, or
`unknown authority`.

| Cause | Fix |
|---|---|
| the host in `--broker` is not in the broker certificate (`cannot validate certificate for 192.0.2.10 because it doesn't contain any IP SANs` means that IP is missing) | reissue the broker certificate with every name or IP clients use, keeping the CA, and restart the broker. Clients need nothing new |
| the client does not trust the broker's CA | a client trusts only `ca.pem` (`--ca-cert`): copy the current one from the broker's config directory |

See what the certificate holds, then reissue it:

```sh
openssl x509 -in broker.pem -noout -ext subjectAltName
podman exec agent-bus agent-busctl tls broker --san localhost --san 127.0.0.1 --san bus.example.com --san 192.0.2.10
podman restart agent-bus     # in the bind-mount setup instead: start.sh, then monitor.sh
```

With compose: `docker compose -f deploy/docker-compose.yaml exec agent-bus agent-busctl tls broker --san ...`,
then `docker compose -f deploy/docker-compose.yaml restart agent-bus`.

### When the bus is down

**Symptom.** Tools fail with "bus unavailable, reconnecting (next attempt in 4s)", or the monitor shows "bus
disconnected, reconnecting...". The broker restarts on every release of a deployment that updates itself, so
this is normal for a moment.

`agent-bus mcp` and `bus-monitor` keep running through an outage, also when they start while the broker is
down. They retry forever: the wait starts at 1 s, doubles after each failed attempt up to 30 s, with some
randomness, and starts over after a successful connect (`--reconnect-initial` and `--reconnect-max` change
it). Each outage and its end are logged once on stderr.

| During the outage | |
|---|---|
| keeps working | `inbox`, `wait_for_messages`, `whoami` and `channels` (its list, from the local directory), from the messages already buffered; the hooks serve that buffer too |
| fails until the bus is back | `send`, `publish` and `mark_read`. Nothing is lost or half done: a failed `mark_read` leaves the message unread, so repeat it later |

- The hooks say so once when an outage lasts a minute
  ([claude-code.md](claude-code.md#a-broker-that-stays-out-of-reach)), instead of staying silent as for an
  empty inbox. Nothing is needed from you: the adapter reads on by itself when the broker is back, with no
  `/mcp`.
- Messages are durable. The broker keeps them on disk, a message that was delivered and not acked is delivered
  again, and an agent skips what it has handled, so after the reconnect you get each message once, including
  those sent during the outage.
- The commands that run once (`agent-busctl`, `agent-bus send`, `publish`, `inbox`, `mark-read`) do not retry:
  they fail at once with the connection error.

If the bus stays down, see whether the broker runs: `podman ps`, `podman logs agent-bus`, or
`docker compose -f deploy/docker-compose.yaml up -d`.

### The session is connected to the wrong broker

**Symptom.** Tools work, but nothing arrives and nobody sees this agent online.

**Cause.** Mail goes where the session's broker is, so a session on a retired test broker (for example
`localhost:4222`) works perfectly and talks to nobody. Call `whoami`: `broker` is the address the session
dialed. The MCP log has the same address on its `connecting to ... (broker from ...)` line, with where it came
from:

| `broker from ...` | Meaning |
|---|---|
| `--broker or BUS_BROKER` | the MCP entry or the environment Claude Code starts it in |
| `enrolled, from <config-dir>/<agent-id>/broker` | the broker this identity enrolled at |
| `the default: no --broker and no recorded broker` | `localhost:4222` |

**Fix.** Correct the `--broker` of the MCP entry (or `BUS_BROKER`), or drop both to use the enrolled broker,
then reconnect with `/mcp`. An identity enrolled before the `broker` file existed, or copied from another
machine without it, has none: see the next case. A `broker` file that is empty or not `host:port` is an error
naming the file.

### An enrolled identity falls back to localhost

**Symptom.** The MCP log of a session has a line like this, and `whoami` carries the same text as
`broker_note`:

```text
agent-bus: WARNING: lib-x@bob has a client certificate but no broker is recorded (/home/bob/.config/agent-bus/lib-x@bob/broker), so this session dials localhost:4222. If that is not your broker, pass --broker or set BUS_BROKER, or run agent-bus enroll again.
```

```json
{"agent_id": "lib-x@bob", "broker": "wss://localhost:4223", "connected": false, "connection_problem": "…", "broker_note": "lib-x@bob has a client certificate but no broker is recorded (…/lib-x@bob/broker), so this session dials localhost:4222. …"}
```

**Cause.** The identity has `client.pem` and `client.key` but no `broker` file: it was enrolled by an older
version, or its files were copied from another machine. With no `--broker` and no recorded broker the session
guesses `localhost:4222`.

**Fix.** Pass the broker once, in the MCP entry (`--broker bus.example.com:4222`) or as `BUS_BROKER`, and
reconnect with `/mcp`. Or run `agent-bus enroll` again for the identity, which records the broker.

### Certificate login fails

**Symptom.** `agent-bus` or `agent-busctl` reports `nats: Authorization Violation`, or a TLS error such as
`bad certificate`, on a machine whose agent folder has `client.pem` and `client.key`.

Those files make the client use the certificate listener (`wss://<broker-host>:4223`, or `BUS_MAPPED_PORT`).
The port must be published and reachable, and the broker must know the certificate.

| Cause | Fix |
|---|---|
| the broker does not know the certificate yet | run `agent-busctl --direct cert switch <agent-id>` (or `cert issue --switch`, [certificates](agent-busctl.md)). Until then delete the two files to use the password again |
| the certificate is for another agent or CA: files copied from another agent, or from before a `tls init --force` | issue again |
| the certificate expired (one year by default) | `cert issue` again, copy the files |
| a removed agent's certificate, or a replaced one | never works again. `cert issue` again, `cert sign`, `ca sign --replace` or a switch back to the password leave only the newest certificate valid, so a machine with an older `client.pem` is refused until it gets the current one |
| after `cert switch <agent-id> --password` (rollback) a machine still has `client.pem` and `client.key` and keeps trying the certificate | delete both files |
| the client refuses to start with an error naming `client.pem` and `client.key`: they are not a pair, are for another identity or have expired | issue again or delete both files. It never falls back to the password |
| the password port (`4222`) and the certificate port (`4223`) | the password port refuses a certificate agent, and the certificate port refuses everything without a client certificate; that is intended |

**An admin migration that failed.** If an admin migration (`cert issue --admin`) or rollback fails midway and a
retry does not help (for example the reload is not confirmed), the escape hatch is offline:
`agent-busctl admin init --force`, then restart the broker. It resets the admin users to passwords, retires
the admin certificates (`*.retired`) and keeps the agents' grants. Automatic recovery of interrupted
migrations is later hardening.

### Authorization Violation after register

**Symptom.** `register` timed out on the monitoring endpoint (`.../varz`) with "the broker may or may not have
created the account", and `agent-bus` then fails with `nats: Authorization Violation`.

**Cause.** The broker runs on non-default ports, but `agent-busctl` still uses the defaults.

**Fix.** Point both tools at the real ports and rerun `register`
(see [Settings and ports](broker.md#settings-and-ports)):

```sh
NATS_PORT=14222        # the port the broker really listens on
MONITOR_PORT=18222     # its monitoring port
export BUS_BROKER="localhost:$NATS_PORT"
export BUS_MONITOR_URL="http://127.0.0.1:$MONITOR_PORT"
```

## Identity

### Identity in use

**Symptom.** `agent-bus mcp` (or any `agent-bus` command) fails to start with "the identity is in use by
another session (adapter process `<pid>`): stop it ...". In Claude Code the server shows as failed in `/mcp`,
and the line is in its MCP log.

**Cause.** Another process holds the identity: a second Claude Code session, an `agent-bus` command that is
still running, or a stale adapter. A stale adapter is a leftover `agent-bus mcp` of an earlier session or
`/mcp` reconnect, or one with an old `--broker`. It works, but for a session nobody uses.

**Fix.**

1. Find the holder: the PID is in the message. `ps -o pid,ppid,etime,args -p <pid>` shows how it started and
   how long it has run; `pgrep -af 'agent-bus mcp'` lists every adapter on the machine.
2. If it is a session you use, give this session its own identity
   ([claude-code.md](claude-code.md#one-session-per-identity)).
3. If it is stale, stop it: `kill <pid>`.
4. Reconnect with `/mcp`.

The lock is released when the process ends, so there is nothing stale to remove, and a PID in the lock file of
a dead process blocks nobody.

### No identity ... in the config directory

**Symptom.** `agent-bus mcp` fails with "no identity `<id>` in `<config-dir>`: --agent (or BUS_AGENT) is not an
identity of this machine, which has `<ids>`" (older builds: "reading credentials: .../`<id>`/password: no such
file").

**Cause.** `--agent` names an identity that was never enrolled or registered on this machine. `agent-bus` has
no default identity, so the value came from the MCP entry or from `BUS_AGENT` in the environment Claude Code
runs in, often copied from an example.

**Fix.** Use one of the listed identities, or enroll the new one
([claude-code.md](claude-code.md#other-machines)).

### The MCP server is gone

**Symptom.** The Stop hook or the waiter says "this session's MCP server (agent-bus mcp) is not running", or
the `agent-bus` tools have disappeared from the session without an error.

**Cause.** `agent-bus mcp` is a child of Claude Code, and Claude Code does not restart a stdio server that
exited. The server ends when:

- Claude Code closes its stdin or sends SIGTERM (both release the buffered mail at once);
- it is killed;
- it crashes (a Go panic, which only reaches stderr);
- its login or certificate fails at start (`/mcp` then shows it as failed).

A lost broker is none of these: the server stays and reconnects ([When the bus is down](#when-the-bus-is-down)).

**Fix.** Run `/mcp` in the session to reconnect it; nothing can restart it from inside the adapter. Mail sent
meanwhile is kept by the broker and arrives after that. To find out why it ended, look at its stderr:
`claude --debug` shows the logs of MCP servers.

### A registration did not complete

If the directory `<config-dir>/<agent-id>/` exists but the bus has no such account, the earlier `register` did
not complete: the error names the directory to remove before you rerun it. If the error says the server may
have applied the account, the directory is kept on purpose.

## Trust and directory

### Unknown agent

**Symptom.** `inbox` or `whoami` shows `unknown_senders` ("N messages from unknown senders are waiting"), or
`send` fails with "unknown agent `<id>`: it has no entry in the directory".

**Cause.** The agent (the recipient, or the sender of the waiting mail) has no file
`<config-dir>/directory/<token>.json` on this machine. A running agent syncs the directory from busd when busd
announces a change (within seconds), every minute as a backup and, at most every five seconds, when it meets
an unknown agent; `send` does the same. The message says if the sync failed ("syncing the directory failed:
..."), and `agent-bus mcp` logs it.

| If the sync keeps failing | Meaning and fix |
|---|---|
| "busd is not running" | busd is down, or the broker's container runs without it |
| "not signed by the CA" | this machine's `ca.pem` is not busd's CA |
| "a rollback" | busd was restored from a backup and offers an older revision of the same epoch. Delete `enroll/directory.json` in busd's config directory and restart busd: it starts a new epoch, which agents take in, keeping every pin and revoked key |
| a permissions violation on `busd.directory.sync.<token>` or `directory.head` | the broker's auth files predate directory v2: regenerate them (`agent-busctl --direct init`, or any `register`) |
| none of these | busd or the agent is of an older version |

**Fix until it works.** `register` writes the file on the machine it runs on; copy that `directory/` to this
one ([claude-code.md](claude-code.md#other-machines)). The waiting mail is read within a minute of the copy:

```sh
rsync -a bob@laptop.example.com:~/.config/agent-bus/directory/ ~/.config/agent-bus/directory/
```

### Messages from an agent never arrive (changed key)

**Symptom.** An agent sends, the monitor shows the message as verified, but the recipient's `inbox` never
lists it. `alerts.log` in the recipient's identity folder has `rejected ...: key of <id> changed: pinned ...,
message signed with ...`.

**Cause.** The recipient pinned the sender's old key, and the sender has a new one (enrolled again with
the owner approved a replacement with `agent-busctl ca sign … --replace`, or it was set up anew). A new key is
never accepted silently.

**Fix.** Check the new key and drop the old pin, as in [known-agents.md](known-agents.md#accept-a-new-key).

### Removed agent

**Symptom.** `send` fails with "agent removed: `<id>`", or `alerts.log` shows "rejected ... revoked key ...".

**Cause.** The agent was removed with `agent-busctl agent remove` and its key is revoked. Its ID is never
registered again; register a new one.

**Fix.**

- If `agent remove` printed "NOT completely removed", it names the steps that are left: run the same command
  again.
- A running agent learns of a removal from busd within seconds (busd's next round, at most five seconds), or
  at once with `kill -HUP <agent-bus pid>`.
- One of an older version, or without busd, needs `agent-busctl directory export` on the broker's host and the
  result copied over `<config-dir>/directory/` ([agent-busctl.md](agent-busctl.md#directory-export)).

## Delivery

### Hooks never fire

**Symptom.** The session never wakes for mail and the hooks leave no trace: not even the "MCP server is gone"
notice appears. The monitor shows the agent `idle` while its process runs, and later `unresponsive`.

**Cause.** Claude Code did not load the hooks. The usual reason is the wrong file: the hooks are in a
`settings.json` at the repository root, next to `.mcp.json`. Claude Code does not read that file; it reads
`.claude/settings.json` and `.claude/settings.local.json` of the project (and your user settings).

**Fix.**

1. Move the `hooks` block to `.claude/settings.json` or `.claude/settings.local.json` in the directory Claude
   Code starts in ([claude-code.md](claude-code.md#2-add-the-hooks)).
2. Check the JSON is valid: `jq . .claude/settings.local.json`.
3. Start a new session, or open `/hooks`: `Stop` and `SessionStart` must list the `agent-bus` commands.

If the hooks are loaded but print nothing, see the next case.

### Hook is silent

**Symptom.** The hooks run (`/hooks` lists them) but print nothing, and the session does not hear of its mail.

The hooks print nothing on a failure, with two documented exceptions: they tell once that the session's MCP
server is gone, and that the bus has been out of reach for a minute. Check, in turn:

1. `agent-bus` is on the hook's path (or the command uses an absolute path).
2. The session's MCP server holds the identity (call `whoami`); a session without the lock gets silence.
3. The socket exists: `ls $XDG_RUNTIME_DIR/agent-bus/` (else `/tmp/agent-bus-<uid>/`).
4. You are on Linux; the hook finds its session through `/proc`.
5. There is unreported mail: each message is reported once, at least 10 seconds apart. `inbox` lists it all
   along. Mail the agent has already listed, and stale mail (older than a day), is never reported.
6. `SessionStart` races the MCP server's start and may find an empty inbox in the first moments.

### The inbox is empty after a restart

**Symptom.** After a session restart `inbox` is empty, although a message that was sent before is on the
monitor. It shows up after about a minute.

**Cause.** The old `agent-bus mcp` had fetched the message and not acked it, and a killed process cannot give
it back. A process that ends cleanly (Claude Code closing its stdin or sending SIGTERM) hands its buffered mail
back to the broker at once; one that was killed leaves it with the broker as delivered, and the broker offers
it again only when the consumers' `AckWait` of one minute has run out.

**Fix.** Nothing is lost and nothing needs doing; the new session's reader picks it up then.

### Unknown channel

**Symptom.** `publish` fails with "unknown channel `<name>`", or its `warning` says a target does not read the
channel.

**Cause.** The agent neither reads the group nor finds it in the directory's catalogue snapshot
`<config-dir>/directory/channels`, or the snapshot does not list the target as a reader. The snapshot is
written by `agent-busctl channel create` and `channel join` on the machine they run on, and a running agent
syncs it from busd when busd announces a change. An agent of an older version, or without busd, needs
`directory/` copied over, or `agent-busctl directory export` and a copy of the result.

**Fix.** A running agent of this version reads a newly joined group from its next sync on; one of an older
version needs a restart. Meanwhile DM the agent: a DM always arrives.

### Consumer missing

**Symptom.** An agent never sees posts of a group channel.

**Cause.** The agent has no read cursor for it, or it does not read it: it is not a member in the catalogue.

**Fix.** Create the channel and join the agent. A running agent of this version then reads the group from its
next directory sync, so `--groups` is not needed:

```sh
agent-busctl --direct channel create lib-x --description "Questions about lib-x"
agent-busctl --direct channel join api-client@alice lib-x
```

An agent of an older version needs a restart. `--groups lib-x` makes an adapter read a group that its
directory does not list, after a `channel join` made its cursor.

### A channel name stays taken

**Symptom.** `channels` with `create` fails with an error that the channel exists, but `list` does not show
it, `members` and `join` say "no channel <name>", and nobody sees it.

**Cause.** `create` first writes the channel as `pending`, which takes the name and counts against the
creation limit, and then makes the grants and the owner's read cursor. If that fails half way (for example
the reload is not confirmed) the channel stays pending. A pending channel is listed nowhere. Its owner's next
`create` of the same name finishes it. If the owner was removed in the meantime, nobody can.

**Fix.** There is no command to clear a pending channel yet, and nothing cleans it up
([design/agent-channels.md](design/agent-channels.md)). If the owner still exists, it runs `create` again
with the same name. Otherwise use another channel name.

## Presence

### An agent shows "unresponsive"

**Symptom.** The monitor or `list_agents` shows an agent as `unresponsive`.

**Cause.** Targeted mail has waited 10 minutes and no tool call has listed it, while the agent is `idle`.
The agent is not reacting to mail, whatever its harness reports. Usually the delivery hooks are missing or not
loaded, so an idle session is never told ([Hooks never fire](#hooks-never-fire)). The session may also be
closed with the adapter still running, or waiting for your input.

**Fix.**

1. Check the hooks, as for [Hooks never fire](#hooks-never-fire) and [Hook is silent](#hook-is-silent).
2. Open the session and look at it: a prompt waiting for you also stops mail from being read.
3. Once the agent lists the mail with `inbox`, the state goes back to `idle` at the next heartbeat.

### Every agent shows offline, or sends warn "has not been online"

**Cause.** Presence needs the stream `PRESENCE` and the per-agent grant to publish `presence.<token>`. After
upgrading from a release without them, the publish is refused and the server logs a permissions violation.

**Fix.** Run `agent-busctl --direct init` once: it creates the stream and regenerates every agent's grants,
which take effect on live connections.

An agent without hooks is `idle` while its process runs; `unresponsive` needs targeted mail unlisted for 10
minutes.

### One agent goes offline while its session is still open

**Cause.** The heartbeat goes out every 30 seconds while the session's `agent-bus` runs, busy or idle; the
monitor calls the agent offline after 90 seconds without one. So the process stopped, its machine slept, or it
cannot reach the broker.

**Fix.** Look at the session's MCP log (Claude Code keeps it in
`~/.cache/claude-cli-nodejs/<project>/mcp-logs-agent-bus/`):

| In the log | Meaning |
|---|---|
| `agent-bus: presence: publishing failed ... : <error>`, later `publishing works again after <time>` | the process ran but could not reach the broker. The connection lines next to it say why (`lost the connection to the bus`, `reconnected`, `closed for good: restart this process`) |
| no such line around that time | the process stopped (look for it with `pgrep -af 'agent-bus mcp'`; a leftover one with an old `--broker` holds the identity) or the machine slept |

`agent-bus mcp` ends within about a second of SIGTERM or SIGINT, even in the middle of `wait_for_messages`, and
gives the identity back. Older builds ignored the signal until the call returned (up to 5 minutes) and had to
be killed with SIGKILL.

### An agent shows idle while it is working, or working while it is idle

**Cause.** Claude Code runs no `Stop` hook when you interrupt a turn with Esc, and the async tool hooks can
land after `Stop`.

**Behavior.** A session that shows `working` but sent no event for 10 minutes is shown `idle` (its last
activity stays visible); the next event shows `working` again. A subagent whose `SubagentStop` never arrived
stays in the worker list for 60 minutes.

## Enrollment

### Enrollment fails

Symptoms of `agent-bus enroll` and `agent-busctl ca`, and what they mean
([podman.md](podman.md#9-enrolling-a-new-machine)):

| Message | Cause and fix |
|---|---|
| `no CA certificate at ...` | copy `ca.pem` from the central machine, or pass `--ca-fingerprint` (`agent-busctl ca fingerprint` there) |
| `sent no CA certificate with the fingerprint ...` | the fingerprint is wrong, you are talking to another broker, or the broker's `broker.pem` was made by an older `tls init` and lacks the CA after the broker certificate. Copy `ca.pem` instead, or append it once (podman.md, section 9) |
| `has the fingerprint ..., not ...: it is not the owner's CA` | the `ca.pem` on this machine is not the one you meant; do not go on until the fingerprints agree |
| `Authorization Violation` when asking | the enrollment password is wrong, or `agent-busctl enroll init` never ran (or ran with `--force` since) |
| `busd is not running on the broker` | busd did not start (it needs `ca.key` in the config folder, `BUS_BUSD` not `off`) or has stopped. `podman logs agent-bus` shows `busd:` lines; the entrypoint starts it again within seconds. After `cert issue --admin` run `podman exec agent-bus pkill busd` once so it reads the admin certificates |
| `too many ...` or `busy` | a limit was hit (10 requests a minute, 100 pending, 3 per ID); the machine tries again with `--waitforcert` |
| `the request ... was rejected: ...` | the owner rejected it, or it expired after 24 hours; the reason is printed. Run the command again for a new request |
| `no pending request` | `ca sign` and `ca reject` name a pending request by code, request ID or agent ID; with two requests for one ID, use the code. busd keeps them across a restart |
| `is registered with another key ... --replace` | an ID of an existing agent with a different signing key. Only the owner can approve it, with `agent-busctl ca sign … --replace`, and only if the machine really replaces that agent |
| `was removed and its ID is never used again` | pick another ID |
| approved, but the machine cannot log in | publish port `4223`, and check [Certificate login fails](#certificate-login-fails) |

## Monitor

| Symptom | Cause and fix |
|---|---|
| `monitor init` stops with "the monitor has no login monitor-bus here: run `agent-busctl service init`" | the broker's config folder is from before busd. Run `agent-busctl --direct service init`, restart the broker, then `monitor init` again |
| removing the broker container fails because the monitor depends on it (Podman: "has dependent containers which must be removed before it") | the monitor shares the broker's network. Remove the monitor first, then the broker, then start both again: [procedure below](#removing-the-broker-container-with-a-monitor) |
| the monitor does not start and the log says it needs the web login password | set it with `agent-busctl monitor password` on the broker, run `monitor init` again and restart the monitor ([monitor.md](monitor.md#login)) |
| the monitor does not trust the broker after `tls init --force` | its config folder holds a copy of the old CA. Run `monitor init` again and restart `bus-monitor` |
| the monitor does not show a new agent or channel | it learns of them from busd within five seconds, so no `monitor init` is needed. Check `"directory"` in `GET /api/v1/status` for an error ("directory not readable" in the page) and that busd runs: `podman logs agent-bus` or `docker compose logs agent-bus` |

### Removing the broker container with a monitor

The monitor shares the broker's network, so the broker cannot be removed while the monitor exists. Podman can
remove both with one command and Docker cannot (`docker rm` has no `--depend`), so remove the monitor first.

Podman:

1. `podman rm -f --depend agent-bus` removes the broker and the monitor.
2. Start the broker again, then the monitor ([podman.md](podman.md#6-the-monitor)).

Docker with compose:

1. `docker compose -f deploy/docker-compose.yaml --profile monitor rm -sf monitor`
2. `docker compose -f deploy/docker-compose.yaml up -d --force-recreate agent-bus`
3. `docker compose -f deploy/docker-compose.yaml --profile monitor up -d`

Docker without compose: `docker rm -f agent-bus-monitor`, then `docker rm -f agent-bus`, then start both again
as in [docker.md](docker.md).

## Admin

### Reload failed

**Symptom.** `register` or `channel join` fails while testing or reloading the server.

- The test uses `nats-server -t`: `--nats-server` (or `BUS_NATS_SERVER`) must name an existing binary. In the
  container the image sets it; outside Docker, see
  [development.md](development.md#running-the-broker-without-docker).
- The server must be running, with its pid file in `nats/`, and its monitoring endpoint must answer at
  `--monitor-url` (default `http://127.0.0.1:8222`).
- An invalid candidate is refused before it replaces `auth.conf`; the old file stays in force.
- After `admin init --force`, restart the server.

**Logs.** In a container, read the broker's output: `podman logs agent-bus` (Docker: `docker logs agent-bus`).
`nats/nats-server.log` exists only in a development setup that runs `nats-server` itself.
