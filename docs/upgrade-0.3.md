# Upgrading from 0.2 to 0.3

A checklist for a bus that runs 0.2 (a 0.1 bus goes through [0.1 to 0.2](upgrade-0.2.md) first).
0.3.0 is the next release and is not tagged yet. Images are published only for release tags, so
until 0.3.0 is out, `latest` is still 0.2: to try these changes before the release, build the image
from a checkout of `main` (`make image`, then `export AGENT_BUS_IMAGE=agent-messenger:dev` for Docker
or `export IMG=agent-messenger:dev` for Podman). What changes:

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
- **Podman:** `start.sh` runs `init` in step 1, before you could, and it passes the limits of
  `$AGENT_BUS_HOME/env`. Put `BUS_MAX_AGE` and `BUS_MAX_BYTES` there first
  ([podman.md](podman.md#the-start-scripts)). Do not run `init` by hand afterwards: it does not see
  `env` and applies the defaults.

Set the bus owner before you recreate the broker: it is an environment variable of the container
and cannot change afterwards. Docker needs `BUS_AGENT=monitor@alice` in `compose.env` (the compose file
passes it to the broker and to the monitor, which is also its sending identity), Podman needs
`MONITOR_AGENT=monitor@alice` in `env` or the environment.

**Caddy users:** the proxy overlay's Caddy service now has the profile `proxy-caddy`. Start it with
`--profile monitor --profile proxy-caddy` (or `COMPOSE_PROFILES=monitor,proxy-caddy`); the old
`--profile monitor` alone no longer starts it. nginx and Traefik are new options in the same
overlay ([docker.md](docker.md#publish-the-monitor-through-a-tls-proxy)).

## 1. The bus machine

**Docker.** Load your settings, which carry the compose command, the profiles, the proxy overlay and
the image ([docker.md](docker.md#quick-start)), and set the bus owner in them if it is not there yet:

No `compose.env` yet (you started compose by hand)? Stop here and write it first as in
[upgrade-0.2.md](upgrade-0.2.md#1-the-bus-machine), with your proxy's overlay lines if you use one.
Then:

```sh
export AGENT_BUS_HOME=~/agent-bus
if [ -f "$AGENT_BUS_HOME/compose.env" ]; then
  grep -q '^export BUS_AGENT=' "$AGENT_BUS_HOME/compose.env" ||
    echo 'export BUS_AGENT=monitor@alice' >> "$AGENT_BUS_HOME/compose.env"
  . "$AGENT_BUS_HOME/compose.env"
else
  echo "no compose.env: write it first, as described above"
fi
```

**Podman.** Export `AGENT_BUS_HOME` and `IMG`, and set the plain alias of
[podman.md](podman.md#4-create-the-streams):

```sh
export AGENT_BUS_HOME=~/agent-bus
export IMG=ghcr.io/gentoomaniac/agent-messenger:latest
alias agent-busctl='podman exec agent-bus agent-busctl'
```

| Step | Docker | Podman |
|---|---|---|
| 1. New image, restart ([routine upgrade](docker.md#upgrade)) | `$compose pull`, then `$compose up -d agent-bus` | `podman pull $IMG`, then `$AGENT_BUS_HOME/start.sh`, which runs step 2 with the limits of `env` |
| 2. Regenerate the grants | `agent-busctl --direct init`, with your `--max-age` and `--max-bytes` | done by `start.sh`; do not run `init` again |
| 3. Name the bus owner (before step 1) | `BUS_AGENT` exported as above | `MONITOR_AGENT` for `start.sh` (it passes `-e BUSD_BUS_OWNER`), or that `-e` on the broker's `podman run` |
| 4. The monitor | `$compose up -d` | `$AGENT_BUS_HOME/monitor.sh` |

**Step 2** lets agents ask `busd.channel.<own token>` and busd write the catalogue. Without it busd
answers channel requests with a permission error. On Docker run it with your retention flags if
you set any; on Podman read it from the log of `start.sh`:

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

**Checkpoint:** the broker log shows busd serving channel requests, and the bus owner can create a
channel. The monitor's web page cannot do that; use the API
([channel changes](api.md#channel-changes)) with the login token of [api.md](api.md) in `$TOKEN` and
the monitor's address in `$BASE`, or **Create Channel...** in the VS Code extension:

```sh
curl -s -X POST $BASE/api/v1/channels/create -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"channel": "upgrade-check", "description": "checks the 0.3 upgrade"}'
```

The answer is the channel as busd describes it ([channel changes](api.md#channel-changes)).

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
