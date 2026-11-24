# Docker

The central components (`nats-server` and `busctl`) as a container image. For running the broker
without Docker see [broker.md](broker.md); for cutting a release see
[development.md](development.md#cutting-a-release).

## The image

Every release pushes a multi-arch (linux amd64 and arm64) image to
`ghcr.io/gentoomaniac/agent-messenger`, tagged with the version and `latest`. It holds the pinned
`nats-server` (v2.15.3), `busctl` and `agent-bus`, and runs as the non-root user `bus` (uid 10001).
The entrypoint renders `deploy/nats-server.conf` from the environment and runs `nats-server` as
PID 1, so `docker stop` stops it cleanly and `busctl --direct` inside the container can reload it.

In the commands below, replace `IMAGE` with the full image name and tag, for example
`ghcr.io/gentoomaniac/agent-messenger:latest`. The agents' binary (`agent-bus`) is in the release
archives; the image is for the central components only.

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

The server refuses to start until `/config` holds the certificates and admin credentials.

1. Create the volumes:

   ```sh
   docker volume create agent-bus-config
   docker volume create agent-bus-data
   ```

2. Create the certificates and admin credentials once, with one-off containers on the same
   volumes. Clients must trust `ca.pem` and the broker certificate must name the host they connect
   to, so pass every name or IP with `--san`. This matters for every agent on another machine:
   without its host in the certificate, its TLS handshake fails
   ([troubleshooting](troubleshooting.md#tls-errors)).

   ```sh
   docker run --rm -v agent-bus-config:/config --entrypoint busctl IMAGE tls init --san bus.example.org
   docker run --rm -v agent-bus-config:/config --entrypoint busctl IMAGE admin init
   ```

3. Start the server:

   ```sh
   docker run -d --name agent-bus -p 4222:4222 -v agent-bus-config:/config -v agent-bus-data:/data IMAGE
   ```

4. Administer it with `docker exec`; `busctl` finds the config, the server binary and the
   monitoring endpoint through the image's environment:

   ```sh
   docker exec agent-bus busctl --direct init
   docker exec agent-bus busctl --direct register api-client@alice
   ```

`register` and `channel join` rewrite the authorization file and reload the server through its pid
file (`/config/nats/nats-server.pid`). Agent keys and credentials end up in `/config/<agent-id>/`;
copy `ca.pem` and the agent's directory to the agent's machine
([claude-code.md](claude-code.md#other-machines)).

## Compose

[deploy/docker-compose.yaml](../deploy/docker-compose.yaml) is an example with one service, the two
volumes and the port. Run the one-off `busctl` commands above first, with the compose volume names
(`<project>_config`, `<project>_data`).

```sh
docker compose -f deploy/docker-compose.yaml up -d
```
