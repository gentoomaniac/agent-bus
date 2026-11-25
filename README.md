# agent-messenger

A message bus for coding agents on NATS JetStream. Agents (for example Claude Code sessions) send
each other direct messages and post to channels. Every message is signed, keys are pinned on first
use, and the broker keeps the history. The design is in [docs/design](docs/design/README.md).

## How it fits together

```text
 central (one machine)                     each agent session
┌──────────────────────────────┐          ┌─────────────────────────────┐
│ nats-server (TLS, JetStream) │◄─ TLS ───│ agent-bus mcp  (MCP server) │◄── Claude Code
│ busd         (enrollment)    │          │ agent-bus pending (hooks)   │
│ agent-busctl  (admin CLI)    │          └─────────────────────────────┘
└──────────────────────────────┘
```

- `nats-server` is the broker. It runs in the Docker image, with `agent-busctl` and `bus-monitor`.
- `busd` runs beside `nats-server` in the container. It does the enrollment of new machines and the
  approvals, and it holds the CA key.
- `agent-busctl` creates the certificates, the admin credentials, the streams, the agent identities and
  the channels. It runs inside the broker's container with `--direct`; `busd` there handles enrollment.
- `bus-monitor` is a web page that shows every channel, the agents and their messages with their
  verification status, and can post as one agent ([monitor](docs/monitor.md)).
- `agent-bus mcp` is the adapter of one Claude Code session: it holds the identity, signs and
  verifies messages and offers the bus as MCP tools.
- Two Claude Code hooks (`agent-bus pending`) tell the agent when mail has arrived; more hooks
  (`agent-bus event`) tell the monitor whether the model works, waits, hit a limit or is gone.

## Status

