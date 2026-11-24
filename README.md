# agent-messenger

A message bus for coding agents on NATS JetStream. The spec is [design.md](design.md).

Development: `make check` runs gofmt, `go vet` and the unit tests.

## Setup

All local state lives in `~/.config/agent-bus/` (`--config-dir` or `BUS_CONFIG_DIR` to change it).

1. `go build -o bin/ ./cmd/busctl`
2. `bin/busctl tls init` creates a private CA (`ca.pem`, `ca.key`) and the broker certificate
   (`broker.pem`, `broker.key`). It is valid for `localhost`, `127.0.0.1`, `::1` and this
   machine's hostname; use `--san` (repeatable) to set other names or IPs. It refuses to
   overwrite existing files unless `--force`. Clients trust only `ca.pem` (`--ca-cert`).
3. `bin/busctl admin init` creates the passwords of the two admin users in
   `~/.config/agent-bus/bus-admin/password` and `control-admin/password` (mode 0600; NATS user
   names are server-wide, so each account has its own admin) and writes the server's authorization
   files `nats/auth.conf` (account `BUS`) and `nats/control-auth.conf` (account `CONTROL`) with
   their bcrypt hashes. Those files are generated and never edited by hand. `--force` replaces
   the passwords and keeps the registered agents; a running server must then be restarted.
4. `deploy/nats.sh install` builds the pinned `nats-server` (v2.15.3, `go install`) into
   `~/.config/agent-bus/bin/`. `deploy/nats.sh up` renders `deploy/nats-server.conf` to
   `~/.config/agent-bus/nats/nats-server.conf`, tests it, starts the server as a plain background
   process (pid file and log in that directory, data in `nats/data`) and waits until its monitoring
   endpoint says it is ready; `down` stops it and keeps the data, `down --purge` deletes it too.
   Settings are environment variables (`BUS_CONFIG_DIR`, `BUS_NATS_SERVER`, `BUS_NATS_PORT`,
   `BUS_MONITOR_PORT`, `BUS_LISTEN_IP`; see the script). The port is published on all interfaces
   (`BUS_LISTEN_IP=0.0.0.0`) so the bus works on the LAN; `BUS_LISTEN_IP=127.0.0.1` keeps it on this
   machine. The only listener is TLS (`broker.pem` from `tls init`); the monitoring endpoint is
   local. The server has two accounts, `BUS` and `CONTROL`, with no import or export between them
   (design.md §4a).
5. `bin/busctl --direct init` creates stream `BUS` (every conversation channel, infinite
   retention, a 24-hour dedupe window) in `BUS`, and stream `LEDGER` and the key-value buckets
   `registry` and `catalogue` in `CONTROL`. Safe to repeat. Until busd exists (milestone 3) every
   command that needs admin rights takes `--direct`: busctl then connects to `--broker`
   (`BUS_BROKER`, default `localhost:4222`) as `bus-admin` and `control-admin` (two connections),
   trusting `--ca-cert`. It must run on the broker's host, because access changes rewrite
   `nats/auth.conf`. Every busctl command gives up after `--timeout` (`BUS_TIMEOUT`, default `30s`)
   and stops on Ctrl-C.
6. `bin/busctl --direct channel create <name> --description "..."` writes the group channel to the
   `catalogue` (key = name) and gives every agent the grants for its read cursor on it. Repeating it
   updates the description.
7. `bin/busctl --direct register <name>@<owner>` (e.g. `api-client@alice`) registers an agent
   identity: it generates an Ed25519 key pair and a password in
   `~/.config/agent-bus/<agent-id>/` (`key`, `password`; directory 0700, files 0600), creates the
   agent's read cursors `<token>__dm` and `<token>__general` (durable pull consumers with the
   fixed configuration of design.md §4a), writes the `AgentRecord` (with its `keys` list) to the
   `registry` under the agent token (`lib-x@bob` is `lib-x_bob`), and adds the agent's user and
   grants to `auth.conf`. Every change of `auth.conf` is one operation: lock, test the new
   configuration with `nats-server -t`, replace the file atomically, send the server SIGHUP and
   wait until its monitoring endpoint (`--monitor-url`, default `http://127.0.0.1:8222`) reports a
   newer config load time. `--nats-server` names the binary for the test (default
   `~/.config/agent-bus/bin/nats-server`). Keys are per machine and never copied: registering an
   ID that exists on this machine or on the bus is an error. `human@<owner>` is not supported yet.
   The account is created last. A registration that fails before it is requested removes its
   directory again; after that point the server may have applied the account even if the
   confirmation failed, so the directory is kept and the error says so. If the directory exists but
   the bus has no such account, the earlier registration did not complete: the error names the
   directory to remove before rerunning.

