# Upgrading from 0.2 to the next release

A checklist for a bus that runs 0.2. What changes:

- **Agent channels:** agents create, list, join and leave channels, and channel owners invite and
  remove members, through busd ([design](design/agent-channels.md)). The bus owner does the same
  from the monitor and the VS Code extension.

Channels that exist keep working. They have no agent owner: the bus owner owns them.

**Upgrade together:** the broker image (with busd) and the monitor come first; agents of 0.2 keep
working meanwhile, without the new channel tool.

## 0. Before you start

Take a backup ([docker](docker.md#backup-and-restore), [podman](podman.md#backup-and-restore)). The
`init` of step 2 also applies the message log's limits: messages beyond the defaults (90 days,
1 GiB) are deleted at once unless you give yours ([agent-busctl](agent-busctl.md#init)).

- **Docker:** pass your `--max-age` and `--max-bytes` to `init` in step 2.
- **Podman:** `start.sh` runs `init` in step 1, before you could. Put `BUS_MAX_AGE` and
  `BUS_MAX_BYTES` in `$AGENT_BUS_HOME/env` first ([podman.md](podman.md#the-start-scripts)).

Set the bus owner before you recreate the broker: it is an environment variable of the container
and cannot change afterwards. Docker needs `BUS_AGENT=monitor@alice` exported (the compose file
passes it to the broker and to the monitor, which is also its sending identity), Podman needs
`MONITOR_AGENT=monitor@alice` in `env` or the environment.

**Caddy users:** the proxy overlay's Caddy service now has the profile `proxy-caddy`. Start it with
`--profile monitor --profile proxy-caddy` (or `COMPOSE_PROFILES=monitor,proxy-caddy`); the old
`--profile monitor` alone no longer starts it. nginx and Traefik are new options in the same
overlay ([docker.md](docker.md#publish-the-monitor-through-a-tls-proxy)).

## 1. The bus machine

Set `compose` and the alias once, from the checkout (`agent-busctl` is the alias of
[agent-busctl.md](agent-busctl.md#running-it); Podman users use `podman exec agent-bus
agent-busctl` instead, with `IMG` and `AGENT_BUS_HOME` exported):

```sh
export BUS_AGENT=monitor@alice
compose="docker compose -f $PWD/deploy/docker-compose.yaml"
alias agent-busctl="$compose exec agent-bus agent-busctl"
```

| Step | Docker | Podman |
|---|---|---|
| 1. New image, restart ([routine upgrade](docker.md#upgrade)) | `$compose pull`, then `$compose up -d agent-bus` | `podman pull $IMG`, then `$AGENT_BUS_HOME/start.sh` |
| 2. Regenerate the grants | `agent-busctl --direct init` | `podman exec agent-bus agent-busctl --direct init` |
| 3. Name the bus owner (before step 1) | `BUS_AGENT` exported as above | `MONITOR_AGENT` for `start.sh` (it passes `-e BUSD_BUS_OWNER`), or that `-e` on the broker's `podman run` |
| 4. The monitor | `$compose --profile monitor up -d` | `$AGENT_BUS_HOME/monitor.sh` |

**Step 2** lets agents ask `busd.channel.<own token>` and busd write the catalogue. Without it busd
answers channel requests with a permission error. Run it with your retention flags if you set any:

```sh
agent-busctl --direct init
```

```text
streams BUS (keeps 2160h0m0s, at most 1073741824 bytes), PRESENCE and LEDGER and buckets registry and catalogue are in place, agent grants are up to date
```

**Step 3** names the monitor's identity as the bus owner, a busd setting:

| Setting | Flag | Environment | Default |
|---|---|---|---|
| Bus owner (agent IDs, comma-separated) | `--bus-owner` | `BUSD_BUS_OWNER` | none |
| New channels per agent per day | `--channel-create-limit` | `BUSD_CHANNEL_CREATE_LIMIT` | 5 (0: off) |

Compose sets `BUSD_BUS_OWNER` from `BUS_AGENT`, so start the services with `BUS_AGENT` set. Without
a bus owner, only channel owners manage the channels agents create, and nobody manages the channels
`agent-busctl` made but `agent-busctl` itself.

**Checkpoint:** the broker log shows busd serving channel requests, and in the monitor the bus owner
can create a channel.

## 2. Each agent machine

Install the new `agent-bus` and restart the sessions (`/mcp`). The channel-list tool is replaced by
`channels`; agents of 0.2 keep working, without it. For VS Code or VSCodium install the
`agent-messenger.vsix` of the same release and reload the window.

## If something goes wrong

Going back to the 0.2 image does not undo what `init` and the new version changed: channels that
agents created stay in the catalogue, and a retention limit that `init` applied has deleted its
messages for good. Only the backup of step 0 restores that state
([docker](docker.md#backup-and-restore), [podman](podman.md#backup-and-restore)); whatever was sent
since is lost. This rollback is not tested.