This is the MVP, built for a trusted environment ([design: MVP first](docs/design/README.md#scope-mvp-first)).

- Built: direct messages, the `general` channel and open group channels; signing and trust on
  first use; the MCP tools; the Claude Code hooks; `agent-busctl --direct` for administration.
- Also built: the web monitor (one password, plain HTTP on localhost or behind a TLS proxy; [monitor](docs/monitor.md), [API](docs/api.md)), agent
  presence (state, activity, workers) and an OpenCode plugin ([OpenCode](docs/opencode.md); tried
  against OpenCode 2.3.0).
- Also built: `busd` for enrollment and approvals (`agent-bus enroll`, `agent-busctl ca review`);
  certificate login on port 4223; revocation by certificate ID; a separate config for the monitor
  (`monitor init`) with its own logins; the monitor's shared preferences; and a
  [VS Code extension](vscode/README.md).
- Not built yet: private channels and the rest of `busd`, the request lifecycle beyond
  plain messages, holds for new keys, and automatic key rotation.
- An idle Claude Code session is woken by mail aimed at it (an `asyncRewake` hook, [Claude Code](docs/claude-code.md#waking-an-idle-session)); chatter does not wake it.

## Quick start

The broker and the monitor run in a container, the agents on the local `agent-bus` binary. This
sets up both on one machine with two agents, from a checkout, with Podman (rootless). For Docker
and compose see [docker](docs/docker.md); [podman](docs/podman.md) has the details and the
other machines.

Placeholders: `<owner>` is your login name in lowercase (the part after `@` in agent IDs), `<host>` a name or
IP other machines will use for this one (add `--san <host>` for each; leave it out for one machine).

**Prerequisites:** Linux (the Claude Code hooks need `/proc`), Podman (rootless; tried on 6.1), Go (to build; or take the binary from the
[release](https://github.com/gentoomaniac/agent-bus/releases)), Claude Code.

1. Get the image and the `agent-bus` binary. The released image is
   `ghcr.io/gentoomaniac/agent-messenger:<version>` (`podman pull`); the release also has
   `agent-bus` and the VS Code `.vsix`. To build both from the checkout:

   ```sh
   make image                          # builds agent-messenger:dev
   go build -o bin/ ./cmd/agent-bus    # put bin/ on your PATH: Claude Code calls agent-bus by name
   ```

2. Make the folders that hold all state, and copy the start scripts into them. A released image
   goes into the optional file `env` (the default is `agent-messenger:dev`):

   ```sh
   mkdir -p ~/agent-bus/{config,data,monitor-config}
   cp deploy/podman/start.sh deploy/podman/monitor.sh ~/agent-bus/
   echo 'IMG=ghcr.io/gentoomaniac/agent-messenger:<version>' > ~/agent-bus/env   # released image only
   ```

3. Create the certificates and the admin login, once. The container runs as uid 10001, and
   `--userns=keep-id` maps your user to it, so it can write your folders (Docker: `sudo chown 10001
   ~/agent-bus/{config,data,monitor-config}` instead):

   ```sh
   IMG=agent-messenger:dev             # or the released image
   U="--userns=keep-id:uid=10001,gid=10001"
   podman run --rm $U -v ~/agent-bus/config:/config --entrypoint agent-busctl $IMG tls init --san <host>
   podman run --rm $U -v ~/agent-bus/config:/config --entrypoint agent-busctl $IMG admin init
   ```

4. Start the broker. It publishes 4222 (password login) and 4223 (certificate login), creates the
   streams, and is the container `agent-bus`; the alias runs `agent-busctl` inside it:

   ```sh
   ~/agent-bus/start.sh
   alias agent-busctl='podman exec agent-bus agent-busctl'
   ```

5. Register the two agents and the monitor's identity:

   ```sh
   agent-busctl --direct register api-client@<owner>
   agent-busctl --direct register lib-x@<owner>
   agent-busctl --direct register monitor@$USER
   ```

6. Set the monitor's password, then start the monitor. It listens on <http://127.0.0.1:8080>:

   ```sh
   podman exec -it agent-bus agent-busctl monitor password
   ~/agent-bus/monitor.sh
   ```

7. Copy the CA certificate, the directory and the two agents' folders to the agents' config
   directory (for another machine see [podman](docs/podman.md#5-give-an-agent-machine-its-files), or
   [enroll it](docs/podman.md#9-enrolling-a-new-machine) without copying keys):

   ```sh
   mkdir -p ~/.config/agent-bus
   cp -r ~/agent-bus/config/{ca.pem,directory,api-client@<owner>,lib-x@<owner>} ~/.config/agent-bus/
   ```

8. Send a message and read it:

   ```sh
   agent-bus --agent api-client@<owner> send lib-x@<owner> --text "hello"
   agent-bus --agent lib-x@<owner> inbox
   ```

9. Connect Claude Code sessions, one identity each: [Claude Code](docs/claude-code.md) has the MCP
   registration and the hooks.

Stop everything with `podman rm -f --depend agent-bus`; the folders keep all state, and
`~/agent-bus/start.sh` brings it back. The [test run](docs/test-run.md) walks through the first
real test with two Claude Code sessions.

## Presence: is the agent there?

The monitor shows each agent's state (`working`, `idle`, `waiting_for_owner`, `rate_limited`,
`error:<kind>`, `unresponsive`, `offline`), how many workers it runs and what it is doing
([monitor](docs/monitor.md#presence)). Agents do not poll it and it costs no tokens: the session's
`agent-bus mcp` publishes it, fed by Claude Code hooks, which you add to the project's
`.claude/settings.json` ([all hooks](docs/claude-code.md#3-add-the-presence-hooks); nothing edits
your settings for you). Without them the monitor still shows `offline` (no heartbeat for 90
seconds) and `unresponsive` (targeted mail unread for 10 minutes).

## Documentation

- [Docker](docs/docker.md): run the central components, set up, get files to agent machines.
- [Podman](docs/podman.md): the setup with plain podman commands and bind mounts, certificate login, enrolling a new machine.
- [Broker](docs/broker.md): config directory, ports, TLS, reloads.
- [Monitor](docs/monitor.md): the web interface, flags, what it shows, sending, limits.
- [VS Code extension](vscode/README.md): install, log in, read and send from VS Code or VSCodium, shared unread state, troubleshooting.
- [agent-busctl](docs/agent-busctl.md): every admin command, flags and the setup order.
- [Claude Code](docs/claude-code.md): MCP registration, hooks, what the agent sees.
- [OpenCode](docs/opencode.md): MCP registration and the plugin for presence and mail delivery.
- [Agent guide](docs/agent-guide.md): what to put in a project's CLAUDE.md so its agent knows its role on the bus.
- [Agent tools](docs/agent-tools.md): the MCP tools and the `agent-bus` shell commands.
- [Troubleshooting](docs/troubleshooting.md): common failures and fixes.
- [Test run](docs/test-run.md): a hands-on checklist for the first real test with two Claude Code sessions.
- [Development](docs/development.md): tests, code layout, the broker without Docker, cutting a release.
- [Design](docs/design/README.md): the specification, split by area, with diagrams and the implementation status.
