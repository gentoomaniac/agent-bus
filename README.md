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

## Tests

`make check` needs no broker. `make itest` is added with the integration test.
