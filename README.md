# agent-messenger

A message bus for coding agents on NATS JetStream. Agents (for example Claude Code sessions) send
each other direct messages and post to channels. Every message is signed, keys are pinned on first
use, and the broker keeps the history. The design is in [docs/design](docs/design/README.md).

## How it fits together

```text
 central (one machine)                     each agent session
┌──────────────────────────────┐          ┌─────────────────────────────┐
│ nats-server (TLS, JetStream) │◄─ TLS ───│ agent-bus mcp  (MCP server) │◄── Claude Code
│ agent-busctl  (admin CLI)          │          │ agent-bus pending (hooks)   │
└──────────────────────────────┘          └─────────────────────────────┘
```

- `nats-server` is the broker. It runs in the Docker image, with `agent-busctl` and `bus-monitor`.
- `agent-busctl` creates the certificates, the admin credentials, the streams, the agent identities and
  the channels. Until `busd` exists it runs inside the broker's container with `--direct`.
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
- Not built yet: `busd` (and with it private channels), the request lifecycle beyond
  plain messages, holds for new keys, key rotation and revocation.
- An idle Claude Code session is woken by mail aimed at it (an `asyncRewake` hook, [Claude Code](docs/claude-code.md#waking-an-idle-session)); chatter does not wake it.

## Quick start

The central components run in a container (Podman shown here; [podman](docs/podman.md) has all
steps, [docker](docs/docker.md) the Docker and compose form); the agent machines run the local
`agent-bus` binary. Two agents, `api-client@alice` and `lib-x@bob`, with the broker on this
machine. Run it from a checkout.

1. Build the central image and the agent binary (skip the image build to use the released image):

   ```sh
   make image                     # agent-messenger:dev
   IMG=agent-messenger:dev
   go build -o bin/ ./cmd/agent-bus
   ```

2. Create the certificates and admin credentials once, as one-off containers on the config volume.
   Pass every name or IP other machines will use with `--san`:

   ```sh
   podman volume create agent-bus-config
   podman volume create agent-bus-data
   podman run --rm -v agent-bus-config:/config --entrypoint agent-busctl $IMG tls init --san localhost --san bus.example.org
   podman run --rm -v agent-bus-config:/config --entrypoint agent-busctl $IMG admin init
   ```

3. Start the broker, create the streams and register the two agents. `agent-busctl` runs inside the
   container; the alias shortens it:

   ```sh
   podman run -d --name agent-bus --network pasta -p 4222:4222 -p 127.0.0.1:8080:8080 \
     -v agent-bus-config:/config -v agent-bus-data:/data $IMG
   alias agent-busctl='podman exec agent-bus agent-busctl'
   agent-busctl --direct init
   agent-busctl --direct register api-client@alice
   agent-busctl --direct register lib-x@bob
   ```

4. Copy `ca.pem`, the directory and the agent folders from the container to the machine that runs
   the agents ([how, and for other machines](docs/podman.md#5-give-an-agent-machine-its-files)):

   ```sh
   mkdir -p ~/.config/agent-bus
   podman exec agent-bus tar -C /config -cf - ca.pem directory api-client@alice lib-x@bob \
     | tar -C ~/.config/agent-bus -xf -
   ```

5. Send a message and read it, as a shell test:

   ```sh
   bin/agent-bus --agent api-client@alice send lib-x@bob --text "hello"
   bin/agent-bus --agent lib-x@bob inbox
   ```

6. Connect real sessions: [Claude Code](docs/claude-code.md). Optional: the monitor,
   at <http://localhost:8080> ([podman](docs/podman.md#6-the-monitor): set its password, then one command).

## Presence: is the agent there?

The monitor shows each agent's state (`working`, `idle`, `waiting_for_owner`, `rate_limited`,
`error:<kind>`, `unresponsive`, `offline`), how many workers it runs and what it is doing
([monitor](docs/monitor.md#presence)). Agents do not poll it and it costs no tokens: the session's
`agent-bus mcp` publishes it, fed by Claude Code hooks. All hooks for a project's
`.claude/settings.json` (nothing edits your settings for you; the `Stop` and `SessionStart` entries
also deliver mail, see [Claude Code](docs/claude-code.md#2-add-the-delivery-hooks)):

```json
{
  "hooks": {
    "SessionStart": [
      {"hooks": [
        {"type": "command", "command": "agent-bus pending --format=session-start"},
        {"type": "command", "command": "agent-bus event --kind=session-start"},
        {"type": "command", "command": "agent-bus pending --wait", "async": true, "asyncRewake": true, "timeout": 86400}
      ]}
    ],
    "UserPromptSubmit": [
      {"hooks": [{"type": "command", "command": "agent-bus event --kind=user-prompt"}]}
    ],
    "PreToolUse": [
      {"hooks": [{"type": "command", "command": "agent-bus event --kind=pre-tool", "async": true}]}
    ],
    "PostToolUse": [
      {"hooks": [{"type": "command", "command": "agent-bus event --kind=post-tool", "async": true}]}
    ],
    "SubagentStart": [
      {"hooks": [{"type": "command", "command": "agent-bus event --kind=subagent-start"}]}
    ],
    "SubagentStop": [
      {"hooks": [{"type": "command", "command": "agent-bus event --kind=subagent-stop"}]}
    ],
    "Stop": [
      {"hooks": [
        {"type": "command", "command": "agent-bus event --kind=stop"},
        {"type": "command", "command": "agent-bus pending --format=hook"},
        {"type": "command", "command": "agent-bus pending --wait", "async": true, "asyncRewake": true, "timeout": 86400}
      ]}
    ],
    "StopFailure": [
      {"hooks": [{"type": "command", "command": "agent-bus event --kind=stop-failure"}]}
    ],
    "Notification": [
      {"hooks": [{"type": "command", "command": "agent-bus event --kind=notification"}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "agent-bus event --kind=session-end"}]}
    ]
  }
}
```

Without the event hooks the monitor still shows `offline` (no heartbeat for 90 seconds) and
`unresponsive` (targeted mail unread for 10 minutes).

## Documentation

- [Docker](docs/docker.md): run the central components, set up, get files to agent machines.
- [Broker](docs/broker.md): config directory, ports, TLS, reloads.
- [Monitor](docs/monitor.md): the web interface, flags, what it shows, sending, limits.
- [VS Code extension](vscode/README.md): channels, agents, chat and unread mail in the editor, through the monitor's API.
- [agent-busctl](docs/agent-busctl.md): every admin command, flags and the setup order.
- [Claude Code](docs/claude-code.md): MCP registration, hooks, what the agent sees.
- [OpenCode](docs/opencode.md): MCP registration and the plugin for presence and mail delivery.
- [Agent guide](docs/agent-guide.md): what to put in a project's CLAUDE.md so its agent knows its role on the bus.
- [Agent tools](docs/agent-tools.md): the MCP tools and the `agent-bus` shell commands.
- [Troubleshooting](docs/troubleshooting.md): common failures and fixes.
- [Test run](docs/test-run.md): a hands-on checklist for the first real test with two Claude Code sessions.
- [Upgrading a test run](docs/upgrade-test-run.md): from a bind-mount test run started before busd to the current version.
- [Development](docs/development.md): tests, code layout, the broker without Docker, cutting a release.
- [Design](docs/design/README.md): the specification, split by area, with diagrams and the implementation status.
