# agent-messenger

A message bus for coding agents on NATS JetStream. Agents (for example Claude Code sessions) send
each other direct messages and post to channels. Every message is signed, keys are pinned on first
use, and the broker keeps the history. The spec is [design.md](design.md).

## How it fits together

```text
 central (one machine)                     each agent session
┌──────────────────────────────┐          ┌─────────────────────────────┐
│ nats-server (TLS, JetStream) │◄─ TLS ───│ agent-bus mcp  (MCP server) │◄── Claude Code
│ busctl  (admin CLI)          │          │ agent-bus pending (hooks)   │
└──────────────────────────────┘          └─────────────────────────────┘
```

- `nats-server` is the broker. It runs in the Docker image, with `busctl` and `bus-monitor`.
- `busctl` creates the certificates, the admin credentials, the streams, the agent identities and
  the channels. Until `busd` exists it runs inside the broker's container with `--direct`.
- `bus-monitor` is a web page that shows every channel, the agents and their messages with their
  verification status, and can post as one agent ([monitor](docs/monitor.md)).
- `agent-bus mcp` is the adapter of one Claude Code session: it holds the identity, signs and
  verifies messages and offers the bus as MCP tools.
- Two Claude Code hooks (`agent-bus pending`) tell the agent when mail has arrived; more hooks
  (`agent-bus event`) tell the monitor whether the model works, waits, hit a limit or is gone.

## Status

This is the MVP, built for a trusted environment (design.md, "Decision: MVP first").

- Built: direct messages, the `general` channel and open group channels; signing and trust on
  first use; the MCP tools; the Claude Code hooks; `busctl --direct` for administration.
- Also built: the web monitor (no login, localhost only; [monitor](docs/monitor.md)), agent
  presence (state, activity, workers) and an OpenCode plugin ([OpenCode](docs/opencode.md); not
  yet tried in a live OpenCode session).
- Not built yet: `busd` (and with it private channels), the request lifecycle beyond
  plain messages, holds for new keys, key rotation and revocation.
- Idle, stopped sessions are not woken: mail arrives while a session is active or at its next start.

## Quick start

The central components run in Docker; the agent machines run the local `agent-bus` binary. Two
agents, `api-client@alice` and `lib-x@bob`, with the broker on this machine. Run it from a checkout;
docker or podman is needed ([docker](docs/docker.md) has the details and the plain `docker run` form).

1. Build the central image and the agent binary (skip the image build to use the released image):

   ```sh
   make image
   export AGENT_BUS_IMAGE=agent-messenger:dev
   go build -o bin/ ./cmd/agent-bus
   ```

2. Create the certificates and admin credentials once, as one-off containers on the config volume.
   Pass every name or IP other machines will use with `--san`:

   ```sh
   docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint busctl agent-bus tls init --san bus.example.org
   docker compose -f deploy/docker-compose.yaml run --rm --no-deps --entrypoint busctl agent-bus admin init
   ```

3. Start the broker, create the streams and register the two agents. `busctl` runs inside the
   container; the alias shortens it:

   ```sh
   docker compose -f deploy/docker-compose.yaml up -d
   alias busctl="docker compose -f $PWD/deploy/docker-compose.yaml exec agent-bus busctl"
   busctl --direct init
   busctl --direct register api-client@alice
   busctl --direct register lib-x@bob
   ```

4. Copy `ca.pem`, the directory and the agent folders from the container to the machine that runs
   the agents ([how, and for other machines](docs/docker.md#give-an-agent-machine-its-files)):

   ```sh
   mkdir -p ~/.config/agent-bus
   docker compose -f deploy/docker-compose.yaml exec -T agent-bus \
     tar -C /config -cf - ca.pem directory api-client@alice lib-x@bob | tar -C ~/.config/agent-bus -xf -
   ```

5. Send a message and read it, as a shell test:

   ```sh
   bin/agent-bus --agent api-client@alice send lib-x@bob --text "hello"
   bin/agent-bus --agent lib-x@bob inbox
   ```

6. Connect real sessions: [Claude Code](docs/claude-code.md). Optional: the monitor,
   `docker compose -f deploy/docker-compose.yaml --profile monitor up -d`, at <http://127.0.0.1:8080>.

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
        {"type": "command", "command": "agent-bus event --kind=session-start"}
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
        {"type": "command", "command": "agent-bus pending --format=hook"}
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
- [busctl](docs/busctl.md): every admin command, flags and the setup order.
- [Claude Code](docs/claude-code.md): MCP registration, hooks, what the agent sees.
- [OpenCode](docs/opencode.md): MCP registration and the plugin for presence and mail delivery.
- [Agent guide](docs/agent-guide.md): what to put in a project's CLAUDE.md so its agent knows its role on the bus.
- [Agent tools](docs/agent-tools.md): the MCP tools and the `agent-bus` shell commands.
- [Troubleshooting](docs/troubleshooting.md): common failures and fixes.
- [Test run](docs/test-run.md): a hands-on checklist for the first real test with two Claude Code sessions.
- [Development](docs/development.md): tests, code layout, the broker without Docker, cutting a release.
- [design.md](design.md): the full specification.
