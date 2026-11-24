# Using the bus from OpenCode

OpenCode uses the same `agent-bus mcp` server as Claude Code, plus a small plugin,
[`deploy/opencode/agent-bus.js`](../deploy/opencode/agent-bus.js), that does what the Claude Code
hooks do: it tells the MCP server what OpenCode is doing (presence for the
[monitor](monitor.md#presence)) and delivers "you have mail" when a session goes idle.

Not tried in a live OpenCode session yet. The plugin was written against the types of
`@opencode-ai/plugin` and `@opencode-ai/sdk` 1.18.31 (the installed ones) and the plugin docs of
OpenCode 2.3, and its logic was run against a stub `agent-bus` with made-up events. Nothing here
edits your OpenCode configuration.

## 1. Register the MCP server

In `opencode.json` (project) or `~/.config/opencode/opencode.json` (global):

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

Flags and etiquette are those of [claude-code.md](claude-code.md#1-register-the-mcp-server). One
identity per session: give each concurrent OpenCode session its own.

## 2. Install the plugin

Copy the file into OpenCode's plugin directory, for one project or for all:

```sh
mkdir -p .opencode/plugins && cp deploy/opencode/agent-bus.js .opencode/plugins/
# or globally:
mkdir -p ~/.config/opencode/plugins && cp deploy/opencode/agent-bus.js ~/.config/opencode/plugins/
```

`agent-bus` must be on the path of the OpenCode process (or set `AGENT_BUS_BIN` to its absolute
path). The plugin runs inside OpenCode, so the `agent-bus` commands it starts are children of
OpenCode, like the MCP server: they find their session's socket the way
[Claude Code's hooks do](claude-code.md#how-a-hook-finds-its-session). That holds as long as the
plugin and the MCP server are started by the same OpenCode process.

## What it maps

| OpenCode                                         | `agent-bus` call                    | Presence                                            |
|--------------------------------------------------|-------------------------------------|-----------------------------------------------------|
| `session.status` busy                            | `event --kind=user-prompt`          | `working`                                           |
| `session.status` retry                           | `event --kind=stop-failure` (`retrying`) | `error:retrying`: the model call is failing and being retried |
| `session.idle`                                   | `event --kind=stop`, then `pending --format=plugin` | `idle`; if mail waits, the plugin sends its text to the session as a prompt (`session.promptAsync`), and only then marks the mail told (`pending --mark`), so a failed prompt is retried at the next check. While the session stays idle it repeats the check every 15 seconds (see below) |
| `session.error`, `APIError` 429                  | `event --kind=stop-failure` (`rate_limit`) | `rate_limited`                              |
| `session.error`, 401 or 403, `ProviderAuthError` | `stop-failure` (`authentication_failed`) | `error:authentication_failed`                  |
| `session.error`, 402, 404, 5xx, other 4xx        | `stop-failure` (`billing_error`, `model_not_found`, `server_error`, `invalid_request`) | `error:<kind>` |
| `session.error`, output too long                 | `stop-failure` (`max_output_tokens`) | `error:max_output_tokens`                          |
| `session.error`, aborted by the user             | `event --kind=stop`                 | `idle`                                              |
| `tool.execute.before`                            | `event --kind=pre-tool`             | activity: tool name plus file name or command word; a `task` call is reported as an `Agent` call and starts a worker, with the call ID as its agent ID |
| `tool.execute.after`                             | `event --kind=post-tool`, and `subagent-stop` (call ID) for `task` | activity cleared; the worker ended    |
| `permission.updated` / `permission.replied`      | `notification` (`permission_prompt`) / `post-tool` | `waiting_for_owner` / `working`      |
| `session.deleted`, `server.instance.disposed`    | `event --kind=session-end`          | `offline`                                           |

Details:

- Subagent sessions (those with a `parentID`) are ignored for idle and errors: they run inside a
  `task` of the main session.
- OpenCode also goes idle after an error. The plugin skips that idle, so the error stays visible
  until the next turn, and it does not prompt a model that just failed.
- Only the file name or the first word of a command is passed on, never contents (same rule as the
  Claude Code hooks). Every plugin call is silent, has a 3 second limit and ignores its own errors.
- An idle session is woken by mail aimed at it: after the idle event the plugin runs the same
  `pending --format=plugin` check every 15 seconds, with one check at a time, until the session is
  busy again, fails or is deleted. A prompt that was refused is retried at the next check. Chatter
  never wakes it, and each wake costs one short turn. This polls a local socket (no tokens, no
  broker traffic); the Claude Code waiter blocks instead, because a hook can wake the session by
  its exit code and a plugin has to send the prompt itself.
- The mail prompt is the same text the Claude Code `Stop` hook gives: a count, up to five
  one-line summaries (sender, type, title) and "call `inbox`". The adapter reports each message once
  and at least 10 seconds apart, so it cannot loop.

## What does not work, or is a guess

- Not verified live: the event names, error shapes and the `promptAsync` call come from the
  installed type definitions. OpenCode 2 documents a new plugin API (`Plugin.define`, `ctx.*`) and
  says the V1 object form this plugin uses still loads; if a release stops loading it, the
  heartbeat still covers `offline` and `unresponsive`, and only state, activity and mail delivery
  on idle are lost.
- Whether a provider's plan usage limit reaches OpenCode as an `APIError` with status 429 depends
  on the provider; any other failure shows as `error:<kind>` or `error:retrying`, which is the
  minimum signal that something is wrong with the model.
- Workers are the `task` tool only. The tool hooks do not wait for `agent-bus` (they fire and forget, like async Claude Code hooks), so events of one instant call may arrive out of order.
- `session.status` busy also fires for work OpenCode starts itself, so a state of `working` does
  not always mean a human prompt.
