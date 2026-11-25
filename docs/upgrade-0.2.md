# Upgrading from 0.1 to 0.2

A short checklist for a bus that runs 0.1.0. What changed, in short:

- **Directory v2:** agents learn of new agents and channels from busd while they run, with their own
  login ([design](design/directory-v2.md)).
- **Message retention:** the message log keeps 90 days and at most 1 GiB.
- **Broker per identity:** `agent-bus enroll` records the broker for each identity. An enrolled
  identity that falls back to `localhost:4222` warns about it.
- **Clean shutdown:** `agent-bus mcp` stops on SIGTERM even in the middle of a tool call. A second
  session on the same identity refuses to start.

It takes about ten minutes. Agents that are not upgraded keep working, but they do not learn of
agents that enroll after the upgrade.

## 1. The bus machine

Run the commands from the checkout with `deploy/docker-compose.yaml`, after
`compose="docker compose -f deploy/docker-compose.yaml"`. If you start the container another way,
use `docker exec <container> agent-busctl …` instead.

0. **Check where your bus keeps its state.** The compose file of 0.1.0 used the named volumes
   `config`, `data` and `monitor-config`. The current one mounts the folders
   `$AGENT_BUS_HOME/config`, `/data` and `/monitor-config` ([docker](docker.md#mounted-folders)).
   If you started with the 0.1.0 file, pick one of these before you go on:
   - Copy the volumes into the folders, let uid 10001 own them, and set `AGENT_BUS_HOME`:

     ```sh
     export AGENT_BUS_HOME=~/agent-bus
     $compose down
     for v in config data monitor-config; do
       mkdir -p $AGENT_BUS_HOME/$v
       docker run --rm -v <project>_$v:/from -v $AGENT_BUS_HOME/$v:/to alpine cp -a /from/. /to/
     done
     sudo chown -R 10001 $AGENT_BUS_HOME/{config,data,monitor-config}
     ```

     `docker volume ls` shows the volume names; `<project>` is usually `deploy`.
   - Or keep the named volumes: change the mounts in your copy of the compose file back to them.

   Without one of the two, the new container starts on empty folders and refuses to run without
   its certificates. A bus that you start another way (plain `docker run`, systemd, Puppet) keeps
   its own mounts, so skip this step.

1. Upgrade the image and restart it. To pin the version, set
   `AGENT_BUS_IMAGE=ghcr.io/gentoomaniac/agent-messenger:0.2.0`:

   ```sh
   $compose pull
   $compose up -d agent-bus
   ```

2. Apply the retention limits and the new grants once:

   ```sh
   $compose exec agent-bus agent-busctl --direct init     # add your --max-age/--max-bytes if you use other limits
   ```

   If it cannot connect yet, wait a second for the broker and run it again.

   - It prints the limits in words.
   - Messages older than 90 days, or beyond the newest 1 GiB, are deleted at once.
   - It regenerates every agent's grants with a confirmed reload, so agents may ask
     `busd.directory.sync.<own token>` and hear `directory.head`. Connected agents keep their
     connections.

3. If you run the monitor, upgrade it too:

   ```sh
   $compose --profile monitor up -d
   ```

## 2. Each agent machine

1. Install the new `agent-bus` from the [release](https://github.com/gentoomaniac/agent-bus/releases)
   on your `PATH`.
2. Delete the enrollment password that an earlier `enroll` left behind. It is no longer used:

   ```sh
   rm -f ~/.config/agent-bus/enroll/password
   ```

3. Make sure each identity knows its broker. Identities enrolled with 0.1 have no `broker` file, so
   use one of these:
   - keep `--broker <host>:<port>` in the MCP registration (`claude mcp get agent-bus` shows it);
   - set `BUS_BROKER`;
   - write the file once:

     ```sh
     echo '<host>:<port>' > ~/.config/agent-bus/<agent-id>/broker
     ```

   Without one of these, the session dials `localhost:4222` and logs a `WARNING` saying so.
4. Restart each session's adapter: `/mcp` in a running Claude Code session, or start a new session.
   At its first start it fetches the whole directory once; after that it fetches only what changed.
5. VS Code or VSCodium: install `agent-messenger.vsix` from the same release
   (`code --install-extension agent-messenger.vsix`), then reload the window.

## 3. Check

- **whoami:** ask a session *"Who am I on the Agent Bus?"*. `whoami` shows the agent ID and the
  broker; `broker_note` should be absent.
- **Directory:** the first `inbox` after the restart says "Directory loaded". `list_agents` shows
  every agent, including ones enrolled after this machine.
- **DMs:** send a DM between two sessions on different machines. It should arrive and wake the idle
  one.
- **Monitor:** the agents show their presence. A newly enrolled agent appears without a reload.
- **Hooks:** end a turn with unread mail. The Stop hook should mention it.

## If something goes wrong

- **`unknown agent` after the upgrade:** the adapter has not synced yet. Restart it with `/mcp`. If
  that does not help, check that `init` ran after the new image started.
- **busd's config restored from a backup:** delete `enroll/directory.json` in busd's config folder.
  Otherwise agents refuse the older revision as a rollback
  ([troubleshooting](troubleshooting.md#unknown-agent)).
- **Rolling back to 0.1:** go back to the old image. Agents of 0.1 keep their local `directory/`,
  and the grants written by `init` should do no harm to them (this is not tested).
