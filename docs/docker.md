# Docker

The central components (`nats-server`, `busctl` and `bus-monitor`) run as a container image; this is
the way to run them. Agent machines run only the local `agent-bus` binary (release archive or
`go build ./cmd/agent-bus`) and need no Docker. For the release process see
[development.md](development.md#cutting-a-release); for running the broker as a plain process, which
is for development and tests only, see [development.md](development.md#running-the-broker-without-docker).

## The image

Every release pushes a multi-arch (linux amd64 and arm64) image to
`ghcr.io/gentoomaniac/agent-messenger`, tagged with the version and `latest`. It holds the pinned
`nats-server` (v2.15.3), `busctl`, `bus-monitor` and `agent-bus`, and runs as the non-root user `bus` (uid 10001).
The entrypoint renders `deploy/nats-server.conf` from the environment and runs `nats-server` as
PID 1, so `docker stop` stops it cleanly and `busctl --direct` inside the container can reload it.

To build the image from a checkout instead, for this machine's architecture (docker or podman,
whichever is installed):

```sh
make image                      # tags agent-messenger:dev
export AGENT_BUS_IMAGE=agent-messenger:dev
```

The compose file uses `$AGENT_BUS_IMAGE` and falls back to `ghcr.io/gentoomaniac/agent-messenger:latest`.
In the plain `docker run` commands below, replace `IMAGE` with the image name.

### Volumes

| Path      | Content                                                                 |
|-----------|-------------------------------------------------------------------------|
| `/config` | `BUS_CONFIG_DIR`: certificates, admin credentials, agent keys, `nats/`  |
| `/data`   | JetStream storage (`BUS_DATA_DIR`)                                      |

The container runs as uid 10001. Named volumes get the right owner automatically. A bind mount
keeps the owner of the host directory, so give it to that user first:

```sh
mkdir -p ./config ./data
sudo chown 10001 ./config ./data
```

### Environment variables

| Variable           | Default   | Meaning                                                     |
|--------------------|-----------|-------------------------------------------------------------|
| `BUS_NATS_PORT`    | `4222`    | client port (TLS only)                                      |
| `BUS_LISTEN_IP`    | `0.0.0.0` | address the client port binds to                            |
| `BUS_MONITOR_PORT` | `8222`    | monitoring port, always bound to 127.0.0.1 in the container |

## Set it up

[deploy/docker-compose.yaml](../deploy/docker-compose.yaml) has the broker service (`agent-bus`),
the volumes `config` and `data`, and the optional `monitor` service. Run everything from the
repository root. The server refuses to start until `/config` holds the certificates and the admin
credentials.

1. Create the certificates and admin credentials once, as one-off containers on the config volume.
   Clients must trust `ca.pem` and the broker certificate must name every host they connect to,
   so pass each name or IP with `--san` (without it an agent on another machine fails its TLS
   handshake, [troubleshooting](troubleshooting.md#tls-errors)). `localhost`, `127.0.0.1`, `::1`
   are always in it:

   ```sh
   docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint busctl agent-bus tls init --san bus.example.org
   docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint busctl agent-bus admin init
   ```

2. Start the broker:

   ```sh
   docker compose -f deploy/docker-compose.yaml up -d
   ```

3. Administer it with `busctl --direct` inside the container, which finds the config, the server
   binary and the monitoring endpoint through the image's environment. A shell alias shortens it
   (use the absolute path to the compose file to use it from any directory); the examples in the
   other docs write plain `busctl` and mean this alias:

   ```sh
   alias busctl='docker compose -f /path/to/deploy/docker-compose.yaml exec agent-bus busctl'

   busctl --direct init
   busctl --direct register api-client@alice
   ```

   Without an alias that is `docker compose -f deploy/docker-compose.yaml exec agent-bus busctl --direct init`.

`register` and `channel join` rewrite the authorization file and reload the server through its pid
file (`/config/nats/nats-server.pid`). Agent keys and credentials end up in `/config/<agent-id>/`,
the public records in `/config/directory/`.

### Without compose

The same with plain `docker run`, on named volumes `agent-bus-config` and `agent-bus-data`:

```sh
docker volume create agent-bus-config
docker volume create agent-bus-data
docker run --rm -v agent-bus-config:/config --entrypoint busctl IMAGE tls init --san bus.example.org
docker run --rm -v agent-bus-config:/config --entrypoint busctl IMAGE admin init
docker run -d --name agent-bus -p 4222:4222 -p 127.0.0.1:8080:8080 -v agent-bus-config:/config -v agent-bus-data:/data IMAGE
alias busctl='docker exec agent-bus busctl'
```

Podman takes the same commands. Rootless podman may not reach the published port over its default
bridge network; add `--network pasta` (or `--network host`) to `podman run`.

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
`docker run --rm -v agent-bus-config:/config:ro --entrypoint tar IMAGE -C /config -cf - ca.pem directory`
works while the broker is stopped. Treat the agent folder as a secret: it holds the identity.

### Keep the directory fresh

After every `register`, `channel join` and `agent remove` the agents' `directory/` must be copied
again; a running `agent-bus mcp` re-reads it every minute. `register` and `channel join` update
`/config/directory` themselves; after `agent remove` write the tombstone out first:

```sh
busctl --direct directory export /config/directory
docker compose -f deploy/docker-compose.yaml exec -T agent-bus tar -C /config -cf - directory \
  | ssh other 'tar -C ~/.config/agent-bus -xf -'
```

(`rsync` needs the files on the host first: `docker compose -f deploy/docker-compose.yaml cp agent-bus:/config/directory/. /tmp/directory`, then `rsync -a /tmp/directory/ other:~/.config/agent-bus/directory/`.)

## The web monitor

`bus-monitor` is in the image and runs as the compose `monitor` profile, on the broker's network
and config volume. It shows all traffic, so it is protected by a password you set first, and the
compose file publishes it on the host's loopback only. In the container it listens on `0.0.0.0`,
which is why the service sets `BUS_WEB_BEHIND_PROXY=true` (and why it will not start without the
password).

```sh
docker compose -f deploy/docker-compose.yaml exec -it agent-bus busctl monitor password
docker compose -f deploy/docker-compose.yaml --profile monitor up -d
curl -s -u me:'<password>' http://127.0.0.1:8080/api/v1/status
```

Open <http://127.0.0.1:8080> and log in; any host port works (`18080:8080`). To post as an agent,
register an identity first (`busctl --direct register monitor@alice`) and set `BUS_AGENT` in the
service. To publish it beyond the host, put a TLS proxy in front: the overlay
[docker-compose.proxy.yaml](../deploy/docker-compose.proxy.yaml) adds Caddy. See
[monitor.md](monitor.md#behind-a-reverse-proxy).

## Upgrade

```sh
docker compose -f deploy/docker-compose.yaml pull
docker compose -f deploy/docker-compose.yaml up -d
```

The volumes keep the certificates, credentials and messages. Agents reconnect by themselves
([troubleshooting](troubleshooting.md#when-the-bus-is-down)).
