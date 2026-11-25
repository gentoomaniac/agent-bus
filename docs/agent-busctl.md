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
8. `agent-busctl --direct service init`: busd's and the monitor's logins, only for a setup from before busd.
9. `agent-busctl enroll init`: turns on the enrollment of new machines (optional).

Every `--direct` command holds a lock on the config directory (`nats/mutations.lock`) while it runs,
and so does each approval of busd, so they wait for one another; a command that cannot get the lock
within `--timeout` fails with "another process holds ...".

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

`--direct` is needed by every command that requires admin rights, except `ca`, `enroll init`, `enroll autosign` and the commands that only write files (`tls init`, `tls broker`, `admin init`, `service init`, `monitor ...`). The `ca` commands talk to busd, which the broker container starts by itself. `agent-busctl`
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
| `--san`         | DNS name or IP the certificate is valid for, repeatable, added to `localhost`, `127.0.0.1` and `::1`. Without it: those and this machine's hostname |
| `--validity`    | validity of the broker certificate, default `17520h`                       |
| `--ca-key`, `--broker-cert`, `--broker-key` | file locations, defaults `ca.key`, `broker.pem`, `broker.key` in the config dir |
| `--force`       | overwrite existing files; clients must get the new CA certificate          |

```sh
bin/agent-busctl tls init --san bus.example.org
```

The container's hostname is not the host's: in a container, name every host name and IP that
clients use with `--san`.

### tls broker

Reissues only the broker certificate, for other names or IPs, signed by the existing CA. Clients
keep their `ca.pem` and their certificates; the broker uses the new one from its next start. No
`--direct`, no broker needed.

| Flag            | Meaning                                                                    |
|-----------------|----------------------------------------------------------------------------|
| `--san`         | DNS name or IP the certificate is valid for, repeatable and required, added to `localhost`, `127.0.0.1` and `::1`. The list replaces the old one, so name all the others |
| `--validity`    | validity of the broker certificate, default `17520h`                       |
| `--ca-key`, `--broker-cert`, `--broker-key` | file locations, as for `tls init`                  |

```sh
podman exec agent-bus agent-busctl tls broker --san localhost --san 127.0.0.1 --san 10.1.1.107
~/srv/agent-bus/start.sh && ~/srv/agent-bus/monitor.sh   # or: podman restart agent-bus
```

### admin init

Creates the passwords of the admin users (`bus-admin/password`, `control-admin/password`, mode
0600), of busd and of the monitor (`busd/password`, `monitor-bus/password`,
`monitor-control/password`, each with only the rights it needs) and the server's authorization
files `nats/auth.conf` and `nats/control-auth.conf`. No broker needed.

| Flag      | Meaning                                                                          |
|-----------|----------------------------------------------------------------------------------|
| `--force` | replace existing passwords, also to finish an `admin init` that failed half way; registered agents stay (grants and certificate mappings untouched), a running server must be restarted. Both admin users are password users again, so their certificates are renamed to `client.pem.retired` and `client.key.retired`; restart `bus-monitor` too only if it still uses the admin logins |

```sh
bin/agent-busctl admin init
```

### service init

Gives busd and the monitor their own logins on a broker whose `admin init` ran before they existed:
it creates the missing `busd`, `monitor-bus` and `monitor-control` passwords and adds the entries to
the authorization files. The broker reads them when it restarts. `--force` replaces the passwords
that exist. No broker needed.

```sh
bin/agent-busctl service init
```

### init

