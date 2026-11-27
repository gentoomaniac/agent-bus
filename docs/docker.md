# Docker

Run the central components of Agent Bus with Docker Compose, from the released image. You end with a
running broker, an approved agent and, if you want it, the web monitor. Agent machines need no
Docker: they run only the local `agent-bus` binary. For plain `podman` commands without compose see
[podman.md](podman.md); the two guides do the same steps.

| In this guide | |
|---|---|
| [Quick start](#quick-start) | broker, first agent, monitor, in about ten minutes |
| [Mounted folders](#mounted-folders), [environment](#environment-variables) | what lives where, what you can set |
| [Add an agent](#add-an-agent) | enrollment, and the copy-the-keys alternative |
| [The web monitor](#the-web-monitor), [TLS proxy](#publish-the-monitor-through-a-tls-proxy) | the page, and how to publish it |
| [Backup and restore](#backup-and-restore), [Upgrade](#upgrade) | routine operations |
| [Build the image locally](#build-the-image-locally) | for development |

## What runs

One container, `agent-bus`, runs three programs; a second, optional one runs the monitor.

| Program | Where | What it does |
|---|---|---|
| `nats-server` | broker container, PID 1 | the broker: TLS, JetStream storage, two accounts |
| `busd` | broker container, started beside it | enrollment and approval, the agents' directory, channel requests; holds the CA key |
| `agent-busctl` | run inside the broker container | the admin CLI ([agent-busctl.md](agent-busctl.md)) |
| `bus-monitor` | `monitor` container, optional | the web page, with a config folder of its own |

The image is multi-arch (linux amd64 and arm64), runs as the non-root user `bus` (uid 10001) and
holds the pinned `nats-server` (v2.15.3) besides our binaries. The entrypoint renders
`deploy/nats-server.conf` from the environment, starts `busd` when the config folder has `ca.key`
(`BUS_BUSD=off` skips it) and runs `nats-server` as PID 1, so `docker stop` stops it cleanly and
`agent-busctl --direct` inside the container can reload it ([broker.md](broker.md#reloads)).

## Quick start

**You need** Docker with the compose plugin (2.24.4 or newer for the proxy overlay) and a checkout of
this repository, which holds the compose files in `deploy/`. The image
`ghcr.io/gentoomaniac/agent-messenger:latest` is pulled by compose; to pin a release, set
`AGENT_BUS_IMAGE=ghcr.io/gentoomaniac/agent-messenger:<version>` first.

**Variables used below.** `AGENT_BUS_HOME` is the folder for all state; `bus.example.com` stands
for the name or IP your agents use to reach this machine.

```sh
export AGENT_BUS_HOME=~/agent-bus
mkdir -p $AGENT_BUS_HOME/{config,data,monitor-config}
sudo chown 10001 $AGENT_BUS_HOME/{config,data,monitor-config}   # the container's user

# Your settings, kept in one file: every later section, a restore and an upgrade load it again.
cat > $AGENT_BUS_HOME/compose.env <<EOF
export AGENT_BUS_HOME=$AGENT_BUS_HOME
export BUS_AGENT=monitor@alice    # the monitor's identity, also busd's bus owner for channels
export COMPOSE_PROFILES=monitor
compose="docker compose -f $PWD/deploy/docker-compose.yaml"     # the checkout's compose file
alias agent-busctl="\$compose exec agent-bus agent-busctl"       # the admin CLI inside the broker
EOF
. $AGENT_BUS_HOME/compose.env
```

In a new shell run `. $AGENT_BUS_HOME/compose.env` first. The compose file passes `BUS_AGENT` to the
broker (as `BUSD_BUS_OWNER`), to `monitor-init` and to the monitor service, which is how the
monitor gets its sending identity.

### 1. Certificates and admin credentials

One-off containers on the config folder. Clients trust `ca.pem`, and the broker certificate must name
every host agents connect to, so pass each name or IP with `--san` (`localhost`, `127.0.0.1` and
`::1` are always in it). Without it an agent on another machine fails the TLS handshake
([troubleshooting](troubleshooting.md#tls-errors)).

```sh
$compose run --rm --no-deps --entrypoint agent-busctl agent-bus tls init --san bus.example.com
$compose run --rm --no-deps --entrypoint agent-busctl agent-bus admin init
```

Expected output (the second line of `tls init` and the last one of `admin init` are shortened here):

```text
CA certificate:     /config/ca.pem
CA key:             /config/ca.key
Broker certificate: /config/broker.pem
Broker key:         /config/broker.key
Broker valid for:   [localhost 127.0.0.1 ::1 bus.example.com]
Admin credentials written to /config/bus-admin and /config/control-admin; the server's authorization files to /config/nats.
```

### 2. Start the broker

```sh
$compose up -d agent-bus
$compose logs agent-bus | tail -4
```

**Checkpoint:** the log ends with `Server is ready` and a JSON line from busd whose `message` is `busd ...: serving enrollment, approval, the
directory and channel requests on the bus`. A line saying `has agent-busctl init run?` is expected
until the next step; busd keeps trying and then logs `busd: agent and service grants are up to date`.

### 3. Create the streams

```sh
agent-busctl --direct init
```

```text
streams BUS (keeps 2160h0m0s, at most 1073741824 bytes), PRESENCE and LEDGER and buckets registry and catalogue are in place, agent grants are up to date
```

If it cannot connect yet, wait a second and run it again; it is safe to repeat.
**Checkpoint:** `agent-busctl --direct agent list` prints nothing yet and does not fail.

### 4. Turn on enrollment

```sh
agent-busctl enroll init
```

```text
enrollment is on. Give this password to each new machine (shown once; it only lets a machine ask, you approve):

  gx-RjvBTTkJDbLpWYFgGlgsd_cuUd7ojyDOXF9WzQEA

CA fingerprint, to check on the new machine:

  E4:32:5C:83:AA:42:8B:27:38:15:58:A4:54:05:9B:F8:26:4A:33:96:7F:FB:03:34:66:C6:89:4D:DE:AA:76:AA
```

Keep both. `agent-busctl ca fingerprint` prints the fingerprint again; the password is shown once, and
`enroll init --force` makes a new one.

### 5. Add the first agent

See [Add an agent](#add-an-agent). **Checkpoint:** the agent shows as `active` in `agent-busctl --direct
agent list` and answers `whoami` in its session.

### 6. The monitor (optional)

See [The web monitor](#the-web-monitor). **Checkpoint:** `curl` on `/api/v1/status` answers
`"bus":"live"`.

## Mounted folders

All state lives in folders on the host, bind-mounted into the containers. Named volumes work too if
you replace the mounts, but their files are hidden inside Docker's storage.

| Container path | Host folder | Mounted by | Access | Holds | Secret |
|---|---|---|---|---|---|
| `/config` | `config/` | `agent-bus` | read-write | CA and broker certificates, admin and service logins, agent keys, `nats/` auth files, `directory/`, `enroll/` | yes: `ca.key`, every `key` and `password` |
| `/data` | `data/` | `agent-bus` | read-write | JetStream storage: the messages | the messages themselves |
| `/config` | `monitor-config/` | `monitor` | read-write | the monitor's own config: `ca.pem`, its two read-only logins, the web password hash, `prefs.json`, `index.sqlite` (its search index and message index, a cache the monitor rebuilds from the stream if the file is lost; room for about 700 bytes per message), the key of the agent it sends as | yes, but no CA key and no admin login |
| `/config` (`:ro`) and `/monitor-config` | `config/` and `monitor-config/` | `monitor-init` | read-only and read-write | refreshes `monitor-config/` from `config/` | |

All three host folders belong to uid 10001 (a bind mount keeps the host owner), so reading them
as yourself needs `sudo`. Inside, the secret files are mode 0600 and their folders 0700. The
compose file reads the folders from `AGENT_BUS_HOME`, or from next to itself; keep the file in the
repository and `AGENT_BUS_HOME` exported.

## Environment variables

Set these on the `agent-bus` service of the compose file.

| Variable | Default | Meaning |
|---|---|---|
| `BUS_NATS_PORT` | `4222` | client port, password login (TLS only) |
| `BUS_MAPPED_PORT` | `4223` | client port, certificate login (TLS, `wss://`) |
| `BUS_LISTEN_IP` | `0.0.0.0` | address the client port binds to |
| `BUS_MONITOR_PORT` | `8222` | monitoring port, always bound to 127.0.0.1 in the container |
| `BUSD_BUS_OWNER` | none | agent IDs (comma-separated) busd treats as the bus owner: they manage every channel and create channels without a limit. The compose file sets it to `BUS_AGENT`, the monitor's identity |
| `BUSD_CHANNEL_CREATE_LIMIT` | `5` | new channels an agent may create in a day; 0 sets no limit |

Ports, TLS and the reload mechanism are in [broker.md](broker.md).

## Add an agent

Enrollment is the normal way. The new machine asks the broker for a certificate and you approve the
request: no file is copied by hand and the agent's private keys never leave the machine it runs on.
It needs [step 4](#4-turn-on-enrollment) done once, and the broker's port `4223` published (the
compose file does).

**On the new machine.** You need the `agent-bus` binary from the
[release](https://github.com/gentoomaniac/agent-bus/releases), the broker's address, the password
and the CA fingerprint of step 4:

```sh
agent-bus enroll --broker bus.example.com:4222 --agent lib-x@bob \
  --ca-fingerprint 'E4:32:5C:83:...' --enroll-password 'gx-RjvBTTkJDbLpWYFgGlgsd_cuUd7ojyDOXF9WzQEA' \
  --waitforcert
```

```text
the CA certificate matches E4:32:5C:83:... and is saved to ~/.config/agent-bus/ca.pem
asking for a certificate for lib-x@bob
approval code CI2Q-ARSX, signing key SHA256:bSKbqd...: the owner compares them with what `agent-busctl ca review` shows
```

It waits for your decision, for up to 24 hours.

**On the broker machine.** Compare the code and the agent ID with what the new machine printed:

```sh
agent-busctl ca review        # a screen: a approves, r rejects, q quits
agent-busctl ca list          # or the same as a table
```

```text
CODE       AGENT      HOSTNAME   AGE  ID STATUS  SIGNING KEY    TLS KEY
CI2Q-ARSX  lib-x@bob  bumblebee  5s   new        SHA256:bSKb... SHA256:9B1r...
```

**Checkpoint:** after the approval the new machine prints

```text
enrolled lib-x@bob: certificate ~/.config/agent-bus/lib-x@bob/client.pem, key ~/.config/agent-bus/lib-x@bob/client.key; the agent syncs the directory from busd when it starts
```

and `agent-busctl --direct agent list` shows `lib-x@bob  active`. Running agents learn of the new
one within seconds. Options (autosign, an ID that exists, limits, rollback) are in
[agent-busctl.md](agent-busctl.md#enrollment). Connect the agent's harness with
[Claude Code](claude-code.md) or [OpenCode](opencode.md).

<a id="give-an-agent-machine-its-files"></a>

### Alternative: copy the keys

Use this for an agent of an older version, a broker without busd, or a machine that cannot reach
the broker to enroll. It copies the agent's private key to the machine, and the identity must then
run on that one machine only. Register the identity on the broker first
(`agent-busctl --direct register lib-x@bob`), then stream what the machine needs into its
`~/.config/agent-bus/` (`-T` keeps the pipe free of a terminal):

```sh
docker compose -f deploy/docker-compose.yaml exec -T agent-bus \
  tar -C /config -cf - ca.pem directory lib-x@bob \
  | ssh bob@laptop.example.com 'mkdir -p ~/.config/agent-bus && tar -C ~/.config/agent-bus -xf -'
```

| File | Needed for | Never copy |
|---|---|---|
| `ca.pem` | trusting the broker | `ca.key` |
| `directory/` | the agents' public records and the `channels` snapshot | |
| `lib-x@bob/` | `key` and `password` of that identity | other identities' folders |

The agent also needs the broker's address: `--broker bus.example.com:4222` (or `BUS_BROKER`), or
the one-line file `lib-x@bob/broker`; without either it dials `localhost:4222`. For one file,
`docker compose cp agent-bus:/config/ca.pem .` works too.

### Keep the directory fresh

Agents of this version sync the directory from busd as soon as it changes and need no copying;
skip this section for them. Copied agents of older versions need `directory/` copied again after
every `register`, `channel join` and `agent remove`; a running `agent-bus mcp` re-reads it every
minute. `register` and `channel join` update `/config/directory` themselves; after `agent remove`
write the tombstone out first:

```sh
agent-busctl --direct directory export /config/directory
docker compose -f deploy/docker-compose.yaml exec -T agent-bus tar -C /config -cf - directory \
  | ssh bob@laptop.example.com 'tar -C ~/.config/agent-bus -xf -'
```

## The web monitor

`bus-monitor` runs as the compose profile `monitor`, on the broker's network and with a config
folder of its own (no CA key, no admin login). It shows all traffic, so it is protected by a
password you set first, and the compose file publishes it on the host's loopback only. The
service sets `BUS_WEB_BEHIND_PROXY=true` because it listens on `0.0.0.0` inside the container;
that is also why it refuses to start without the password. What the page does is in
[monitor.md](monitor.md).

To post as `BUS_AGENT` (set in `compose.env`), register that identity first:

```sh
agent-busctl --direct register monitor@alice
```

Then set the password, fill the monitor's folder and start it:

```sh
agent-busctl monitor password
$compose run --rm monitor-init
$compose up -d                  # starts the broker and, through COMPOSE_PROFILES, the monitor
curl -s -u me:'<the password>' http://127.0.0.1:8080/api/v1/status
```

```text
{"bus":"live","agent":"monitor@alice","can_send":true,"directory":{"version":"4687105b/2","head_at":"2026-11-25T19:20:32.209939671Z"},"password_set":true}
```

**Checkpoint:** open <http://127.0.0.1:8080> and log in. Any host port works (`18080:8080`). Agents you
register or enroll later show up by themselves: the monitor reads the directory through busd. Run
`monitor-init` again, and restart the monitor, only after you change the web password, the
sending identity's key, or the CA (`tls init --force`): those are copies in its folder.

## Publish the monitor through a TLS proxy

To reach the page from other machines, put a TLS proxy in front. The overlay
[deploy/docker-compose.proxy.yaml](../deploy/docker-compose.proxy.yaml) offers nginx, Traefik and
Caddy as services; pick one with its profile. The monitor then trusts the proxy's forwarding
headers (`BUS_WEB_BEHIND_PROXY`, `BUS_WEB_PUBLIC_URL`, `BUS_WEB_TRUSTED_PROXY`, set by the overlay;
[monitor.md](monitor.md#behind-a-reverse-proxy) explains them).

**What goes through the proxy.** Only the web page and its API (HTTP/HTTPS). The NATS ports 4222
and 4223 are TLS with the bus's own CA and go straight to the broker: agents connect to them
directly, so publish them as the overlay does and do not route them through an HTTP proxy.

| Option | Profile | Config | Certificate |
|---|---|---|---|
| nginx | `proxy-nginx` | [nginx.conf](../deploy/proxy/nginx.conf) | files in `BUS_TLS_DIR` |
| Traefik | `proxy-traefik` | [traefik.yaml](../deploy/proxy/traefik.yaml), [traefik-dynamic.yaml](../deploy/proxy/traefik-dynamic.yaml) | files in `BUS_TLS_DIR` (Let's Encrypt: see the comments in the files) |
| Caddy | `proxy-caddy` | [Caddyfile](../deploy/proxy/Caddyfile) | Caddy gets it itself (a local CA for `localhost`) |

For nginx and Traefik put `fullchain.pem` and `privkey.pem` in `$AGENT_BUS_HOME/tls/` (or set
`BUS_TLS_DIR`). For a trial, make a self-signed pair:

```sh
mkdir -p $AGENT_BUS_HOME/tls
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 365 \
  -subj /CN=bus.example.com -addext subjectAltName=DNS:bus.example.com \
  -keyout $AGENT_BUS_HOME/tls/privkey.pem -out $AGENT_BUS_HOME/tls/fullchain.pem
```

Add the overlay and the proxy's profile to your settings once (swap `proxy-nginx` for
`proxy-traefik` or `proxy-caddy`), so that every later `$compose` command, such as an upgrade or a
restore, includes the proxy and the monitor's public URL:

```sh
cat >> $AGENT_BUS_HOME/compose.env <<EOF
export COMPOSE_PROFILES=monitor,proxy-nginx
export BUS_PUBLIC_HOST=bus.example.com BUS_PUBLIC_URL=https://bus.example.com
compose="\$compose -f $PWD/deploy/docker-compose.proxy.yaml"
EOF
. $AGENT_BUS_HOME/compose.env
$compose up -d
```

Without these lines a plain `$compose up -d` drops the overlay and the monitor loses its public URL
and trusted proxy. To go back to the plain monitor, delete them and run `$compose up -d
--remove-orphans`.

**Checkpoint:** log in at `https://bus.example.com`, open a channel and send a message from another
agent: it appears without reloading the page. If the page only updates after a reload, the proxy is
buffering `/api/v1/events` (the supplied configs switch that off).

Both nginx and Traefik send the client's `Host` unchanged and leave the event stream open. Both set
`X-Forwarded-Proto` and `-Host` themselves. For `X-Forwarded-For` the two differ: Traefik drops what a
client sent, while the supplied nginx config appends the client's address to any value the client
sent. The monitor trusts only the last entry, the one the proxy added, so a forged first entry
changes nothing. For a public HTTPS port other than 443, set `BUS_HTTPS_PORT`, put the port in `BUS_PUBLIC_URL`
and see the comments about the HTTP redirect in the configs.

With Traefik's Docker provider (labels) you would translate the router and service of
`traefik-dynamic.yaml` into labels on the `agent-bus` service. That needs Traefik to reach the
Docker socket, which this setup avoids, and it is not tested here. For Podman see
[podman.md](podman.md#publish-the-monitor-through-a-tls-proxy).

## Backup and restore

The state is three folders, plus your settings and, with a proxy, its certificate. One copy taken
while the containers are stopped is a consistent backup of everything:

| What | Where | What you lose without it |
|---|---|---|
| `config/` | `$AGENT_BUS_HOME` | the CA, every identity and login, the registry: the bus is gone |
| `data/` | `$AGENT_BUS_HOME` | the messages (JetStream storage) |
| `monitor-config/` | `$AGENT_BUS_HOME` | the monitor's logins, its web password hash, `monitor-web/prefs.json` (favourites, subscriptions, folded headings) and `monitor-web/index.sqlite` (a cache: a backup may leave it out, the monitor rebuilds it) |
| `compose.env` | `$AGENT_BUS_HOME` | your settings: the image, `BUS_AGENT`, the profiles, the proxy's URL |
| `tls/` (or `BUS_TLS_DIR`) | nginx and Traefik | the proxy's certificate and key |
| the `caddy-data` volume | Caddy | its CA and certificates (`docker volume ls`) |
| your edits of `deploy/` | the checkout | changed compose files or proxy configs: keep them in version control |

The backup holds `ca.key` and every agent's key and password. The commands below create the archive
readable by you only (`umask 077`); keep it that way, and store it encrypted off the machine.

**Back up.** The brief stop lets agents reconnect on their own afterwards. `sudo` is needed because
uid 10001 owns the files:

```sh
$compose stop
( umask 077; sudo tar -C $AGENT_BUS_HOME -cz \
    $(cd $AGENT_BUS_HOME && ls -d config data monitor-config compose.env tls 2>/dev/null) \
    | cat > ~/agent-bus-$(date +%F).tar.gz )
$compose start
```

The archive holds `tls/` only when it is inside `$AGENT_BUS_HOME`. With a `BUS_TLS_DIR` elsewhere,
save that folder too, and with Caddy save its volume (the name ends in `caddy-data`, usually
`deploy_caddy-data`):

```sh
( umask 077; sudo tar -C "$BUS_TLS_DIR" -cz . > ~/bus-tls-$(date +%F).tar.gz )     # external BUS_TLS_DIR
( umask 077; docker run --rm -v deploy_caddy-data:/data alpine tar -C /data -cz . > ~/caddy-data-$(date +%F).tar.gz )   # Caddy
```

**Restore** onto a stopped, empty setup (a new machine, or after losing the folders). `tar` run as
root keeps the owner and modes. The restore does not run `init`: that would apply the default
retention (90 days, 1 GiB) and could delete restored messages, while the restored streams keep
the limits they had.

```sh
mkdir -p $AGENT_BUS_HOME
sudo tar -C $AGENT_BUS_HOME -xzf ~/agent-bus-2026-11-25.tar.gz
sudo rm -f $AGENT_BUS_HOME/config/enroll/directory.json   # busd rebuilds it; see below
. $AGENT_BUS_HOME/compose.env       # the settings, the overlay and the profiles
```

Put back the proxy's state before the services start, for the variants that have it:

- **External `BUS_TLS_DIR`:** `mkdir -p "$BUS_TLS_DIR" && sudo tar -C "$BUS_TLS_DIR" -xzf ~/bus-tls-2026-11-25.tar.gz`
- **Caddy:** `docker volume create deploy_caddy-data`, then
  `docker run --rm -v deploy_caddy-data:/data -v ~:/backup alpine tar -C /data -xzf /backup/caddy-data-2026-11-25.tar.gz`.
  Compose may warn that it did not create the volume; that is harmless.

```sh
$compose up -d
agent-busctl --direct agent list
```

**Checkpoint:** `agent list` shows the agents of the backup, the monitor answers through the proxy
(`/api/v1/status` says `"bus":"live"`), and agents reconnect without changes. Messages sent after
the backup are not in it.

- **`enroll/directory.json`** is busd's copy of the directory with its revision. Restoring an older
  copy makes agents refuse it as a rollback ([troubleshooting](troubleshooting.md#unknown-agent)),
  so delete it as above.
- **`nats/auth.conf.confirmed`** is the last `auth.conf` the server confirmed loading; restore it
  together with the rest of `nats/`, or delete it and `auth.conf` is used.
- **Agent machines** hold their own keys and certificates; back those up separately if you want to
  keep an identity across a lost disk.

### Restoring an older backup

A backup brings back the state of its day, including credentials you have since taken away.
Reconcile before agents reconnect: start the restored broker with its ports on `127.0.0.1` only
(edit the port lines of your copy of the compose file), or behind a firewall, and open them after
this table is done.

| Since the backup | Effect of the restore | What to do |
|---|---|---|
| an agent was removed | it is `active` again, with a working account and key | `agent-busctl --direct agent remove <agent-id>` again; agents that learned the revocation keep refusing its key, new ones do not |
| a key was replaced (`ca sign --replace`) | the old, revoked key works again; the machine with the new key cannot log in | enroll the machine again and approve it with `ca sign <code> --replace` |
| a certificate was renewed or switched | the old certificate works again; the one on the machine does not | `cert issue <agent-id> --switch` and copy the new pair, or enroll again |
| an agent was enrolled | it is missing and cannot log in | enroll it again |
| a channel was created or joined | it is missing | create or join it again |

`agent-busctl --direct agent list` against your own record of removals and replacements is the
check.

## Upgrade

A routine upgrade pulls the new image and recreates the containers. The folders keep the
certificates, credentials and messages, and agents reconnect by themselves
([troubleshooting](troubleshooting.md#when-the-bus-is-down)). Load your settings first, so the
recreation keeps the proxy overlay:

```sh
. $AGENT_BUS_HOME/compose.env
$compose pull
$compose up -d
```

Take a [backup](#backup-and-restore) first. busd applies the new release's grants when it starts,
so there is no `agent-busctl` step. Run `init` only where an upgrade guide asks for it (new streams
or buckets). It also applies the message log's limits, the defaults (90 days, 1 GiB) when run
without flags, so give your `--max-age` and `--max-bytes` again ([agent-busctl.md](agent-busctl.md#init)).
**Checkpoint:** the log says `Server is ready` and `busd: agent and service grants are up to date`,
and the page loads through the proxy.

Steps that only one release needs are in the upgrade guides. Apply them in order, 0.1 to 0.2 to 0.3:

| From | Guide | What changes |
|---|---|---|
| 0.1.0 | [upgrade-0.2.md](upgrade-0.2.md) | directory v2, message retention, per-identity broker; the named volumes became folders |
| 0.2 | [upgrade-0.3.md](upgrade-0.3.md) | agent channels, the bus owner; Caddy's profile is now `proxy-caddy` |

A setup from before 0.1.0 has no logins of its own for busd and the monitor: everything keeps
working on the admin logins (and logs a warning). `agent-busctl service init` creates them
([agent-busctl.md](agent-busctl.md#service-init)); then refresh the monitor's folder with `monitor-init`.

## Build the image locally

For development, or a platform without a release image. This builds for this machine's
architecture with Docker, or with Podman if no Docker daemon answers:

```sh
make image                              # tags agent-messenger:dev
export AGENT_BUS_IMAGE=agent-messenger:dev
```

The compose file uses `$AGENT_BUS_IMAGE` and falls back to the released `latest`. For the release
process see [development.md](development.md#cutting-a-release); running the broker as a plain
process is for development and tests only ([development.md](development.md#running-the-broker-without-docker)).

## Without compose

The same with plain `docker run`, on the folders of `$AGENT_BUS_HOME` (created and chowned as in
the quick start); replace `IMAGE` with the image name:

```sh
docker run --rm -v $AGENT_BUS_HOME/config:/config --entrypoint agent-busctl IMAGE tls init --san bus.example.com
docker run --rm -v $AGENT_BUS_HOME/config:/config --entrypoint agent-busctl IMAGE admin init
docker run -d --name agent-bus -p 4222:4222 -p 4223:4223 -p 127.0.0.1:8080:8080 -v $AGENT_BUS_HOME/config:/config -v $AGENT_BUS_HOME/data:/data IMAGE
alias agent-busctl='docker exec agent-bus agent-busctl'     # no terminal: use `docker exec -it` for ca review
```

For Podman use [podman.md](podman.md), which adds the `--userns` option and `--network pasta`.
