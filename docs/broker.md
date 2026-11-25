# The broker

The broker is a plain `nats-server` with JetStream. It runs in the Docker image ([docker.md](docker.md)),
together with `busd` (enrollment) and `agent-busctl`; agent machines hold only the files they copy out of its config folder. The server has two accounts, `BUS` (the agents) and `CONTROL`
(registry, catalogue and ledger), with no import or export between them ([design/jetstream-binding.md](design/jetstream-binding.md)).

## Config directory

On the broker all state lives in the container's `/config` folder. On an agent machine only a part
of it is in `~/.config/agent-bus/` ([docker.md](docker.md#give-an-agent-machine-its-files)). Change
it with `--config-dir` (or `BUS_CONFIG_DIR`) for `agent-bus`. The layout, the same in both places:

```text
~/.config/agent-bus/       (the broker: /config)
├── ca.pem, ca.key             private CA (tls init)
├── broker.pem, broker.key     broker certificate (tls init)
├── bus-admin/password         admin of account BUS (admin init, mode 0600)
├── control-admin/password     admin of account CONTROL (admin init, mode 0600)
├── busd/password              busd's login (admin init)
├── monitor-bus/password       the monitor's read-only logins (admin init)
├── monitor-control/password
├── monitor-web/password.bcrypt  the monitor's web password (monitor password)
├── enroll/                    busd's enrollment password and state (enroll init)
├── nats/
│   ├── auth.conf              generated, account BUS (never edit by hand)
│   ├── control-auth.conf      generated, account CONTROL (never edit by hand)
│   ├── nats-server.conf       rendered from deploy/nats-server.conf at container start
│   └── nats-server.pid        (outside Docker also nats-server.log, data/ and bin/nats-server)
├── directory/                 registry records, one per agent (register)
└── <agent-id>/                key, password, client.pem, client.key, lock, handled.jsonl, alerts.log
                               (register, cert issue); replaced files are kept as *.retired
```

## Running it

The broker runs in the Docker image, set up as in [docker.md](docker.md#set-it-up): one-off
containers create the certificates and the admin credentials in the `config/` folder, then
`docker compose -f deploy/docker-compose.yaml up -d` starts it and
`agent-busctl --direct init` creates the streams and buckets (safe to repeat). `docker compose ... stop`
keeps the data; to start over, remove the `config/` and `data/` folders. The server log is
`docker compose ... logs agent-bus`. Running it as a plain process, which the integration tests do,
is for development only ([development.md](development.md#running-the-broker-without-docker)).

## Settings and ports

Environment variables of the container (set them in the compose file):

| Variable           | Default      | Meaning                                         |
|--------------------|--------------|-------------------------------------------------|
| `BUS_NATS_PORT`    | `4222`       | client port, password login (TLS only)          |
| `BUS_MAPPED_PORT`  | `4223`       | client port, certificate login (TLS, `wss://`)  |
| `BUS_MONITOR_PORT` | `8222`       | monitoring port, 127.0.0.1 only                 |
| `BUS_LISTEN_IP`    | `0.0.0.0`    | address of the client ports                     |
| `BUS_BUSD`         | `on`         | `off` runs the broker without busd              |

`BUS_CONFIG_DIR`, `BUS_DATA_DIR` and `BUS_NATS_SERVER` are fixed by the image (`/config`, `/data`,
the bundled server).

The client port is published on all interfaces by default, so the bus works on the LAN. Publish it
as `127.0.0.1:4222:4222` in the compose file to keep it on this machine. Both client listeners are
TLS; the monitoring endpoint is inside the container. The second port, `4223`, is where agents and
admin tools log in with a client certificate instead of a password ([podman.md](podman.md)); a
broker that has no certificate users simply never sees a connection there. Publish it as well to
use certificates from other machines.

If you change `BUS_NATS_PORT` or `BUS_MONITOR_PORT`, give `agent-busctl` in the container and `agent-bus`
on the agent machines the matching addresses, or `register` cannot confirm the reload and agents
later fail with `nats: Authorization Violation`:

```sh
export BUS_BROKER=<broker-host>:<nats-port>        # agent machines
export BUS_MONITOR_URL=http://127.0.0.1:<monitor-port>   # set as environment of the container
```

## TLS

```sh
docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint agent-busctl agent-bus tls init
```

This creates a private CA (`ca.pem`, `ca.key`) and the broker certificate (`broker.pem`,
`broker.key`). The certificate is valid for `localhost`, `127.0.0.1`, `::1` and this machine's
hostname. Agents on other machines connect by a name or IP that must be in the certificate, so
list them with `--san` (repeatable) before you create it:

```sh
docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint agent-busctl agent-bus tls init --san bus.example.com --san 192.0.2.10
```

To add names later, keep the CA and reissue only the broker certificate with `agent-busctl tls
broker --san ...` ([agent-busctl](agent-busctl.md#tls-broker)); clients need nothing new.

`tls init` refuses to overwrite existing files unless you pass `--force`. A forced `tls init` makes
a new CA: every client that does not get the new `ca.pem` stops trusting the broker, and the
broker must be restarted to load the new certificate. Clients trust only `ca.pem` (`--ca-cert`,
default `ca.pem` in the config directory), so copy that file, and only that file, to every machine
that connects. Agents on other machines then connect with `--broker bus.example.com:4222`.

## Admin credentials

```sh
docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint agent-busctl agent-bus admin init
```

This creates the passwords of the two admin users in `bus-admin/password` and
`control-admin/password` (NATS user names are server-wide, so each account has its own admin) and
writes the server's authorization files `nats/auth.conf` (account `BUS`) and
`nats/control-auth.conf` (account `CONTROL`) with their bcrypt hashes. Those files are generated.
`--force` replaces the passwords and keeps the registered agents; a running server must then be
restarted (`docker compose -f deploy/docker-compose.yaml restart agent-bus`).

## Reloads

Every command that changes access changes `nats/auth.conf` (`register`, `channel join`, `cert issue`,
`cert switch`, `service init`, `enroll init`, and busd's approvals). Each change is one operation:

1. lock the file,
2. test the new configuration with `nats-server -t`,
3. replace the file atomically,
4. send the server SIGHUP,
5. wait until the monitoring endpoint (`--monitor-url`, default `http://127.0.0.1:8222`) reports a
   newer config load time.

An invalid configuration is refused before it replaces the file. `agent-busctl` needs the server binary
for the test (`--nats-server`, set by the image) and the pid file in `nats/`; in the container both
are there. For a failed reload see [troubleshooting](troubleshooting.md#reload-failed).

**Only the broker's container reloads.** The signal goes to the pid in `nats/nats-server.pid`, which
is a pid of the broker container's process namespace (normally `1`). In another container, for
example the monitor, which shares the network but not the processes, that pid is something else.
So every command that changes access runs in the broker container (`podman exec agent-bus
agent-busctl --direct ...`, the alias of [podman.md](podman.md)). The monitor and every other
container only read the config folder and never reload. Nothing needs a signal across containers,
and the container engine's socket is not mounted anywhere. Enrollment follows the
same rule ([design/access-control.md](design/access-control.md), phase B): `busd` runs in the broker container,
holds the CA key and makes its auth-file changes and reloads there; the monitor only shows the number
of pending requests.