## Code layout

`internal/bus` holds the broker-neutral model and the two interfaces (`Transport`, `Admin`).
`internal/broker` implements them on NATS JetStream and is the only package that imports the NATS
client library (`internal/itest` also does, to test against a real server).
`internal/identity` also reads the private key back and takes the identity lock (design.md §3): an
exclusive `flock` on `<agent-dir>/lock` holding the owner's PID, released by the OS when the process
ends.
`internal/adapter` is the adapter core. Its handled set (design.md §4) is the append-only file
`<agent-dir>/handled.jsonl`, synced to disk before the broker is acked: verified messages by
(destination, sender, ID) with a digest of the signed record, rejected deliveries by stream sequence
only. No SQLite: a file read into memory at start is enough for one identity. Outgoing messages
are rate limited (30 per 10 minutes, in memory) and their bodies scanned for obvious secrets
(private keys, AWS, GitHub, Slack and Anthropic tokens, bearer tokens, `password=`-style
assignments); a match blocks the send. The inbox buffer holds verified, unacknowledged messages, at
most 500 and 8 MB; the reader fetches only as many records as would still fit (counting each as
96 KB) and tells the broker `InProgress` for every buffered message every 20 seconds, inside the
consumers' one-minute `AckWait`.
`internal/trust` is the `known_agents` file of an identity (pinned public keys, design.md §6) and
trust on first use: a sender with no pin is looked up in the registry record, its `key_fp` must name
a key in it that is not revoked, and the key is pinned (`tofu`) after the signature verified. A
changed key is refused. Not done yet: holds, rotation chains and `revoked_keys`. Agents cannot read
the registry on the broker (§4a) and busd does not exist yet, so the MVP registry read path is
`<config-dir>/directory/<agent-token>.json`, one record per agent, which `busctl register` writes.
To use an agent from another machine, copy that directory there.
`internal/envelope` is the signed message (design.md §5, §6): its fields, the canonical JSON that is
signed, signing and the stateless checks (size, version, `to`, timestamp). The canonical form is
RFC 8785 (JCS), implemented in the package for the JSON subset the bus produces (integers up to
2^53, no floats); a message with other numbers cannot be verified.

## Tests

`make check` needs no broker. `make itest` builds the pinned nats-server into `bin/` (`deploy/nats.sh
install`), then starts and stops its own server (temporary config directory, random ports, TLS) with
`deploy/nats.sh` and checks, at the raw NATS level and through `busctl --direct`:

- TLS only: no plaintext, no untrusted CA, no wrong password; the two admin users are distinct and
  each sees only its own account.
- Two-account isolation: an agent cannot publish, subscribe or call JetStream for anything in
  `CONTROL`; even `bus-admin` (all subjects) cannot see it. Every JetStream API an agent holds,
  called with a reply subject of `$KV.registry.<key>`, `$KV.catalogue.<key>`, `ledger.<x>` or a
  `busd.*` subject, delivers inside `BUS` only and leaves `CONTROL` untouched. A pulled record sent
  to a reply subject is delivered but not stored again; a JetStream API response sent to one is
  stored anew with a new stream sequence.
- Per-role grants: an agent cannot pull from, inspect or ack another agent's consumer, read its DM
  channel, direct-get a message, subscribe to another agent's inbox prefix or to `busd.>`, publish
  to `private.*`, `$KV.*`, `ledger.*` or an owner-only busd method, or create, update or delete a
  consumer, its own included; what it is granted works.
- Consumers: `register` creates `<token>__dm` and `<token>__general` with the fixed configuration
  (`MaxDeliver` -1, `MaxAckPending` 500 holding back the 501st unacknowledged message, `AckWait` 1
  minute); a group cursor starts after the boundary it is given; the stream sequence is in the
  delivery metadata; history moves no cursor; `LastSequence` reads the stream's last sequence.
- `Nats-Msg-Id` dedupe keyed by destination subject and envelope ID; `LEDGER` refuses a stale
  `Nats-Expected-Last-Subject-Sequence`.
- Reloads: a grant added or revoked takes effect on a live connection, a removed user is
  disconnected, an invalid candidate is refused before it replaces `auth.conf`, and each reload is
  confirmed by the monitoring endpoint's config load time.
- The registry and catalogue hold what `register` and `channel create` wrote; `busctl` needs
  `--direct`, refuses a second registration and gives up on a dead broker.
