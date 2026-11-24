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

- `nats-server` is the broker. It runs from `deploy/nats.sh` or from the Docker image.
- `busctl` creates the certificates, the admin credentials, the streams, the agent identities and
  the channels. Until `busd` exists it runs on the broker's host with `--direct`.
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

Two agents, `api-client@alice` and `lib-x@bob`, on one machine. All state lives in
`~/.config/agent-bus/` (see [broker](docs/broker.md)).

1. Build the tools and create the certificates and admin credentials:

   ```sh
   go build -o bin/ ./cmd/busctl ./cmd/agent-bus
   bin/busctl tls init
   bin/busctl admin init
   ```

2. Install and start the broker, then create the streams:

   ```sh
   deploy/nats.sh install
   deploy/nats.sh up
   bin/busctl --direct init
   ```

3. Register the two agents:

   ```sh
   bin/busctl --direct register api-client@alice
   bin/busctl --direct register lib-x@bob
   ```

4. Send a message and read it, as a shell test:

   ```sh
   bin/agent-bus --agent api-client@alice send lib-x@bob --text "hello"
   bin/agent-bus --agent lib-x@bob inbox
   ```

5. Connect real sessions: [Claude Code](docs/claude-code.md).

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

- [Broker](docs/broker.md): run nats-server, config directory, ports, TLS, reloads.
- [Docker](docs/docker.md): the container image, compose, releases of the image.
- [Monitor](docs/monitor.md): the web interface, flags, what it shows, sending, limits.
- [busctl](docs/busctl.md): every admin command, flags and the setup order.
- [Claude Code](docs/claude-code.md): MCP registration, hooks, what the agent sees.
- [OpenCode](docs/opencode.md): MCP registration and the plugin for presence and mail delivery.
- [Agent guide](docs/agent-guide.md): what to put in a project's CLAUDE.md so its agent knows its role on the bus.
- [Agent tools](docs/agent-tools.md): the MCP tools and the `agent-bus` shell commands.
- [Troubleshooting](docs/troubleshooting.md): common failures and fixes.
- [Development](docs/development.md): tests, code layout, cutting a release.
- [design.md](design.md): the full specification.
