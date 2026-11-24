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
   (`BUS_CONFIG_DIR`, `BUS_POD_NAME`, `BUS_KAFKA_PORT`, `BUS_ADVERTISE_HOST`, `BUS_LISTEN_IP`; see the
   script). The port is published on all interfaces (`BUS_LISTEN_IP=0.0.0.0`) so the bus works on
   the LAN; to keep the broker on this machine, start it with `BUS_LISTEN_IP=127.0.0.1`.
   The broker has only a TLS + SASL/SCRAM listener, topic auto-creation is off and ACLs are
   enforced. For clients on other machines set `BUS_ADVERTISE_HOST` to a name that is in the
   certificate's SANs. The image tag is pinned in the manifest.
5. `bin/busctl init` creates the system topics (`agents.registry`, `agents.general`,
   `agents.requests`, `agents.topics`) with the retention from design.md §4: compacted for the
   registry, requests and topic catalogue, infinite for `agents.general`. All topics have one
   partition (infinite means `retention.ms=-1`, `retention.bytes=-1`, `cleanup.policy=delete`). Safe to
   repeat: for topics that already exist it applies these configs again, so a broker created by an
   older version picks them up. `busctl` connects to `--broker` (`BUS_BROKER`, default
   `localhost:9092`) as the admin, trusting `--ca-cert`. Every busctl command gives up after `--timeout` (`BUS_TIMEOUT`, default
   `30s`) and stops on Ctrl-C, so an unreachable broker ends in an error instead of a hang.
6. `bin/busctl topic create <name> --description "..."` creates the one-partition topic
   `agents.topic.<name>` (infinite retention) and writes the description to the compacted
   `agents.topics` catalogue, keyed by name. Repeating it updates the description.
7. `bin/busctl register <name>@<owner>` (e.g. `api-client@alice`) registers an agent identity:
   it generates an Ed25519 key pair and a SCRAM password in `~/.config/agent-bus/<agent-id>/`
   (`key`, `password`; directory 0700, files 0600), creates the DM topic
   `agents.dm.<name>.<owner>`, the SCRAM user, the ACLs from design.md §4, and publishes the public
   key and fingerprint to `agents.registry`. Keys are per machine and never copied: registering an
   ID that exists on this machine or on the bus is an error. `human@<owner>` is not supported yet.
   Agents may read, write and describe every group topic (`agents.topic.*`, open channels) through
   one prefix ACL. Agents registered before that ACL existed lack it: run `bin/busctl acls <agent-id>`,
   which applies the current ACLs of a registered agent again (safe to repeat).
   A registration that fails before the SCRAM user is sent removes its directory again. After that
   point the broker may have applied the user even if the answer was lost, so the directory is kept
   and the error says so. If the directory exists but the broker has no such user, the earlier
   registration did not complete: the error names the directory to remove before rerunning.

## Tests

`make check` needs no broker. `make itest` starts its own broker pod (`agent-bus-itest-<pid>`, random port, temporary config directory, removed afterwards) with `deploy/broker.sh` and checks: no plaintext or untrusted-CA connections, an agent reads its own DM topic but is denied the other agent's, no topic auto-creation, topic layout, registry and catalogue contents. It needs podman and the pinned Redpanda image (`podman pull docker.io/redpandadata/redpanda:v26.1.21`).
