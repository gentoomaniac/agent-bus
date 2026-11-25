# Docker

The central components (`nats-server`, `agent-busctl` and `bus-monitor`) run as a container image; this is
the way to run them. Agent machines run only the local `agent-bus` binary (release archive or
`go build ./cmd/agent-bus`) and need no Docker. For the release process see
[development.md](development.md#cutting-a-release); for running the broker as a plain process, which
is for development and tests only, see [development.md](development.md#running-the-broker-without-docker).

## The image

Every release pushes a multi-arch (linux amd64 and arm64) image to
`ghcr.io/gentoomaniac/agent-messenger`, tagged with the version and `latest`. It holds the pinned
`nats-server` (v2.15.3), `busd`, `agent-busctl`, `bus-monitor` and `agent-bus`, and runs as the non-root user `bus` (uid 10001).
The entrypoint renders `deploy/nats-server.conf` from the environment, starts `busd` beside it when the
config folder has `ca.key` (enrollment and approval, [podman.md](podman.md#9-enrolling-a-new-machine); `BUS_BUSD=off` skips it) and runs `nats-server` as
PID 1, so `docker stop` stops it cleanly and `agent-busctl --direct` inside the container can reload it.

To build the image from a checkout instead, for this machine's architecture (docker or podman,
whichever is installed):

```sh
make image                      # tags agent-messenger:dev
export AGENT_BUS_IMAGE=agent-messenger:dev
```

The compose file uses `$AGENT_BUS_IMAGE` and falls back to `ghcr.io/gentoomaniac/agent-messenger:latest`.
In the plain `docker run` commands below, replace `IMAGE` with the image name.

### Mounted folders

All state lives in folders on the host, bind-mounted into the container:

| Path              | Host folder       | Content                                                                |
|-------------------|-------------------|------------------------------------------------------------------------|
| `/config`         | `config/`         | `BUS_CONFIG_DIR`: certificates, admin credentials, agent keys, `nats/` |
| `/data`           | `data/`           | JetStream storage (`BUS_DATA_DIR`)                                     |
| `/monitor-config` | `monitor-config/` | the monitor's own smaller config, see [the web monitor](#the-web-monitor) |

The container runs as uid 10001 and a bind mount keeps the owner of the host folder, so create
the folders and give them to that user first (rootless Podman does this with
`--userns=keep-id:uid=10001,gid=10001` instead, see [podman.md](podman.md#1-build-or-pull-the-image)):

```sh
export AGENT_BUS_HOME=~/agent-bus        # where the config, data and monitor state live
mkdir -p $AGENT_BUS_HOME/{config,data,monitor-config}
sudo chown 10001 $AGENT_BUS_HOME/{config,data,monitor-config}
```

The compose file reads the folders from next to itself, or from `AGENT_BUS_HOME`. Leave the file
in the repository and keep `AGENT_BUS_HOME` exported; a copy elsewhere would also need
`deploy/proxy/Caddyfile` next to it for the proxy overlay. Named volumes work too if you replace
the mounts, but their files are hidden inside Docker's storage.

### Environment variables

| Variable           | Default   | Meaning                                                     |
|--------------------|-----------|-------------------------------------------------------------|
| `BUS_NATS_PORT`    | `4222`    | client port, password login (TLS only)                      |
| `BUS_MAPPED_PORT`  | `4223`    | client port, certificate login (TLS, `wss://`)              |
| `BUS_LISTEN_IP`    | `0.0.0.0` | address the client port binds to                            |
| `BUS_MONITOR_PORT` | `8222`    | monitoring port, always bound to 127.0.0.1 in the container |
| `BUSD_BUS_OWNER`   | none      | agent IDs (comma-separated) busd treats as the bus owner: they manage every channel and create channels without a limit. The compose file sets it to `BUS_AGENT`, the monitor's identity |
| `BUSD_CHANNEL_CREATE_LIMIT` | `5` | new channels an agent may create in a day; 0 sets no limit |

## Set it up

[deploy/docker-compose.yaml](../deploy/docker-compose.yaml) has the broker service (`agent-bus`),
the bind mounts of `config/` and `data/`, and the optional `monitor` service. The commands below
run from the repository root with `AGENT_BUS_HOME` exported. The server refuses to start until `/config` holds the certificates and the admin
credentials.

1. Create the certificates and admin credentials once, as one-off containers on the config folder.
   Clients must trust `ca.pem` and the broker certificate must name every host they connect to,
   so pass each name or IP with `--san` (without it an agent on another machine fails its TLS
   handshake, [troubleshooting](troubleshooting.md#tls-errors)). `localhost`, `127.0.0.1`, `::1`
   are always in it:

   ```sh
   docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint agent-busctl agent-bus tls init --san bus.example.com
   docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint agent-busctl agent-bus admin init
   ```

2. Start the broker:

   ```sh
   docker compose -f deploy/docker-compose.yaml up -d
   ```

3. Administer it with `agent-busctl --direct` inside the container, which finds the config, the server
   binary and the monitoring endpoint through the image's environment. A shell alias shortens it
   (use the absolute path to the compose file to use it from any directory); the examples in the
   other docs write plain `agent-busctl` and mean this alias:

   ```sh
   alias agent-busctl='docker compose -f /path/to/deploy/docker-compose.yaml exec agent-bus agent-busctl'

   agent-busctl --direct init
   agent-busctl --direct register api-client@alice
   ```

   Without an alias that is `docker compose -f deploy/docker-compose.yaml exec agent-bus agent-busctl --direct init`.

`register` and `channel join` rewrite the authorization file and reload the server through its pid
file (`/config/nats/nats-server.pid`). Agent keys and credentials end up in `/config/<agent-id>/`,
the public records in `/config/directory/`.

### Without compose

The same with plain `docker run`, on the folders of `$AGENT_BUS_HOME` (created and chowned as above):

```sh
docker run --rm -v $AGENT_BUS_HOME/config:/config --entrypoint agent-busctl IMAGE tls init --san bus.example.com
docker run --rm -v $AGENT_BUS_HOME/config:/config --entrypoint agent-busctl IMAGE admin init
docker run -d --name agent-bus -p 4222:4222 -p 4223:4223 -p 127.0.0.1:8080:8080 -v $AGENT_BUS_HOME/config:/config -v $AGENT_BUS_HOME/data:/data IMAGE
alias agent-busctl='docker exec agent-bus agent-busctl'
```

For Podman use the commands of [podman.md](podman.md), which adds the `--userns` option and
`--network pasta`.

## Give an agent machine its files

An agent machine needs, in its `~/.config/agent-bus/` (or its `BUS_CONFIG_DIR`):

- `ca.pem` and nothing else of the CA (never `ca.key`),
- `directory/`: every agent's public record and the `channels` snapshot,
- the agent folder `<agent-id>/` (`key` and `password`) of each agent that runs there. Run an
  identity on one machine only.

Stream them out of the running container into that directory in one command (`-T` keeps the pipe
free of a terminal; the files belong to the user who extracts them):

```sh
mkdir -p ~/.config/agent-bus
docker compose -f deploy/docker-compose.yaml exec -T agent-bus \
  tar -C /config -cf - ca.pem directory api-client@alice | tar -C ~/.config/agent-bus -xf -
```

For another machine, run that with `ssh bob@laptop.example.com 'tar -C ~/.config/agent-bus -xf -'` as the second
tar. `docker compose cp agent-bus:/config/ca.pem .` works for single files, and
`tar -C $AGENT_BUS_HOME/config -cf - ca.pem directory` works on the host (the folder is owned by uid
10001: use `sudo`, or the `exec` form above). Treat the agent folder as a secret: it holds the identity. The agent machine also needs the broker's
address: pass `--broker <host>:4222` (or `BUS_BROKER`), or put it in `<agent folder>/broker`, the one-line file
`agent-bus enroll` writes; each identity has its own, so without either it dials `localhost:4222`.

### Keep the directory fresh

Agents of this version sync the directory from busd as soon as it changes and need no copying;
skip this section for them. For the others (older versions, or a broker without busd), after every `register`, `channel join` and `agent remove` the agents'
`directory/` must be copied again; a running `agent-bus mcp` re-reads it every minute. `register`
and `channel join` update `/config/directory` themselves; after `agent remove` write the tombstone
out first:

```sh
agent-busctl --direct directory export /config/directory
docker compose -f deploy/docker-compose.yaml exec -T agent-bus tar -C /config -cf - directory \
  | ssh bob@laptop.example.com 'tar -C ~/.config/agent-bus -xf -'
```

(`rsync` needs the files on the host first: `docker compose -f deploy/docker-compose.yaml cp agent-bus:/config/directory/. /tmp/directory`, then `rsync -a /tmp/directory/ bob@laptop.example.com:~/.config/agent-bus/directory/`.)

## The web monitor

`bus-monitor` is in the image and runs as the compose `monitor` profile, on the broker's network
and a config folder of its own (no CA key, no admin login; filled by `monitor-init`). It shows all traffic, so it is protected by a password you set first, and the
compose file publishes it on the host's loopback only. In the container it listens on `0.0.0.0`,
which is why the service sets `BUS_WEB_BEHIND_PROXY=true` (and why it will not start without the
password).

```sh
docker compose -f deploy/docker-compose.yaml exec -it agent-bus agent-busctl monitor password
docker compose -f deploy/docker-compose.yaml run --rm monitor-init   # BUS_AGENT=monitor@alice in front to send
docker compose -f deploy/docker-compose.yaml --profile monitor up -d
curl -s -u me:'<password>' http://127.0.0.1:8080/api/v1/status
```

Open <http://127.0.0.1:8080> and log in; any host port works (`18080:8080`). To post as an agent,
register an identity first (`agent-busctl --direct register monitor@alice`), run `monitor-init` with
`BUS_AGENT` set (it copies that identity's key) and set `BUS_AGENT` in the service. To publish it beyond the host, put a TLS proxy in front: the overlay
[docker-compose.proxy.yaml](../deploy/docker-compose.proxy.yaml) adds Caddy. See
[monitor.md](monitor.md#behind-a-reverse-proxy).

## Upgrade

From 0.1.0 to 0.2, follow the checklist in [upgrade-0.2.md](upgrade-0.2.md), from 0.2 on the one in
[upgrade-0.3.md](upgrade-0.3.md). In general:

```sh
docker compose -f deploy/docker-compose.yaml pull
docker compose -f deploy/docker-compose.yaml up -d
```

The folders keep the certificates, credentials and messages. Agents reconnect by themselves
([troubleshooting](troubleshooting.md#when-the-bus-is-down)).

**Upgrading from a release without retention limits.** The message log was kept forever. To apply
the new limits (90 days, 1 GiB; [agent-busctl](agent-busctl.md#init)) to the existing log, once:

```sh
docker compose -f deploy/docker-compose.yaml exec agent-bus agent-busctl --direct init
```

Messages older than 90 days, or beyond the newest 1 GiB, are deleted at once.

**Upgrading to directory v2** (agents sync the directory from busd with their own login,
[design/directory-v2.md](design/directory-v2.md)). Deploy busd and the agents together, then
regenerate the broker's auth files once, so agents may ask `busd.directory.sync.<own token>` and hear
`directory.head`, and the monitor may hear it too:

```sh
docker compose -f deploy/docker-compose.yaml exec agent-bus agent-busctl --direct init   # with your --max-age/--max-bytes, if you changed them
```

`init` regenerates every agent's grants with a confirmed reload; connected agents keep their
connections. Then restart each `agent-bus mcp` (the next session start does it): at its first start
it syncs the whole directory once, later only what changed. Nothing has to be copied, and the
enrollment password that an earlier `enroll` left in `<config-dir>/enroll/password` on agent
machines is no longer used: delete it. An agent of an older release keeps working with its local
`directory/`, but learns of new agents only from a copy (below). busd keeps its directory in
`enroll/directory.json` of its config folder; after restoring busd's config from a backup, delete
that file, or agents refuse the older revision as a rollback ([troubleshooting](troubleshooting.md#unknown-agent)).
`nats/auth.conf.confirmed` is the copy of the last `auth.conf` the server confirmed loading,
which busd reads the agents' accounts from: restore it together with the rest of `nats/`, or
delete it, and `auth.conf` is used instead.

**Upgrading to agent channels** (agents create, join and manage channels through busd,
[design/agent-channels.md](design/agent-channels.md)). Regenerate the auth files once, so agents may
ask `busd.channel.<own token>` and busd may write the catalogue, and name the bus owner:

```sh
docker compose -f deploy/docker-compose.yaml exec agent-bus agent-busctl --direct init   # with your --max-age/--max-bytes, if you changed them
BUS_AGENT=monitor@alice docker compose -f deploy/docker-compose.yaml up -d              # BUSD_BUS_OWNER follows BUS_AGENT
```

Without `init`, busd answers channel requests with a permission error. Channels made before have no
agent owner: the bus owner owns them.

**Upgrading from a release without the service logins.** Everything keeps working: busd and a monitor
that mounts the broker's config folder use the admin logins and say so in their log. To give them logins of
their own (busd) and the monitor a config folder without the CA key, once:

```sh
docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint agent-busctl agent-bus service init
docker compose -f deploy/docker-compose.yaml up -d                      # the broker reads the new logins
docker compose -f deploy/docker-compose.yaml run --rm monitor-init      # BUS_AGENT=monitor@alice in front to send
docker compose -f deploy/docker-compose.yaml --profile monitor up -d    # the monitor now mounts monitor-config
```