Creates stream `BUS` (every conversation channel, a 24-hour dedupe window, and the retention below) in
account `BUS`, stream `PRESENCE` (the last presence record of each agent, see
[monitor.md](monitor.md#presence)), and stream `LEDGER` and the key-value buckets `registry` and
`catalogue` in `CONTROL`. It also regenerates every agent's grants, so after upgrading from a
release without presence, running `init` once is all it takes. Safe to repeat.

```sh
bin/agent-busctl --direct init
```

`BUS` keeps messages for 90 days and at most 1 GiB; whichever limit is hit first deletes the oldest
messages, of all channels together. Change the limits with `--max-age` (`BUS_MAX_AGE`, a duration
such as `2160h`; `0` keeps messages forever, anything else is at least the 24-hour dedupe window)
and `--max-bytes` (`BUS_MAX_BYTES`; `-1` sets no limit). `init` applies them to an existing stream
as well, and it applies the defaults when run without the flags, so give the flags on every run if
you changed them. A log that is over a new limit loses its oldest messages at once.

Consequences: an agent that stays offline longer than the log keeps loses the messages it did not
read; its cursor continues at the oldest one left. A reply can outlive the message it answers, which
the monitor shows as an orphan. The monitor's history and `history` start at the oldest message
left. The registry, the catalogue, `PRESENCE` and `LEDGER` are not bounded.

### register

Registers an agent identity.

```sh
bin/agent-busctl --direct register api-client@alice
```

It

- generates an Ed25519 key pair and a password in `<config-dir>/<agent-id>/` (`key`, `password`;
  directory 0700, files 0600),
- creates the agent's read cursors `<token>__dm` and `<token>__general` (durable pull consumers
  with the fixed configuration of [design/jetstream-binding.md](design/jetstream-binding.md)) at
  the current end of the log: a new agent does not see what was posted before it registered,
  `general`'s history included,
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
list. On another machine the snapshot arrives with the next fetch of a running agent from busd, or
with the next copy of `directory/` for an agent of an older version.

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
agent-busctl --direct directory export /config/directory   # inside the container (the alias); the files land in the config folder
```

To sync it to another machine, export it into the container's own `/config/directory` and stream
that to the machine's directory ([docker.md](docker.md#keep-the-directory-fresh)):

```sh
bin/agent-busctl --direct directory export /config/directory
docker compose -f deploy/docker-compose.yaml exec -T agent-bus tar -C /config -cf - directory \
  | ssh other 'tar -C ~/.config/agent-bus -xf -'
```

A running `agent-bus mcp` fetches the directory from busd at start, every minute
(`--directory-refresh`), on `SIGHUP` and, at most every five seconds, when it meets an agent that is
not in it (a recipient to send to, or the sender of a message); `send` does the same on a miss. It
merges it into its local `directory/` (it adds and replaces records and never deletes one), and
re-reads it. It then keeps the fingerprints of all revoked keys in `<agent-dir>/revoked_keys` and
rejects messages signed by them, even from a pinned key ("revoked key" in `alerts.log`). Sending to
a removed agent fails with "agent removed", and `list_agents` leaves it out. The fetch uses the
shared enrollment login, whose password `agent-bus enroll` keeps in `<config-dir>/enroll/password`;
an agent enrolled by an older version has none, and `agent-bus mcp` says so once: write the password
there (mode 0600) and restart it. Without the file, or when busd cannot be reached, the local
directory stays as it is, and running the export and copying it over is still how such an agent is
kept current. The version of busd must be the new one too
([design/access-control.md](design/access-control.md)).

### cert issue

Client certificates replace passwords as an agent's or admin's login ([design/access-control.md](design/access-control.md), phase A).
They are used on the broker's second port, `4223` ([broker.md](broker.md#settings-and-ports)), and
are signed by the bus CA (`ca.key` in the config directory, which never leaves it).

`cert issue <agent-id>` creates a key and a certificate for a registered agent and writes
`client.pem` and `client.key` (0644, 0600) into `<config-dir>/<agent-id>/`. The certificate is
built from the agent ID alone: client authentication only, not a CA, valid for one year
(`--validity`), with exactly one URI SAN, `agentbus:agent/<agent-token>/<certID>` (admin users:
`agentbus:admin/bus-admin/<certID>` and `agentbus:admin/control-admin/<certID>`), where the certID is
random and new for every certificate. nats-server maps that URI to the user of the same name, and
the authorization file names one certID per agent. The certificate does not log in until `cert
switch` (or `--switch`) tells the broker to accept it. Run it again to renew: an agent that logs in
with a certificate already is switched to the new one at once. Running clients (`agent-bus`, the
monitor, busd) read the pair again at every TLS handshake: the broker drops their old login once,
and they reconnect with the new certificate by themselves, without a restart. **The old certificate stops working**, as does one replaced by
`cert sign --switch`, an approval with `--replace` or `agent remove`; switching back to the password
takes the current one away too. (A certificate made before certIDs, with the URI SAN
`agentbus:agent/<agent-token>`, works until the next issue.) A removed ID gets none. The pair is
written atomically (both files, or the old pair stays); a directory with only one of the two files is
an error, not a silent fall back to the password.

| Flag         | Meaning                                                                       |
|--------------|-------------------------------------------------------------------------------|
| `--switch`   | also switch the agent to certificate login (below)                            |
| `--admin`    | instead of an agent: issue for `bus-admin` and `control-admin` and switch both at once. The tools in this config directory log in with the new files from then on; restart `bus-monitor` only if it still uses these users (an old config directory without `monitor init`; it warns) |
| `--validity` | how long the certificate is valid, default `8760h`; must be positive and is cut off at the CA's expiry, and a CA that is not valid now issues nothing |

`cert issue --admin` never locks the admin tools out. For each admin (`bus-admin` first) it writes
the pair as `client.pem.new` and `client.key.new`, switches the server entry and waits for the
confirmed reload, and only then renames the pair to `client.pem` and `client.key`. If a step fails,
the error names the admin and the tools keep the login that worked until then (the password). Run
the same command again to resume; the next `cert issue --admin` or `cert switch --admin
--password` first activates a staged pair of an admin whom `nats/auth.conf` already maps to that
certificate. If the server did not confirm a reload, restart it (or run the command again) so it
reads the file. A staged pair is only activated, and a client only uses a certificate, after it is
checked: the files form a pair, the certificate is for that identity and it is valid now. A broken
pair is an error naming the files, never a silent fall back to the password.

**Escape hatch.** If an admin migration or rollback fails midway and retrying does not help (for
example the reload is never confirmed), the always-working way out is the offline `agent-busctl
admin init --force`, then a restart of the broker. It resets both admin users to new passwords,
retires the admin certificates and keeps every agent's grants. Automatic recovery of interrupted
migrations is later hardening.

The `agent-bus` of this machine, `agent-busctl`, and `bus-monitor` for the agent it sends as, use `client.pem` and
`client.key` as soon as the identity's directory has them, and the password otherwise. So an agent
running on this machine must be switched right after the issue (`--switch`); for an agent on
another machine, copy both files into its folder on that machine, then switch.

### cert sign

`cert sign <agent-id> --csr <file> [--out <file>] [--switch]` signs a certificate request made on the agent's
machine (`openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout
client.key -out client.csr -subj /CN=x`), so the key never leaves it. The request's signature is
checked and only its public key is used: names, usages and CA rights in it are ignored. The
certificate goes to standard output or `--out`; put it into the agent's folder as `client.pem`,
next to `client.key`. The broker accepts it once `--switch` was given, the agent logged in with a
certificate already (it is then switched to this one, and the old one stops working), or `cert switch
<agent-id> --cert <that file>` ran. `--validity` (default `8760h`) works as for `cert issue`.

### cert switch

`cert switch <agent-id>` moves an agent from its password to certificate login in `auth.conf`
and returns once the server has confirmed the reload; the agent's connections close and it logs in
with the certificate. Its ID, signing key, consumers and registry record stay. Its old password no
longer works. It switches to the certificate in the agent's folder here, which `cert issue` made, or to the one in `--cert <file>` (made with `cert sign`); it refuses when there is none, or the certificate is for another agent. Every other certificate of the agent stops working. Copy the files before the agent restarts.

`cert switch <agent-id> --password` is the rollback: the agent gets its password back (the one in
its folder, or a new one if the file is gone) and the certificate files in this config directory
are deleted. Delete `client.pem` and `client.key` in the agent's folder on every other machine too,
or `agent-bus` there keeps trying the certificate. The certificate itself stays valid until it
expires, but nothing maps it any more, and it is refused at login. `cert switch --admin --password` does the same for the two
admin users.

`agent remove` deletes the agent's certificate mapping together with its account, and the ID is
never registered or certified again, so a certificate issued before the removal cannot log in
as, or under the name of, a later agent.

### enroll init

Turns on the enrollment of new machines ([podman.md](podman.md#9-enrolling-a-new-machine)). It makes
the shared enrollment login (user `enroll`, in `nats/control-auth.conf`, a confirmed reload), stores
its password in `enroll/password` of the config directory (0600) and prints it once, with the CA
fingerprint, for copying to each new machine. The login may only publish `busd.enroll.request`,
`busd.enroll.status` and `busd.directory` and subscribe `_INBOX.enroll.>`. `--force` makes a new
password. It runs in the broker's container, like `cert switch`, and needs the broker running; busd
needs no restart.

### enroll autosign

Prints a new secret and turns autosigning on: a request that carries it (`agent-bus enroll --secret`)
is approved without you, never for an ID that is registered with another key, and busd logs each
one. `--off` turns it off. The secret is `enroll/autosign` (0600), read by busd on every request.

### ca list

Lists the pending enrollment requests (code, agent, hostname, age, ID status `new`, `same-key` or
`replaces`, and the fingerprints of the signing key and the TLS key). Like the other `ca` commands
it asks busd over the bus as `control-admin` (its certificate if it has one): no `--direct`, and it
fails with "busd is not running" when busd is down.

### ca sign

`ca sign <code|request-id|agent-id> [--replace]` approves a request: busd builds the certificate from
the agent ID, registers the agent or switches its account to certificate login, creates its read
cursors and confirms the reload. An agent ID is accepted only when one request names it. An ID that
is registered with another key needs `--replace`, which revokes the old key. An approval is
single-use; a failed one leaves the request pending and can be repeated.

### ca reject

`ca reject <code|request-id|agent-id> [--reason <text>]` rejects a pending request; the machine
prints the reason.

### ca fingerprint

Prints the CA certificate's SHA-256 fingerprint (colon-separated hex) from `ca.pem`, without
the broker. A new machine passes it as `agent-bus enroll --ca-fingerprint`.

### ca review

A terminal screen for the same, updated every two seconds: the list of pending requests, a detail
pane, and the keys up/down, `a` approve, `r` reject (reason prompt), `/` filter, `q` quit. An approval of an
ID that would replace another key asks for confirmation first. It needs a terminal: `podman exec -it
agent-bus agent-busctl ca review`.

### monitor init

`monitor init <dir> [--agent <agent-id>]` creates or refreshes the monitor's own config directory (a
separate folder, never the broker's one). It copies `ca.pem` (not `ca.key`), the passwords
of `monitor-bus` and `monitor-control` (not the admins' or busd's), the web password hash
(`monitor-web/password.bcrypt`, or the old `monitor/password.bcrypt`), the `directory/` snapshot and,
with `--agent` (env `BUS_AGENT`), that registered agent's `key`, `password`, `client.pem` and
`client.key`. It needs `service init` or `admin init` to have run, and no broker. Run it again after
agents are registered or enrolled, or after a password change, then restart `bus-monitor`.

```sh
agent-busctl monitor init /monitor-config --agent monitor@alice
```

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

`agent-busctl alerts` (alerts go to `<agent-dir>/alerts.log` only) and every command that needs the full `busd` (channels, history, registry reads over the bus). What exists of busd is enrollment and approval ([design/busd.md](design/busd.md)).
