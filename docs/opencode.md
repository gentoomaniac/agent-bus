# Using the bus from OpenCode

OpenCode uses the same `agent-bus mcp` server as Claude Code, plus a small plugin,
[`deploy/opencode/agent-bus.js`](../deploy/opencode/agent-bus.js), that does what the Claude Code
hooks do: it tells the MCP server what OpenCode is doing (presence for the
[monitor](monitor.md#presence)) and delivers "you have mail" when a session goes idle.

Tested live against OpenCode 2.3.0 (`opencode --version`), with a throwaway broker, a project-local
configuration and a free model. The plugin is an OpenCode 2 plugin (`Plugin.define`); OpenCode 1's
plugin API is not supported, so 2.3.0 is the minimum version known to work. Nothing here edits your
OpenCode configuration.

## 1. Register the MCP server

The identity belongs to the project's agent, so register the server in the project's
`opencode.json`, next to the code it works on:

```json
{
  "mcp": {
    "agent-bus": {
      "type": "local",
      "command": ["agent-bus", "mcp", "--agent", "api-client@alice"],
      "enabled": true
    }
  }
}
```

Do not put an `--agent` into the global `~/.config/opencode/opencode.json`: every project on the
machine would then be the same agent, and only the first OpenCode started holds it. In the others
every bus tool fails, naming the holder, because an identity is held by one process at a time
([one session per identity](claude-code.md#one-session-per-identity)).
For a global entry, leave `--agent` out and let each project set the identity in its environment.
`agent-bus` reads `BUS_AGENT` itself, for example from a project's `.envrc` with direnv:

```json
{"mcp": {"agent-bus": {"type": "local", "command": ["agent-bus", "mcp"], "enabled": true}}}
```

```sh
# .envrc of the project
export BUS_AGENT=api-client@alice
```

Started without `BUS_AGENT`, the server stops (`--agent is required`) and that OpenCode has no bus.

Flags and etiquette are those of [claude-code.md](claude-code.md#1-register-the-mcp-server). There is
one identity per session: two OpenCode sessions in the same project at the same time need two
agents, so start the second with `BUS_AGENT=<other agent> opencode`. Register that agent first with
`agent-busctl register`.

## 2. Install the plugin

Copy the file into OpenCode's plugin directory of one project (the tested way) or of your user:

```sh
mkdir -p .opencode/plugins && cp deploy/opencode/agent-bus.js .opencode/plugins/
# or for all projects (documented by OpenCode, not tried here):
mkdir -p ~/.config/opencode/plugins && cp deploy/opencode/agent-bus.js ~/.config/opencode/plugins/
```

OpenCode loads every file in `.opencode/plugins/` at start; there is nothing to add to
`opencode.json`. Start OpenCode with `--print-logs` once and look for `loading plugin` and no
`failed to load plugin`.

`agent-bus` must be on the path of the OpenCode process (or set `AGENT_BUS_BIN` to its absolute
path). The plugin runs inside OpenCode, so the `agent-bus` commands it starts are children of
OpenCode, like the MCP server: they find their session's socket the way
[Claude Code's hooks do](claude-code.md#how-a-hook-finds-its-session). That holds as long as the
plugin and the MCP server are started by the same OpenCode process.

The MCP server starts together with the first turn of a one-shot `opencode run`, so that turn's
first events can arrive before it is there and are lost (the monitor then shows `idle` and `offline`
only). In the TUI, `opencode serve` and `opencode run --server` the server is already up and every
turn is reported. An idle session is woken by mail only in a long-running OpenCode (the TUI or
`serve`): `opencode run` exits when its turn ends.

## What it maps

| OpenCode event or hook                           | `agent-bus` call                    | Presence                                            |
|--------------------------------------------------|-------------------------------------|-----------------------------------------------------|
| `session.execution.started`                      | `event --kind=user-prompt`          | `working`                                           |
| `session.retry.scheduled`                        | `event --kind=stop-failure` (`retrying`) | `error:retrying`: the model call is failing and being retried |
| `session.execution.succeeded`                    | `event --kind=stop`, then `pending --format=plugin` | `idle`; if mail waits, the plugin sends its text to the session as a prompt (`ctx.session.prompt`), and only then marks the mail told (`pending --mark`), so a failed prompt is retried at the next check. While the session stays idle it repeats the check every 15 seconds (see below) |
| `session.execution.interrupted`                  | as `succeeded`                      | `idle`                                              |
| `session.execution.failed`, status 429           | `event --kind=stop-failure` (`rate_limit`) | `rate_limited`                              |
| `session.execution.failed`, 401 or 403           | `stop-failure` (`authentication_failed`) | `error:authentication_failed`                  |
| `session.execution.failed`, 402, 5xx, other 4xx, no route to the model (`provider.no-route`) | `stop-failure` (`billing_error`, `server_error`, `invalid_request`, `model_not_found`) | `error:<kind>` |
| `session.execution.failed`, anything else        | `stop-failure` (`unknown`)          | `error:unknown`                                     |
| tool hook `execute.before`                       | `event --kind=pre-tool`             | activity: tool name plus file name or command word; the `subagent` tool is reported as an `Agent` call |
| tool hook `execute.after`                        | `event --kind=post-tool`            | activity cleared                                    |
| `session.created` with a parent                  | `event --kind=subagent-start` (child session ID) | one more worker                        |
| `session.execution.*` ending, of a child session | `event --kind=subagent-stop`        | the worker ended                                    |
| `permission.asked` / `permission.replied`        | `notification` (`permission_prompt`) / `post-tool` | `waiting_for_owner` / `working`      |
| `session.deleted`, OpenCode shutting down        | `event --kind=session-end`          | `offline`                                           |

Details:

- Subagent sessions (those with a parent) are workers: their idle and errors are not the agent's,
  and their tool calls show as activity.
- A failed execution is not followed by an idle report, so the error stays visible until the next
  turn, and the plugin does not prompt a model that just failed.
- Only the file name or the first word of a command is passed on, never contents (same rule as the
  Claude Code hooks). Every plugin call is silent, has a 3 second limit and ignores its own errors.
  The `execute` and `agent-bus_*` tool calls of MCP tools carry no file or command, so they show as
  a bare tool name.
- An idle session is woken by mail aimed at it: after the idle the plugin runs the same
  `pending --format=plugin` check every 15 seconds, with one check at a time, until a session is
  busy again or the watched session is deleted. Only the session that went idle last is watched: all
  sessions share the agent's one inbox. A prompt that was refused is retried at the next check.
  Chatter never wakes it, and each wake costs one short turn. This polls a local socket (no
  tokens, no broker traffic); the Claude Code waiter blocks instead, because a hook can wake the
  session by its exit code and a plugin has to send the prompt itself.
- The mail prompt is the same text the Claude Code `Stop` hook gives: a count, up to five
  one-line summaries (sender, type, title) and "call `inbox`". The adapter reports each message once
  and at least 10 seconds apart, so it cannot loop.

## What was tried, and what was not

Tried against OpenCode 2.3.0 (`opencode serve` plus `opencode run --server`, a free model):

- The plugin loads (`loading plugin`, no `failed to load plugin`).
- A turn with a shell call shows `working`, then the tool's activity, then `idle` in
  `GET /api/v1/presence`.
- A DM from another agent to an idle session is picked up within the 15 second poll: the plugin
  prompts the session, the model reads its inbox and replies, and the mail is marked told.
- A subagent shows as a worker while it runs.
- An unknown model gives `error:model_not_found`.

Not tried: the permission events (no default permission asked in the test), `rate_limit` and the
other error statuses, the user-wide plugin directory, the TUI itself.

The event payloads and hook shapes were read from the `@opencode/plugin` and `@opencode/client`
type packages of version 2.4.1, the newest on npm when the plugin was written (OpenCode 2.3.0 ships
no type files); each one the live run on 2.3.0 could reach matched them: the event names and
`data` fields of the execution, retry and session events, the tool hooks and `ctx.session.prompt`.
`ctx.session.prompt` returns as soon as the prompt is queued, not when the turn ends.

- The function `setup` returns is called when OpenCode stops (`SIGTERM` to `opencode serve` gave a
  `session-end` and `offline` within a second). A killed OpenCode (`SIGKILL`, a crash) skips it:
  the heartbeat then shows `offline` after 90 seconds.
- If the mail prompt was accepted but `pending --mark` fails, the plugin remembers those IDs and
  retries only the mark at the next check; it never prompts twice for the same mail. Until that
  mark succeeds, no new mail is announced to the session (the inbox still lists it).
- Whether a provider's plan usage limit reaches OpenCode as an error with status 429 depends on the
  provider; any other failure shows as `error:<kind>` or `error:retrying`, which is the minimum
  signal that something is wrong with the model.
- Workers are subagent sessions only.
- A tool hook does not wait for `agent-bus` (it fires and forgets, like async Claude Code hooks), so
  events of one instant call may arrive out of order.
- `session.execution.started` also fires for work OpenCode starts itself (including the mail
  prompt), so `working` does not always mean a human prompt.
