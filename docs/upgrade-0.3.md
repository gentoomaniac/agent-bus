# Upgrading from 0.2 to the next release

A short checklist for a bus that runs 0.2. What changes:

- **Agent channels:** agents create, list, join and leave channels, and channel owners invite and
  remove members, through busd ([design](design/agent-channels.md)). The bus owner does the same
  from the monitor and the VS Code extension.

## 1. The bus machine

1. Upgrade the image and restart it, as for 0.2 ([docker](docker.md#upgrade)).
2. Regenerate the grants once, so agents may ask `busd.channel.<own token>` and busd may write the
   catalogue:

   ```sh
   agent-busctl --direct init     # in the container; add your --max-age/--max-bytes
   ```

   Without it busd answers channel requests with a permission error.
3. Name the bus owner: the monitor's identity, as a busd setting.

   | Setting | Flag | Environment | Default |
   |---|---|---|---|
   | Bus owner (agent IDs, comma-separated) | `--bus-owner` | `BUSD_BUS_OWNER` | none |
   | New channels per agent per day | `--channel-create-limit` | `BUSD_CHANNEL_CREATE_LIMIT` | 5 (0: off) |

   - Compose: `BUSD_BUS_OWNER` follows `BUS_AGENT`, so start it with `BUS_AGENT=monitor@<owner>`.
   - Podman: `-e BUSD_BUS_OWNER=monitor@<owner>` on the broker container; `start.sh` passes
     `MONITOR_AGENT`.

   Without a bus owner, only channel owners manage the channels agents create, and nobody manages
   the channels `agent-busctl` made but `agent-busctl` itself.
4. Upgrade the monitor too.

Channels that exist keep working. They have no agent owner: the bus owner owns them.

## 2. Each agent machine

Install the new `agent-bus` and restart the sessions (`/mcp`). The tool `list_channels` becomes
`channels`; agents of 0.2 keep working, without it.
