# Upgrading from 0.1 to 0.2

A checklist for a bus that runs 0.1.0. It takes about ten minutes. Agents that are not upgraded
keep working, but they do not learn of agents that enroll after the upgrade.

| What changed | Effect |
|---|---|
| **Directory v2** | agents learn of new agents and channels from busd while they run, with their own login ([design](design/directory-v2.md)) |
| **Message retention** | the message log keeps 90 days and at most 1 GiB: **older messages are deleted** |
| **Broker per identity** | `agent-bus enroll` records the broker for each identity. An enrolled identity that falls back to `localhost:4222` warns about it |
| **Clean shutdown** | `agent-bus mcp` stops on SIGTERM even in the middle of a tool call. A second session on the same identity refuses to start |

**Upgrade together:** the broker image first, then the agents. The bus (broker, busd and the
monitor) comes from one image; upgrade the monitor with it. The agent machines and the VS Code
extension can follow at their own pace.

## 0. Before you start

1. **Check the versions.** `agent-busctl --version` in the container and `agent-bus --version` on an
   agent machine tell you what runs.
2. **Take a backup.** The retention step below deletes messages for good, and the restore is the
   only way back. Keep the archive until the checks pass.
   - **Folders** (the current compose file, the Podman scripts):
     [docker](docker.md#backup-and-restore), [podman](podman.md#backup-and-restore).
   - **Named volumes** (the compose file of 0.1.0): stop the services, then copy each volume out.
     `docker volume ls` shows the names, usually `deploy_config`, `deploy_data` and
     `deploy_monitor-config`.

     ```sh
     docker compose -f deploy/docker-compose.yaml down
     ( umask 077; for v in config data monitor-config; do
         docker run --rm -v deploy_$v:/from alpine tar -C /from -cz . > ~/agent-bus-volume-$v.tar.gz
       done )
     ```
3. **Check where your bus keeps its state** (below), and which messages the new limits would delete:
   anything older than 90 days, or beyond the newest 1 GiB of the log. To keep more, note the
   `--max-age` and `--max-bytes` you want (`0` and `-1` keep everything) and pass them to `init` in
   step 2 ([agent-busctl](agent-busctl.md#init)).

**Where the state lives.** The compose file of 0.1.0 used the named volumes `config`, `data` and
`monitor-config`. The current one mounts the folders `config/`, `data/` and `monitor-config/` of
`$AGENT_BUS_HOME` ([docker](docker.md#mounted-folders)). If you started with the 0.1.0 file, pick
one of these before you go on:

- Copy the volumes into the folders, let uid 10001 own them, and set `AGENT_BUS_HOME`. `PROJECT`
  is the prefix of the volume names that `docker volume ls` shows, usually `deploy`:

  ```sh
  export AGENT_BUS_HOME=~/agent-bus
  PROJECT=deploy
  docker compose -f deploy/docker-compose.yaml down
  for v in config data monitor-config; do
    mkdir -p $AGENT_BUS_HOME/$v
    docker run --rm -v ${PROJECT}_$v:/from -v $AGENT_BUS_HOME/$v:/to alpine cp -a /from/. /to/
  done
  sudo chown -R 10001 $AGENT_BUS_HOME/{config,data,monitor-config}
  ```
- Or keep the named volumes: change the mounts in your copy of the compose file back to them.

Without one of the two, the new container starts on empty folders and refuses to run without its
certificates. A bus that you start another way (plain `docker run`, systemd, Puppet, the Podman
scripts) keeps its own mounts, so skip this.

## 1. The bus machine

Pick your runtime and set it up once in the shell you use. `agent-busctl` is the alias of
[agent-busctl.md](agent-busctl.md#running-it).

**Docker.** Load your settings file. It carries the compose command, the profiles, the proxy overlay,
`BUS_AGENT` and a pinned image ([docker.md](docker.md#quick-start)):

```sh
export AGENT_BUS_HOME=~/agent-bus
. $AGENT_BUS_HOME/compose.env
```

**Docker without a `compose.env`** (an install from the 0.1.0 compose file, or one started by hand):
write one first, so that this and every later upgrade loads it. Add the exports your setup used:
`BUS_AGENT` if the monitor sends as an identity, and, with a proxy, the overlay lines of
[docker.md](docker.md#publish-the-monitor-through-a-tls-proxy).

```sh
export AGENT_BUS_HOME=~/agent-bus
cat > $AGENT_BUS_HOME/compose.env <<EOF
export AGENT_BUS_HOME=$AGENT_BUS_HOME
export COMPOSE_PROFILES=monitor
compose="docker compose -f $PWD/deploy/docker-compose.yaml"
alias agent-busctl="\$compose exec agent-bus agent-busctl"
EOF
. $AGENT_BUS_HOME/compose.env
```

**Podman.** Export the image to run and set the alias of [podman.md](podman.md#4-create-the-streams).
The scripts read `IMG` from the environment first and from `$AGENT_BUS_HOME/env` second, so an old
`IMG=` line in `env` does not matter while the variable is exported:

```sh
export AGENT_BUS_HOME=~/agent-bus
export IMG=ghcr.io/gentoomaniac/agent-messenger:latest
alias agent-busctl='podman exec agent-bus agent-busctl'
```

| Step | Docker | Podman |
|---|---|---|
| 1. New image, restart | `$compose pull`, then `$compose up -d agent-bus` | `podman pull $IMG`; set `BUS_MAX_AGE` and `BUS_MAX_BYTES` in `$AGENT_BUS_HOME/env` if you want other limits than the defaults ([podman.md](podman.md#the-start-scripts)); then `$AGENT_BUS_HOME/start.sh`, which runs step 2 with those limits |
| 2. Retention and grants | `agent-busctl --direct init`, with your `--max-age` and `--max-bytes` | done by `start.sh`. Do not run `init` again by hand: it does not see `env` and applies the defaults |
| 3. The monitor | `$compose up -d` | `$AGENT_BUS_HOME/monitor.sh` |

This guide moves a 0.1 bus to 0.2 with the current image. To stop at 0.2 for now, pin it: add
`export AGENT_BUS_IMAGE=ghcr.io/gentoomaniac/agent-messenger:0.2.0` to `compose.env` (Docker) or set
`IMG=ghcr.io/gentoomaniac/agent-messenger:0.2.0` in `env` (Podman). The next step after this one is
[0.2 to 0.3](upgrade-0.3.md).

### Step 2 in detail

**This deletes messages.** `init` applies the retention limits at once: messages older than 90 days,
or beyond the newest 1 GiB, are gone when it returns, and no image downgrade brings them back.
Do this only with the backup of step 0, and add your `--max-age`/`--max-bytes` if you use other
limits. On Podman `start.sh` has run it already with the limits of `env`: skip the command and
compare its log with the output below.

```sh
agent-busctl --direct init
```

```text
streams BUS (keeps 2160h0m0s, at most 1073741824 bytes), PRESENCE and LEDGER and buckets registry and catalogue are in place, agent grants are up to date
```

If it cannot connect yet, wait a second for the broker and run it again. The output states the
limits in words. It also regenerates every agent's grants with a confirmed reload, so agents may ask
`busd.directory.sync.<own token>` and hear `directory.head`; connected agents keep their
connections.

**Checkpoint:** the broker log ends with `Server is ready`, and `agent-busctl --direct agent list`
shows your agents.

## 2. Each agent machine

### The adapter

1. Install the new `agent-bus` from the [release](https://github.com/gentoomaniac/agent-bus/releases)
   on your `PATH`.
2. Delete the enrollment password that an earlier `enroll` left behind. It is no longer used:

   ```sh
   rm -f ~/.config/agent-bus/enroll/password
   ```

3. Make sure each identity knows its broker. Identities enrolled with 0.1 have no `broker` file, so
   use one of these:
   - keep `--broker bus.example.com:4222` in the MCP registration (`claude mcp get agent-bus` shows
     it);
   - set `BUS_BROKER`;
   - write the file once:

     ```sh
     echo 'bus.example.com:4222' > ~/.config/agent-bus/api-client@alice/broker
     ```

   Without one of these, the session dials `localhost:4222` and logs a `WARNING` saying so.
4. Restart each session's adapter: `/mcp` in a running Claude Code session, or start a new session.
   At its first start it fetches the whole directory once; after that it fetches only what changed.

### VS Code or VSCodium

Install `agent-messenger.vsix` from the same release and reload the window:

```sh
code --install-extension agent-messenger.vsix
```

## 3. Check

| Check | How | Expected |
|---|---|---|
| Identity | ask a session *"Who am I on the Agent Bus?"* | `whoami` shows the agent ID and the broker, and `broker_note` is absent |
| Directory | the first `inbox` after the restart | says "Directory loaded"; `list_agents` shows every agent, including ones enrolled after this machine |
| DMs | send a DM between two sessions on different machines | it arrives and wakes the idle one |
| Monitor | open the page | the agents show their presence; a newly enrolled agent appears without a reload |
| Hooks | end a turn with unread mail | the Stop hook mentions it |

A healthy `whoami` looks like:

```json
{"agent_id": "api-client@alice", "channels": ["DM api-client@alice", "general"], "broker": "wss://bus.example.com:4223", "connected": true}
```

## If something goes wrong

| Symptom | Fix |
|---|---|
| `unknown agent` after the upgrade | the adapter has not synced yet: restart it with `/mcp`. If that does not help, check that `init` ran after the new image started |
| busd's config restored from a backup | delete `enroll/directory.json` in busd's config folder; otherwise agents refuse the older revision as a rollback ([troubleshooting](troubleshooting.md#unknown-agent)) |

**Rolling back to 0.1.** Going back to the old image does not undo anything `init` did: the
deleted messages stay deleted, and the grants and the migrated directory stay as they are. To
return to the state before the upgrade, stop the containers and restore the backup of step 0
([docker](docker.md#backup-and-restore), [podman](podman.md#backup-and-restore)); anything sent
since is lost. Agents of 0.1 keep their local `directory/`, and the grants written by `init` should
do no harm to them (this is not tested).
