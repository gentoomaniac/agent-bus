# The broker

The broker is a plain `nats-server` with JetStream, started by `deploy/nats.sh`. For a container
see [docker.md](docker.md). The server has two accounts, `BUS` (the agents) and `CONTROL`
(registry, catalogue and ledger), with no import or export between them (design.md §4a).

## Config directory

All local state lives in `~/.config/agent-bus/`. Change it with `--config-dir` (or
`BUS_CONFIG_DIR`) for `busctl` and `agent-bus`, and with `BUS_CONFIG_DIR` for `deploy/nats.sh`.

```
~/.config/agent-bus/
├── ca.pem, ca.key             private CA (tls init)
├── broker.pem, broker.key     broker certificate (tls init)
├── bus-admin/password         admin of account BUS (admin init, mode 0600)
├── control-admin/password     admin of account CONTROL (admin init, mode 0600)
├── nats/
│   ├── auth.conf              generated, account BUS (never edit by hand)
│   ├── control-auth.conf      generated, account CONTROL (never edit by hand)
│   ├── nats-server.conf       rendered from deploy/nats-server.conf by `up`
│   ├── nats-server.pid, nats-server.log
│   └── data/                  JetStream storage
├── bin/nats-server            the pinned server (deploy/nats.sh install)
├── directory/                 registry records, one per agent (register)
└── <agent-id>/                key, password, lock, handled.jsonl, alerts.log (register)
```

## Running it locally

1. Create the certificates and admin credentials once ([TLS](#tls), [admin credentials](#admin-credentials)):

   ```sh
   bin/busctl tls init
   bin/busctl admin init
   ```

2. Build the pinned `nats-server` (v2.15.3, with `go install`) into `~/.config/agent-bus/bin/`:

   ```sh
   deploy/nats.sh install
   ```

3. Start it. `up` renders `deploy/nats-server.conf` to `~/.config/agent-bus/nats/nats-server.conf`,
   tests it, starts the server as a plain background process and waits until its monitoring
   endpoint says it is ready:

   ```sh
   deploy/nats.sh up
   ```

4. Create the streams and buckets (safe to repeat):

   ```sh
   bin/busctl --direct init
   ```

5. Stop it. `down` keeps the data, `down --purge` deletes it too:

   ```sh
   deploy/nats.sh down
   deploy/nats.sh down --purge
   ```

Pid file and log are in `nats/`.

## Settings and ports

Environment variables of `deploy/nats.sh`:

| Variable           | Default                          | Meaning                                         |
|--------------------|----------------------------------|-------------------------------------------------|
| `BUS_CONFIG_DIR`   | `~/.config/agent-bus`            | config directory                                |
| `BUS_NATS_SERVER`  | `$BUS_CONFIG_DIR/bin/nats-server`| the server binary (must be named `nats-server`) |
| `BUS_NATS_PORT`    | `4222`                           | client port (TLS only)                          |
| `BUS_MONITOR_PORT` | `8222`                           | monitoring port, 127.0.0.1 only                 |
| `BUS_LISTEN_IP`    | `0.0.0.0`                        | address of the client port                      |
| `BUS_DATA_DIR`     | `$BUS_CONFIG_DIR/nats/data`      | JetStream storage                               |

The client port is published on all interfaces by default, so the bus works on the LAN. Set
`BUS_LISTEN_IP=127.0.0.1` to keep it on this machine. The only listener is TLS; the monitoring
endpoint is local.

## TLS

```sh
bin/busctl tls init
```

This creates a private CA (`ca.pem`, `ca.key`) and the broker certificate (`broker.pem`,
`broker.key`). The certificate is valid for `localhost`, `127.0.0.1`, `::1` and this machine's
hostname. Agents on other machines connect by a name or IP that must be in the certificate, so
list them with `--san` (repeatable) before you create it:

```sh
bin/busctl tls init --san bus.example.org --san 192.168.1.10
```

`tls init` refuses to overwrite existing files unless you pass `--force`; clients must then get
the new `ca.pem`. Clients trust only `ca.pem` (`--ca-cert`, default `ca.pem` in the config
directory), so copy that file, and only that file, to every machine that connects. Agents on other
machines then connect with `--broker bus.example.org:4222`.

## Admin credentials

```sh
bin/busctl admin init
```

This creates the passwords of the two admin users in `bus-admin/password` and
`control-admin/password` (NATS user names are server-wide, so each account has its own admin) and
writes the server's authorization files `nats/auth.conf` (account `BUS`) and
`nats/control-auth.conf` (account `CONTROL`) with their bcrypt hashes. Those files are generated.
`--force` replaces the passwords and keeps the registered agents; a running server must then be
restarted (`deploy/nats.sh down`, `deploy/nats.sh up`).

## Reloads

`register` and `channel join` change `nats/auth.conf`. Each change is one operation:

1. lock the file,
2. test the new configuration with `nats-server -t`,
3. replace the file atomically,
4. send the server SIGHUP,
5. wait until the monitoring endpoint (`--monitor-url`, default `http://127.0.0.1:8222`) reports a
   newer config load time.

An invalid configuration is refused before it replaces the file. `busctl` needs the server binary
for the test (`--nats-server`, default `~/.config/agent-bus/bin/nats-server`) and the pid file in
`nats/`. For a failed reload see [troubleshooting](troubleshooting.md#reload-failed).
