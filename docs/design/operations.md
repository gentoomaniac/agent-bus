# Operations and deployment

[Design index](README.md) | previous: [monitor and API](monitor-and-api.md) | next: [milestones](milestones.md)

How the broker is run, how permission changes reach it (the confirmed reload), and how the pieces
are deployed and released. The step-by-step guides are the user documentation:
[docs/docker.md](../docker.md), [docs/podman.md](../podman.md), [docs/broker.md](../broker.md),
[docs/development.md](../development.md).

| Part | Status |
|---|---|
| `nats-server` as its own process, `deploy/nats-server.conf`, `deploy/nats.sh`, generated `auth.conf` and `control-auth.conf` | implemented |
| Confirmed reload (lock, candidate, validate, replace, signal, confirm) | implemented in `agent-busctl --direct` and in busd (enrollment approvals); the **generation numbers recorded in the ledger and crash recovery by busd** are planned |
| One image with `nats-server`, `busd`, `agent-busctl`, `agent-bus`, `bus-monitor`; compose files, Podman scripts (`deploy/podman/start.sh`, `monitor.sh`); Caddy and nginx proxy samples; release by tag (archives and image) | implemented (this replaced the Pod manifest of the original plan, decisions 15 and 31) |
| busd as a supervised service | minimal busd runs in the broker container, restarted by an entrypoint loop; systemd-style supervision **planned** |

## Deployment

The ports are 4222 (password login), 4223 (certificate login, `wss://`) and the monitor's 8080
(`127.0.0.1` by default, a TLS reverse proxy for anything else). nats-server's own monitoring
endpoint (8222) stays inside the container.

```mermaid
flowchart LR
  subgraph host["Central host"]
    subgraph broker["Container agent-bus"]
      nats["nats-server, ports 4222 and 4223"]
      busd["busd, started by the entrypoint, restarted if it stops"]
      busctl["agent-busctl, started with exec"]
    end
    subgraph monitorc["Container monitor, compose profile monitor"]
      monitor["bus-monitor, port 8080, shares the broker network"]
    end
    initc["monitor-init: agent-busctl monitor init"]
    proxy["Caddy or nginx, TLS, optional"]
    config[("volume config at /config")]
    monitorconfig[("volume monitor-config: no CA key, no admin logins")]
    data[("volume data at /data")]
  end
  agents["Agent machines: agent-bus mcp"]
  browser["Browser"]
  agents -->|"4222 password, TLS"| nats
  agents -->|"4223 certificate, wss"| nats
  browser -->|"https"| proxy
  proxy -->|"http, from a trusted proxy"| monitor
  browser -.->|"or 127.0.0.1:8080 directly"| monitor
  monitor -->|"localhost, shared network"| nats
  nats --- data
  nats --- config
  busd --- config
  busd -->|"localhost"| nats
  monitor -->|"busd.monitor.*"| busd
  initc -->|"reads"| config
  initc -->|"fills"| monitorconfig
  monitor --- monitorconfig
  busctl --- config
```

## Server configuration and reload

**Server configuration and reload.** `nats-server.conf` holds the static settings (listener, TLS, the two accounts, JetStream store directory, a local monitoring endpoint) and includes `auth.conf`, the users and permissions of `BUS`. busd and `agent-busctl --direct` are the only writers. A change is one serialised operation: take the file lock; write a candidate file with a new generation number; validate it (`nats-server -t`); atomically replace `auth.conf`; reload (`nats-server --signal reload` with the pid file); then confirm that the server loaded it (the monitoring endpoint's config load time moves past the reload). Only then is the change reported done. The ledger records the desired and the confirmed generation, so after a crash busd finishes an unconfirmed reload before anything else. A reload changes the permissions of live connections: a revoked grant removes the subscription at once, and removing a user disconnects it. Consumer cleanup (deleting the member's consumers, which cancels waiting pulls) is part of finishing a revocation, not an afterthought. Reconciliation ([private channels](private-channels.md)) reads the confirmed generation, not just the file. busd and `nats-server` run on the same host. Because the server is a separate process, a busd crash or restart drops no agent connection.

```mermaid
sequenceDiagram
  participant Op as agent-busctl --direct or busd
  participant Conf as auth.conf
  participant Server as nats-server
  Op->>Op: take the file lock
  Op->>Conf: write a candidate with a new generation number
  Op->>Server: nats-server -t validates the candidate
  Op->>Conf: atomically replace auth.conf
  Op->>Server: signal reload through the pid file
  Op->>Server: monitoring endpoint, config load time moved past the reload
  Op->>Op: report done, record the confirmed generation
```

Reloads go through the broker's container only; the monitor container never signals
([docs/broker.md](../broker.md)). The monitor's config volume is filled by `monitor-init` from the broker's config, which it reads only; the Podman scripts `deploy/podman/start.sh` and `monitor.sh` do the same with bind mounts.

## Deploy

**Deploy.** One image holds `nats-server`, `busd`, `agent-busctl`, `bus-monitor` and `agent-bus`. Its entrypoint renders the server configuration (`deploy/nats-server.conf`; `auth.conf` is generated, mode 0600), starts busd beside it in a restart loop where the config volume has `ca.key` (`BUS_BUSD=off` skips it) and runs `nats-server` as the main process, so signals reach it and `agent-busctl --direct` can reload it. The monitor runs from the same image in its own container, with a config volume of its own. Run it with the compose file in `deploy/` or the Podman scripts in `deploy/podman/`; a plain `nats-server` binary with the start script is for development and tests only ([docs/development.md](../development.md)). Nothing in the Go code or agent-busctl depends on how the processes start. There is no high availability, and [busd](busd.md) says what works while busd is down.

> **Superseded in part:** the broker is run from the Docker image, not from a Pod manifest
> (decisions 15 and 31).

## Releases

A tag `v*` publishes the archives, `checksums.txt` and the changelog as a GitHub release and pushes
the image to `ghcr.io`; every push to `main` runs `make check` and a snapshot build that publishes
nothing. The procedure is in [docs/development.md](../development.md#cutting-a-release). `make check`
(gofmt, vet, unit tests) needs no broker; `make itest` runs the integration tests against a real
`nats-server`.

## See also

- [Access control](access-control.md): the two listeners, enrollment and the reload topology (busd reloads inside the broker container).
- [JetStream binding](jetstream-binding.md): what the configuration contains.
