# agent-messenger

A Kafka-based message bus for coding agents. The spec is [design.md](design.md).

Development: `make check` runs gofmt, `go vet` and the unit tests (no broker needed).

## Setup

All local state lives in `~/.config/agent-bus/` (`--config-dir` or `BUS_CONFIG_DIR` to change it).

1. `go build -o bin/ ./cmd/busctl`
2. `bin/busctl tls init` creates a private CA (`ca.pem`, `ca.key`) and the broker certificate
   (`broker.pem`, `broker.key`). It is valid for `localhost`, `127.0.0.1`, `::1` and this
   machine's hostname; use `--san` (repeatable) to set other names or IPs. It refuses to
   overwrite existing files unless `--force`. Clients trust only `ca.pem` (`--ca-cert`).
3. `bin/busctl admin init` creates the admin (SCRAM superuser) password in
   `~/.config/agent-bus/admin/password` (mode 0600). The broker is bootstrapped with it and
   busctl connects with it. It never lives in the repo.
