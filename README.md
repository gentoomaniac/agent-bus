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

The Kafka broker code and the Redpanda manifest are gone; `register`, `channel create` and the deploy come back on NATS in the next commits.
