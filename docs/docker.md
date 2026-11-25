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
mkdir -p ~/agent-bus/{config,data,monitor-config}
sudo chown 10001 ~/agent-bus/{config,data,monitor-config}
```

The compose file reads the folders from next to itself, or from `AGENT_BUS_HOME`. Leave the file
in the repository and `export AGENT_BUS_HOME=~/agent-bus`; a copy elsewhere would also need
`deploy/proxy/Caddyfile` next to it for the proxy overlay. Named volumes work too if you replace
the mounts, but their files are hidden inside Docker's storage.

### Environment variables

| Variable           | Default   | Meaning                                                     |
|--------------------|-----------|-------------------------------------------------------------|
| `BUS_NATS_PORT`    | `4222`    | client port, password login (TLS only)                      |
| `BUS_MAPPED_PORT`  | `4223`    | client port, certificate login (TLS, `wss://`)              |
| `BUS_LISTEN_IP`    | `0.0.0.0` | address the client port binds to                            |
| `BUS_MONITOR_PORT` | `8222`    | monitoring port, always bound to 127.0.0.1 in the container |

## Set it up

[deploy/docker-compose.yaml](../deploy/docker-compose.yaml) has the broker service (`agent-bus`),
the bind mounts of `config/` and `data/`, and the optional `monitor` service. The commands below
run from the repository root with `AGENT_BUS_HOME=~/agent-bus` exported. The server refuses to start until `/config` holds the certificates and the admin
credentials.

1. Create the certificates and admin credentials once, as one-off containers on the config folder.
   Clients must trust `ca.pem` and the broker certificate must name every host they connect to,
   so pass each name or IP with `--san` (without it an agent on another machine fails its TLS
   handshake, [troubleshooting](troubleshooting.md#tls-errors)). `localhost`, `127.0.0.1`, `::1`
   are always in it:

   ```sh
   docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint agent-busctl agent-bus tls init --san bus.example.org
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

The same with plain `docker run`, on the folders of `~/agent-bus` (created and chowned as above):

```sh
docker run --rm -v ~/agent-bus/config:/config --entrypoint agent-busctl IMAGE tls init --san bus.example.org
docker run --rm -v ~/agent-bus/config:/config --entrypoint agent-busctl IMAGE admin init
docker run -d --name agent-bus -p 4222:4222 -p 4223:4223 -p 127.0.0.1:8080:8080 -v ~/agent-bus/config:/config -v ~/agent-bus/data:/data IMAGE
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

For another machine, run that with `ssh other 'tar -C ~/.config/agent-bus -xf -'` as the second
tar. `docker compose cp agent-bus:/config/ca.pem .` works for single files, and
`tar -C ~/agent-bus/config -cf - ca.pem directory` works on the host (the folder is owned by uid
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
  | ssh other 'tar -C ~/.config/agent-bus -xf -'
```

(`rsync` needs the files on the host first: `docker compose -f deploy/docker-compose.yaml cp agent-bus:/config/directory/. /tmp/directory`, then `rsync -a /tmp/directory/ other:~/.config/agent-bus/directory/`.)

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

**Upgrading from a release without the service logins.** Everything keeps working: busd and a monitor
that mounts the broker's config folder use the admin logins and say so in their log. To give them logins of
their own (busd) and the monitor a config folder without the CA key, once:

```sh
docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint agent-busctl agent-bus service init
docker compose -f deploy/docker-compose.yaml up -d                      # the broker reads the new logins
docker compose -f deploy/docker-compose.yaml run --rm monitor-init      # BUS_AGENT=monitor@alice in front to send
docker compose -f deploy/docker-compose.yaml --profile monitor up -d    # the monitor now mounts monitor-config
```
