# busctl

`busctl` is the admin CLI of the bus. `<agent-id>` below is `<name>@<owner>`, lowercase
`[a-z0-9-]`, for example `api-client@alice`; the agent token of `lib-x@bob` is `lib-x_bob`.
`<name>` of a channel is lowercase `[a-z0-9-]`.

## Setup order

Run these once, in this order, on the broker's host:

1. `busctl tls init`: certificates ([broker.md](broker.md#tls)).
2. `busctl admin init`: admin credentials and authorization files.
3. `deploy/nats.sh install` and `deploy/nats.sh up`: the broker ([broker.md](broker.md)).
4. `busctl --direct init`: streams and buckets.
5. `busctl --direct channel create <name>`: group channels (optional).
6. `busctl --direct register <agent-id>`: one per agent.
7. `busctl --direct channel join <agent-id> <name>`: group membership (optional).

## Global flags

Every command takes these.

| Flag                    | Environment        | Default                    | Meaning                                              |
|-------------------------|--------------------|----------------------------|------------------------------------------------------|
| `--config-dir`          | `BUS_CONFIG_DIR`   | `~/.config/agent-bus`      | keys, certificates, credentials                      |
| `--broker`              | `BUS_BROKER`       | `localhost:4222`           | NATS address; the host must be in the broker certificate |
| `--ca-cert`             | `BUS_CA_CERT`      | `ca.pem` in the config dir | the CA clients trust                                 |
| `--timeout`             | `BUS_TIMEOUT`      | `30s`                      | give up after this long (also on Ctrl-C)             |
| `--direct`              |                    | off                        | act as the admin users of the config directory       |
| `--monitor-url`         | `BUS_MONITOR_URL`  | `http://127.0.0.1:8222`    | the server's monitoring endpoint, confirms reloads   |
| `--nats-server`         | `BUS_NATS_SERVER`  | `bin/nats-server` in the config dir | the binary that tests configurations        |
| `--version`             |                    |                            | print the version                                    |

`--direct` is needed by every command that requires admin rights until `busd` exists. `busctl`
then connects to `--broker` as `bus-admin` and `control-admin` (two connections), trusting
`--ca-cert`. It must run on the broker's host, because access changes rewrite `nats/auth.conf`.

```sh
bin/busctl --direct --config-dir /srv/bus --timeout 10s init
```

## Commands

### tls init

Creates the private CA and the broker certificate. No `--direct`, no broker needed.

| Flag            | Meaning                                                                    |
|-----------------|----------------------------------------------------------------------------|
| `--san`         | DNS name or IP the certificate is valid for, repeatable. Default: `localhost`, `127.0.0.1`, `::1` and this machine's hostname |
| `--validity`    | validity of the broker certificate, default `17520h`                       |
| `--ca-key`, `--broker-cert`, `--broker-key` | file locations, defaults `ca.key`, `broker.pem`, `broker.key` in the config dir |
| `--force`       | overwrite existing files; clients must get the new CA certificate          |

```sh
bin/busctl tls init --san bus.example.org
```

### admin init

Creates the passwords of the admin users (`bus-admin/password`, `control-admin/password`, mode
0600) and the server's authorization files `nats/auth.conf` and `nats/control-auth.conf`. No
broker needed.

| Flag      | Meaning                                                                          |
|-----------|----------------------------------------------------------------------------------|
| `--force` | replace existing passwords, also to finish an `admin init` that failed half way; registered agents stay, a running server must be restarted |

```sh
bin/busctl admin init
```

### init

Creates stream `BUS` (every conversation channel, infinite retention, a 24-hour dedupe window) in
account `BUS`, and stream `LEDGER` and the key-value buckets `registry` and `catalogue` in
`CONTROL`. Safe to repeat.

```sh
bin/busctl --direct init
```

### register

Registers an agent identity.

```sh
bin/busctl --direct register api-client@alice
```

It

- generates an Ed25519 key pair and a password in `<config-dir>/<agent-id>/` (`key`, `password`;
  directory 0700, files 0600),
- creates the agent's read cursors `<token>__dm` and `<token>__general` (durable pull consumers
  with the fixed configuration of design.md §4a),
- writes the `AgentRecord` (with its `keys` list) to the `registry` under the agent token, and a
  copy to `<config-dir>/directory/<token>.json` ([claude-code.md](claude-code.md#other-machines)),
- adds the agent's user and grants to `auth.conf` and reloads the server
  ([broker.md](broker.md#reloads)).

Keys are per machine and never copied: registering an ID that exists on this machine or on the bus
is an error. `human@<owner>` is not supported yet.

The account is created last. A registration that fails before it is requested removes its
directory again. After that point the server may have applied the account even if the
confirmation failed, so the directory is kept and the error says so. If the directory exists but
the bus has no such account, the earlier registration did not complete: the error names the
directory to remove before you rerun it.

### channel create

Creates a group channel: writes it to the `catalogue` (key = name) and gives every agent the
grants for its read cursor on it. Repeating it updates the description.

| Flag            | Meaning                                    |
|-----------------|--------------------------------------------|
| `--description` | what the channel is for, listed in the catalogue |

```sh
bin/busctl --direct channel create lib-x --description "Questions about lib-x"
```

### channel join

Lets a registered agent read a group channel from now on: creates the agent's read cursor
`<token>__g__<group>` (same fixed configuration) at the current end of the log. Repeating it keeps
the cursor. The agent's grants came with `channel create`, and the group is not checked against
the catalogue, so create the channel first. Then start the adapter with `--groups <group>`.

```sh
bin/busctl --direct channel join api-client@alice lib-x
```

## Not built yet

`busctl alerts` (alerts go to `<agent-dir>/alerts.log` only) and every command that needs `busd`.
