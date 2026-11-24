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
4. `deploy/broker.sh up` starts Redpanda (pod manifest `deploy/redpanda.yaml`, started with
   `podman kube play`) and waits until it is ready. `deploy/broker.sh down` stops it and keeps its
   data, `down --purge` deletes the data too. Settings are environment variables
   (`BUS_CONFIG_DIR`, `BUS_POD_NAME`, `BUS_KAFKA_PORT`, `BUS_ADVERTISE_HOST`; see the script).
   The broker has only a TLS + SASL/SCRAM listener, topic auto-creation is off and ACLs are
   enforced. For clients on other machines set `BUS_ADVERTISE_HOST` to a name that is in the
   certificate's SANs. The image tag is pinned in the manifest.
5. `bin/busctl init` creates the system topics (`agents.registry`, `agents.general`,
   `agents.requests`, `agents.topics`) with the retention from design.md §4: compacted for the
   registry, requests and topic catalogue, infinite for `agents.general`. All topics have one
   partition. Safe to repeat. `busctl` connects to `--broker` (`BUS_BROKER`, default
   `localhost:9092`) as the admin, trusting `--ca-cert`.
6. `bin/busctl topic create <name> --description "..."` creates the one-partition topic
   `agents.topic.<name>` (infinite retention) and writes the description to the compacted
   `agents.topics` catalogue, keyed by name. Repeating it updates the description.
