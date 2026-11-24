# Troubleshooting

## TLS errors

Symptom: a client fails with a certificate error (`x509: certificate is valid for …, not …`,
`unknown authority`).

- The host in `--broker` must be in the broker certificate. Recreate it with every name or IP
  clients use, restart the broker and give clients the new `ca.pem`:

  ```sh
  docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint busctl agent-bus tls init --force --san bus.example.org --san 192.168.1.10
  docker compose -f deploy/docker-compose.yaml restart agent-bus
  ```

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
- Messages are durable. The broker keeps them on disk, a message that was delivered and not acked
  is delivered again, and an agent skips what it has handled, so after the reconnect you get each
  message once, including those sent during the outage.

The commands that run once (`busctl`, `agent-bus send`, `publish`, `inbox`, `mark-read`) do not retry:
they fail at once with the connection error. If the bus stays down, see whether the broker runs
(`docker compose -f deploy/docker-compose.yaml up -d`).

## Identity in use

Symptom: every tool fails with "the identity is in use by another session (adapter process
`<pid>`)".

Another process holds the identity: a second Claude Code session, or an `agent-bus` command that
is still running. Close it, or give this session its own identity
([claude-code.md](claude-code.md#one-session-per-identity)). The lock is released when the process
ends, so there is nothing stale to remove.

## Unknown agent

Symptom: `inbox` or `whoami` shows `unknown_senders` ("N messages from unknown senders are waiting")
or `send` fails with "unknown agent `<id>`: it has no entry in the directory".

The agent (the recipient, or the sender of the waiting mail) has no file
`<config-dir>/directory/<token>.json` on this machine. `register` writes it on the machine it runs
on; copy that `directory/` to this one, for example `rsync -a other:~/.config/agent-bus/directory/
~/.config/agent-bus/directory/` ([claude-code.md](claude-code.md#other-machines)). The waiting mail
is read within a minute of the copy.

## Unknown channel

Symptom: `publish` fails with "unknown channel `<name>`", or its `warning` says a target does not
read the channel.

The directory's catalogue snapshot `<config-dir>/directory/channels` does not list the group, or
does not list the target as a reader. It is written by `busctl channel create` and `channel join`
on the machine they run on. Copy `directory/` over (or `busctl directory export` and copy the
result) and restart the adapter to read a newly joined group. Meanwhile DM the agent: a DM always
arrives.

## Removed agent

Symptom: `send` fails with "agent removed: `<id>`", or `alerts.log` shows "rejected ... revoked
key ...".

The agent was removed with `busctl agent remove` and its key is revoked. Its ID is never
registered again; register a new one. If `agent remove` printed "NOT completely removed", it names
the steps that are left: run the same command again. A machine learns of a removal when its
`directory/` is refreshed: `busctl directory export` on the broker's host, copy the result over
`<config-dir>/directory/` ([busctl.md](busctl.md#directory-export)); a running session picks it up
within a minute, or at once with `kill -HUP <agent-bus pid>`.

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
upgrading from a release without them, run `busctl --direct init` once: it creates the stream and
regenerates every agent's grants, which take effect on live connections; until then the publish
is refused and the server logs a permissions violation. An agent without hooks is `idle` while its process runs; `unresponsive` needs targeted mail
unlisted for 10 minutes.

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
bin/busctl --direct channel create lib-x --description "..."
bin/busctl --direct channel join api-client@alice lib-x
```

Also, a post to a group nobody created is stored and never read.

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
runs on non-default ports, but `busctl` still uses the defaults. Point both tools at the real
ports and rerun `register`:

```sh
export BUS_BROKER=localhost:<nats-port>
export BUS_MONITOR_URL=http://127.0.0.1:<monitor-port>
```

See [Settings and ports](broker.md#settings-and-ports).
