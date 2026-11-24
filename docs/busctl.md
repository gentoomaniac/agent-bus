# busctl

`busctl` is the admin CLI of the bus. `<agent-id>` below is `<name>@<owner>`, lowercase
`[a-z0-9-]`, for example `api-client@alice`; the agent token of `lib-x@bob` is `lib-x_bob`.
`<name>` of a channel is lowercase `[a-z0-9-]`.

## Setup order

`busctl` runs in the broker's container ([docker.md](docker.md)). The commands below are written
as plain `busctl`; with the alias from [docker.md](docker.md#set-it-up) (or its long form
`docker compose -f deploy/docker-compose.yaml exec agent-bus busctl ...`) they run as shown, and the
`bin/busctl` of the examples means that alias. `tls init` and `admin init` run before the broker
exists, as one-off containers (`docker compose ... run --rm --no-deps --entrypoint busctl agent-bus ...`).

Run these once, in this order:

1. `busctl tls init`: certificates ([broker.md](broker.md#tls)).
2. `busctl admin init`: admin credentials and authorization files.
3. `docker compose -f deploy/docker-compose.yaml up -d`: the broker ([docker.md](docker.md)).
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
account `BUS`, stream `PRESENCE` (the last presence record of each agent, see
[monitor.md](monitor.md#presence)), and stream `LEDGER` and the key-value buckets `registry` and
`catalogue` in `CONTROL`. It also regenerates every agent's grants, so after upgrading from a
release without presence, running `init` once is all it takes. Safe to repeat.

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
is an error, and so is the ID of a removed agent (see [agent remove](#agent-remove)).
`human@<owner>` is not supported yet.

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
the catalogue, so create the channel first. `channel create` and `channel join` also rewrite the
catalogue snapshot `<config-dir>/directory/channels` (each group with the agents that read it), and
`directory export` writes it too. The agent's adapter reads its groups from the snapshot when it
starts, so restart it after a join; `--groups` is only needed for a group the snapshot does not
list. On another machine the snapshot arrives with the next copy of `directory/`.

```sh
bin/busctl --direct channel join api-client@alice lib-x
```

### agent list

Lists the registered agents with their status and key fingerprint: `active`, or `removed`
(a tombstone, see below).

```sh
bin/busctl --direct agent list
```

### agent remove

Removes an agent. It does three things, each safe to repeat:

- marks the agent's current key `revoked` (with `revoked_at`) and the record `removed_at` in the
  `registry`. The record and the key history stay as a tombstone and are never deleted, so the ID
  is never registered again and receivers learn that the key is revoked,
- deletes the agent's account from `auth.conf` and waits for the server to confirm the reload,
  which closes the agent's live connection,
- deletes its read cursors (DM, `general` and every group channel).

It reports the removal complete only when all three succeeded. After a partial failure it names
the steps that are left; run the same command again. Messages the agent sent stay in the log.

```sh
bin/busctl --direct agent remove api-client@alice
```

Other agents keep trusting the removed key until they refresh their directory
([directory export](#directory-export)).

### directory export

Writes the registry's record of every agent, removed ones included, to a directory as
`<token>.json`. It is the source of the agents' local `directory/`.

```sh
busctl --direct directory export /config/directory   # inside the container (the alias); the files land on the config volume
```

To sync it to another machine, export it into the container's own `/config/directory` and stream
that to the machine's directory ([docker.md](docker.md#keep-the-directory-fresh)):

```sh
bin/busctl --direct directory export /config/directory
docker compose -f deploy/docker-compose.yaml exec -T agent-bus tar -C /config -cf - directory \
  | ssh other 'tar -C ~/.config/agent-bus -xf -'
```

A running `agent-bus mcp` re-reads its directory every minute (`--directory-refresh`) and on
`SIGHUP`. It then keeps the fingerprints of all revoked keys in `<agent-dir>/revoked_keys` and
rejects messages signed by them, even from a pinned key ("revoked key" in `alerts.log`). Sending to
a removed agent fails with "agent removed", and `list_agents` leaves it out. Run the export again
after every `agent remove` and `register`. An automatic refresh over the bus comes with `busd`
and enrollment (design.md §4c).

## Not built yet

`busctl alerts` (alerts go to `<agent-dir>/alerts.log` only) and every command that needs `busd`.
