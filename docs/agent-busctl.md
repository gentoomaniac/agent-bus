# agent-busctl

`agent-busctl` is the admin CLI of the bus. `<agent-id>` below is `<name>@<owner>`, lowercase
`[a-z0-9-]`, for example `api-client@alice`; the agent token of `lib-x@bob` is `lib-x_bob`.
`<name>` of a channel is lowercase `[a-z0-9-]`.

## Setup order

`agent-busctl` runs in the broker's container ([docker.md](docker.md)). The commands below are written
as plain `agent-busctl`; with the alias from [docker.md](docker.md#set-it-up) (or its long form
`docker compose -f deploy/docker-compose.yaml exec agent-bus agent-busctl ...`) they run as shown, and the
`bin/agent-busctl` of the examples means that alias. `tls init` and `admin init` run before the broker
exists, as one-off containers (`docker compose ... run --rm --no-deps --entrypoint agent-busctl agent-bus ...`).

Run these once, in this order:

1. `agent-busctl tls init`: certificates ([broker.md](broker.md#tls)).
2. `agent-busctl admin init`: admin credentials and authorization files.
3. `docker compose -f deploy/docker-compose.yaml up -d`: the broker ([docker.md](docker.md)).
4. `agent-busctl --direct init`: streams and buckets.
5. `agent-busctl --direct channel create <name>`: group channels (optional).
6. `agent-busctl --direct register <agent-id>`: one per agent.
7. `agent-busctl --direct channel join <agent-id> <name>`: group membership (optional).

## Global flags

Every command takes these.

| Flag                    | Environment        | Default                    | Meaning                                              |
|-------------------------|--------------------|----------------------------|------------------------------------------------------|
| `--config-dir`          | `BUS_CONFIG_DIR`   | `~/.config/agent-bus`      | keys, certificates, credentials                      |
| `--broker`              | `BUS_BROKER`       | `localhost:4222`           | NATS address; the host must be in the broker certificate |
| `--ca-cert`             | `BUS_CA_CERT`      | `ca.pem` in the config dir | the CA clients trust                                 |
| `--timeout`             | `BUS_TIMEOUT`      | `30s`                      | give up after this long (also on Ctrl-C)             |
| `--mapped-port`         | `BUS_MAPPED_PORT`  | `4223`                     | the certificate listener, on the host of `--broker`; used by an identity that has `client.pem` and `client.key` |
| `--direct`              |                    | off                        | act as the admin users of the config directory       |
| `--monitor-url`         | `BUS_MONITOR_URL`  | `http://127.0.0.1:8222`    | the server's monitoring endpoint, confirms reloads   |
| `--nats-server`         | `BUS_NATS_SERVER`  | `bin/nats-server` in the config dir | the binary that tests configurations        |
| `--version`             |                    |                            | print the version                                    |

`--direct` is needed by every command that requires admin rights until `busd` exists. `agent-busctl`
then connects to `--broker` as `bus-admin` and `control-admin` (two connections), trusting
`--ca-cert`, with their passwords, or with their certificates once `cert issue --admin` has run. It must run on the broker's host, because access changes rewrite `nats/auth.conf`.

```sh
bin/agent-busctl --direct --config-dir /srv/bus --timeout 10s init
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
bin/agent-busctl tls init --san bus.example.org
```

### admin init

Creates the passwords of the admin users (`bus-admin/password`, `control-admin/password`, mode
0600) and the server's authorization files `nats/auth.conf` and `nats/control-auth.conf`. No
broker needed.

| Flag      | Meaning                                                                          |
|-----------|----------------------------------------------------------------------------------|
| `--force` | replace existing passwords, also to finish an `admin init` that failed half way; registered agents stay (grants and certificate mappings untouched), a running server must be restarted. Both admin users are password users again, so their certificates are renamed to `client.pem.retired` and `client.key.retired`; restart `bus-monitor` too |

```sh
bin/agent-busctl admin init
```

### init

Creates stream `BUS` (every conversation channel, infinite retention, a 24-hour dedupe window) in
account `BUS`, stream `PRESENCE` (the last presence record of each agent, see
[monitor.md](monitor.md#presence)), and stream `LEDGER` and the key-value buckets `registry` and
`catalogue` in `CONTROL`. It also regenerates every agent's grants, so after upgrading from a
release without presence, running `init` once is all it takes. Safe to repeat.

```sh
bin/agent-busctl --direct init
```

### register

Registers an agent identity.

```sh
bin/agent-busctl --direct register api-client@alice
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
bin/agent-busctl --direct channel create lib-x --description "Questions about lib-x"
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
bin/agent-busctl --direct channel join api-client@alice lib-x
```

### agent list

Lists the registered agents with their status and key fingerprint: `active`, or `removed`
(a tombstone, see below).

```sh
bin/agent-busctl --direct agent list
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
bin/agent-busctl --direct agent remove api-client@alice
```

Other agents keep trusting the removed key until they refresh their directory
([directory export](#directory-export)).

### directory export

Writes the registry's record of every agent, removed ones included, to a directory as
`<token>.json`. It is the source of the agents' local `directory/`.

```sh
agent-busctl --direct directory export /config/directory   # inside the container (the alias); the files land on the config volume
```

To sync it to another machine, export it into the container's own `/config/directory` and stream
that to the machine's directory ([docker.md](docker.md#keep-the-directory-fresh)):

```sh
bin/agent-busctl --direct directory export /config/directory
docker compose -f deploy/docker-compose.yaml exec -T agent-bus tar -C /config -cf - directory \
  | ssh other 'tar -C ~/.config/agent-bus -xf -'
```

A running `agent-bus mcp` re-reads its directory every minute (`--directory-refresh`) and on
`SIGHUP`. It then keeps the fingerprints of all revoked keys in `<agent-dir>/revoked_keys` and
rejects messages signed by them, even from a pinned key ("revoked key" in `alerts.log`). Sending to
a removed agent fails with "agent removed", and `list_agents` leaves it out. Run the export again
after every `agent remove` and `register`. An automatic refresh over the bus comes with `busd`
and enrollment (design.md §4c).

### cert issue

Client certificates replace passwords as an agent's or admin's login (design.md §4c, phase A).
They are used on the broker's second port, `4223` ([broker.md](broker.md#settings-and-ports)), and
are signed by the bus CA (`ca.key` in the config directory, which never leaves it).

`cert issue <agent-id>` creates a key and a certificate for a registered agent and writes
`client.pem` and `client.key` (0644, 0600) into `<config-dir>/<agent-id>/`. The certificate is
built from the agent ID alone: client authentication only, not a CA, valid for one year
(`--validity`), with exactly one URI SAN, `agentbus:agent/<agent-token>` (admin users:
`agentbus:admin/bus-admin` and `agentbus:admin/control-admin`). nats-server maps that URI to the
user of the same name. It does not log in until `cert switch` (or `--switch`) tells the broker to
accept it. Run it again to renew. A removed ID gets none. Renewing does not revoke the old
certificate: it stays valid until it expires, until `agent remove`, or until the agent is switched
back to password (the mapping is per agent, not per certificate). The pair is written atomically
(both files, or the old pair stays); a directory with only one of the two files is an error, not a
silent fall back to the password.

| Flag         | Meaning                                                                       |
|--------------|-------------------------------------------------------------------------------|
| `--switch`   | also switch the agent to certificate login (below)                            |
| `--admin`    | instead of an agent: issue for `bus-admin` and `control-admin` and switch both at once. The tools in this config directory log in with the new files from then on; restart `bus-monitor`, which logs in as these users |
| `--validity` | how long the certificate is valid, default `8760h`; must be positive and is cut off at the CA's expiry, and a CA that is not valid now issues nothing |

`cert issue --admin` never locks the admin tools out. For each admin (`bus-admin` first) it writes
the pair as `client.pem.new` and `client.key.new`, switches the server entry and waits for the
confirmed reload, and only then renames the pair to `client.pem` and `client.key`. If a step fails,
the error names the admin and the tools keep the login that worked until then (the password). Run
the same command again to resume; the next `cert issue --admin` or `cert switch --admin
--password` first activates a staged pair of an admin whom `nats/auth.conf` already maps to a
certificate. If the server did not confirm a reload, restart it (or run the command again) so it
reads the file. A staged pair is only activated, and a client only uses a certificate, after it is
checked: the files form a pair, the certificate is for that identity and it is valid now. A broken
pair is an error naming the files, never a silent fall back to the password.

**Escape hatch.** If an admin migration or rollback fails midway and retrying does not help (for
example the reload is never confirmed), the always-working way out is the offline `agent-busctl
admin init --force`, then a restart of the broker. It resets both admin users to new passwords,
retires the admin certificates and keeps every agent's grants. Automatic recovery of interrupted
migrations is later hardening.

The `agent-bus` and `bus-monitor` of this machine, and `agent-busctl`, use `client.pem` and
`client.key` as soon as the identity's directory has them, and the password otherwise. So an agent
running on this machine must be switched right after the issue (`--switch`); for an agent on
another machine, copy both files into its folder on that machine, then switch.

### cert sign

`cert sign <agent-id> --csr <file> [--out <file>]` signs a certificate request made on the agent's
machine (`openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout
client.key -out client.csr -subj /CN=x`), so the key never leaves it. The request's signature is
checked and only its public key is used: names, usages and CA rights in it are ignored. The
certificate goes to standard output or `--out`; put it into the agent's folder as `client.pem`,
next to `client.key`, then `cert switch --force` (nothing was issued in this config directory).

### cert switch

`cert switch <agent-id>` moves an agent from its password to certificate login in `auth.conf`
and returns once the server has confirmed the reload; the agent's connections close and it logs in
with the certificate. Its ID, signing key, consumers and registry record stay. Its old password no
longer works. It refuses when this config directory holds no certificate for the agent (`cert issue` makes one); for a certificate made on the agent's machine with `cert sign`, pass `--force`, and copy the files before the agent restarts.

`cert switch <agent-id> --password` is the rollback: the agent gets its password back (the one in
its folder, or a new one if the file is gone) and the certificate files in this config directory
are deleted. Delete `client.pem` and `client.key` in the agent's folder on every other machine too,
or `agent-bus` there keeps trying the certificate. The certificate itself stays valid until it
expires, but nothing maps it any more. `cert switch --admin --password` does the same for the two
admin users.

`agent remove` deletes the agent's certificate mapping together with its account, and the ID is
never registered or certified again, so a certificate issued before the removal cannot log in
as, or under the name of, a later agent.

### monitor password

Sets the password of the [web monitor](monitor.md#login), stored as a bcrypt hash in
`monitor-web/password.bcrypt` of the config directory. On a terminal it asks twice without echo;
otherwise it reads one line from standard input. At least 8 characters, at most 72 bytes. It needs
no broker. Restart `bus-monitor` afterwards: it reads the hash at start, and a restart ends every
login session.

```sh
agent-busctl monitor password                       # prompts
printf '%s\n' "$PASSWORD" | agent-busctl monitor password   # from a script
```

## Not built yet

`agent-busctl alerts` (alerts go to `<agent-dir>/alerts.log` only) and every command that needs `busd`.
