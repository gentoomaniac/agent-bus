# Releases and Docker

## The image

Every release pushes a multi-arch (linux amd64 and arm64) image to
`<registry>/<owner>/agent-messenger`, tagged with the version and `latest`. It holds the pinned
`nats-server` (v2.15.3), `busctl` and `agent-bus`, and runs as the non-root user `bus` (uid 10001).
The entrypoint renders `deploy/nats-server.conf` from the environment and runs `nats-server` as
PID 1, so `docker stop` stops it cleanly and `busctl --direct` inside the container can reload it.

Volumes:

| Path      | Content                                                                 |
|-----------|-------------------------------------------------------------------------|
| `/config` | `BUS_CONFIG_DIR`: certificates, admin credentials, agent keys, `nats/`  |
| `/data`   | JetStream storage (`BUS_DATA_DIR`)                                      |

Environment variables:

| Variable           | Default   | Meaning                                                   |
|--------------------|-----------|-----------------------------------------------------------|
| `BUS_NATS_PORT`    | `4222`    | client port (TLS only)                                    |
| `BUS_LISTEN_IP`    | `0.0.0.0` | address the client port binds to                          |
| `BUS_MONITOR_PORT` | `8222`    | monitoring port, always bound to 127.0.0.1 in the container |

The server refuses to start until `/config` holds the certificates and admin credentials. Create
them once with one-off containers on the same volumes. Clients must trust `ca.pem` and the broker
certificate must name the host they connect to, so pass every name or IP with `--san`:

```sh
docker volume create agent-bus-config
docker volume create agent-bus-data
docker run --rm -v agent-bus-config:/config --entrypoint busctl IMAGE tls init --san bus.example.org
docker run --rm -v agent-bus-config:/config --entrypoint busctl IMAGE admin init
```

Start the server and administer it with `docker exec`; `busctl` finds the config, the server
binary and the monitoring endpoint through the image's environment:

```sh
docker run -d --name agent-bus -p 4222:4222 -v agent-bus-config:/config -v agent-bus-data:/data IMAGE
docker exec agent-bus busctl --direct init
docker exec agent-bus busctl --direct register api-client@alice
```

`register` and `channel join` rewrite the authorization file and reload the server through its pid
file (`/config/nats/nats-server.pid`). Agent keys and credentials end up in `/config/<agent-id>/`;
copy `ca.pem` and the agent's directory to the agent's machine.

## Compose

[deploy/docker-compose.yaml](../deploy/docker-compose.yaml) is an example with one service, the two
volumes and the port. Run the one-off `busctl` commands above first, with the compose volume names
(`<project>_config`, `<project>_data`).

```sh
docker compose -f deploy/docker-compose.yaml up -d
```

## Cutting a release

`.github/workflows/release.yaml` runs on GitHub Actions and Gitea/Forgejo Actions. On every push
to `main` and every pull request it runs `make check` and a goreleaser snapshot build that
publishes nothing. A tag `v*` publishes the archives, `checksums.txt` and the changelog as a
release and pushes the image:

```sh
git tag v0.1.0
git push origin v0.1.0
```

Settings, all in the repository settings of the forge:

- secret `REGISTRY_TOKEN`: a token that may push packages and create releases (the job token is
  the fallback)
- variable `REGISTRY` (optional): registry host, default the host of the forge
- variable `IMAGE_NAME` (optional): full image name, default `<registry>/<owner>/agent-messenger`

To try the build locally: `goreleaser release --snapshot --clean --skip=publish`. The agents'
binary (`agent-bus`) is in the archives; the image is for the central components only.
