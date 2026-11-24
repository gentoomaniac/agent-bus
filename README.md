# agent-messenger

A message bus for coding agents, being moved from Kafka to NATS JetStream (design.md §4a, milestone 1a). The spec is [design.md](design.md).

Development: `make check` runs gofmt, `go vet` and the unit tests.

## Setup

All local state lives in `~/.config/agent-bus/` (`--config-dir` or `BUS_CONFIG_DIR` to change it).

1. `go build -o bin/ ./cmd/busctl`
2. `bin/busctl tls init` creates a private CA (`ca.pem`, `ca.key`) and the broker certificate
   (`broker.pem`, `broker.key`). It is valid for `localhost`, `127.0.0.1`, `::1` and this
   machine's hostname; use `--san` (repeatable) to set other names or IPs. It refuses to
   overwrite existing files unless `--force`. Clients trust only `ca.pem` (`--ca-cert`).
3. `deploy/nats.sh install` builds the pinned `nats-server` (v2.15.3, `go install`) into
   `~/.config/agent-bus/bin/`. `deploy/nats.sh up` renders `deploy/nats-server.conf` to
   `~/.config/agent-bus/nats/nats-server.conf`, tests it, starts the server as a plain background
   process (pid file and log in that directory, data in `nats/data`) and waits until its monitoring
   endpoint says it is ready; `down` stops it and keeps the data, `down --purge` deletes it too.
   Settings are environment variables (`BUS_CONFIG_DIR`, `BUS_NATS_SERVER`, `BUS_NATS_PORT`,
   `BUS_MONITOR_PORT`, `BUS_LISTEN_IP`; see the script). The port is published on all interfaces
   (`BUS_LISTEN_IP=0.0.0.0`) so the bus works on the LAN; `BUS_LISTEN_IP=127.0.0.1` keeps it on this
   machine. The only listener is TLS (`broker.pem` from `tls init`); the monitoring endpoint is
   local. The server has two accounts, `BUS` and `CONTROL`, with no import or export between them
   (design.md §4a). Their users come from `auth.conf` and `control-auth.conf` in the same directory
   (mode 0600, generated, never edited by hand): `up` needs them, and the busctl commands that
   write them come with the next commits.

The Kafka broker code and the Redpanda manifest are gone; `register`, `channel create` and the deploy come back on NATS in the next commits.
