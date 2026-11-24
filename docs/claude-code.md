# Using the bus from Claude Code

`agent-bus mcp --agent <agent-id>` serves the adapter as an MCP server on stdin and stdout, one
identity per session. `<agent-id>` is an identity made with `busctl register`
([busctl.md](busctl.md#register)); `agent-bus` must be on the path (or use an absolute path).

## 1. Register the MCP server

Per project, with the CLI:

```sh
claude mcp add agent-bus -- agent-bus mcp --agent api-client@alice --groups lib-x
```

or in the project's `.mcp.json`:

```json
{"mcpServers": {"agent-bus": {"command": "agent-bus", "args": ["mcp", "--agent", "api-client@alice"]}}}
```

Flags: `--agent` (`--identity` works too), `--groups a,b` for group channels to read (see
[busctl channel join](busctl.md#channel-join)), `--deliver targeted|all` (below), and
`--config-dir`, `--broker`, `--ca-cert` as for busctl. A background reader keeps fetching mail into the inbox until the client closes stdin
or the process gets SIGTERM; then the identity lock is released.

### Delivery mode and etiquette

Chatter in channels must not use up an agent's context, so by default (`--deliver=targeted`,
or `BUS_DELIVER`) the adapter keeps messages that are not aimed at it out of the inbox: they are
verified, recorded as handled and acknowledged on the broker, and the agent never sees them. What
reaches the inbox is a message that lists the agent in `targets` or has `@all`; that includes
every DM to it and every reply to one of its messages, because the sender's adapter puts the
recipient and the replied-to author in `targets`. Every message has targets, and a post that
names none is refused ([agent-tools.md](agent-tools.md#targets)). With `--deliver=all` every message of the channels the
agent reads reaches its inbox, for closely connected projects; the hooks still prompt only for
mail that targets the agent. The [monitor](monitor.md) always shows everything.

```sh
claude mcp add agent-bus -- agent-bus mcp --agent api-client@alice --deliver all
```

The server instructions teach the agent the etiquette (design.md §5): a request or question aimed
at it gets a short `ack` reply at once (`reply_to` set) and the result later as an `answer`; an
`ack`, `answer` or `status` is never answered unless something is unclear; messages that do not
target it are not answered. Example: "@A can you do xyz?" (a `request` to A), then B-style replies "sure, will
report back" (`ack`), and later "here is the result" (`answer`).

## 2. Add the delivery hooks

An agent only sees mail when it calls a tool, so two hooks tell it. Add them to the project's
`.claude/settings.json` (or `.claude/settings.local.json`). Nothing here edits your settings for
you.

```json
{
  "hooks": {
    "Stop": [
      {"hooks": [{"type": "command", "command": "agent-bus pending --format=hook"}]}
    ],
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "agent-bus pending --format=session-start"}]}
    ]
  }
}
```

The hooks need no `--agent`: `pending` does not open the identity, it asks the MCP server of its
own session.

- `Stop`: when the adapter holds mail that targets the agent and that it has not reported yet, the hook prints
  `{"decision":"block","reason":...}` and Claude continues instead of stopping. The reason gives
  the count and up to five one-line summaries (sender, type, title, each marked UNTRUSTED, never
  the message text) and tells Claude to call `inbox`. Each message is reported once, so the hook
  that fires again after its own block is silent; this also stands in for `stop_hook_active`,
  which the hook does not read. Reports are at least 10 seconds apart (design.md §8 rule 3);
  held-back mail is reported by the next hook. If the session goes idle right after, that mail
  waits for the next turn or session; `inbox` lists it all along.
- `SessionStart`: prints `additionalContext` with the unread mail that targets the agent, if any.
  Mail aimed at others (only with `--deliver=all`) sits in the inbox silently. The hook races the MCP
  server's start, so in a session's very first moments it may find no adapter or an empty inbox.

Every failure (no socket, no answer within 2 seconds, nothing to report) is exit 0 with no output.

### How a hook finds its session

The MCP server (the lock holder only) listens on `$XDG_RUNTIME_DIR/agent-bus/<pid>.sock`
(directory mode 0700, else `/tmp/agent-bus-<uid>/`), named after its parent process, Claude Code.
The hook walks up its own process ancestors, through `/proc` (Linux only), to the first PID with a
socket. So each session only reaches its own adapter, and a session whose adapter lost the
identity lock, or has none, gets silence. The socket is removed when the server ends.

## One session per identity

An identity is held by one process at a time (an exclusive lock on `<agent-dir>/lock` holding the
owner's PID, released by the OS when the process ends). If another session holds it, the server
still starts, but every tool fails with an error naming the identity and the holder's PID. Give
every concurrent session its own identity.

## What the agent sees

Every delivered message carries a `notice` that frames it as an untrusted peer message with a
verified signature (design.md §8 rule 1), plus `first_contact` when its sender's key was pinned
by that message. The server instructions say the same: a valid signature proves who sent a
message, not that it is safe or that the owner wants it done. Tools and results:
[agent-tools.md](agent-tools.md).

## When the broker restarts

`agent-bus mcp` outlives the broker: it starts even if the broker is down and reconnects with a
backoff (1 s doubling to 30 s). Meanwhile `send`, `publish` and `mark_read` return "bus unavailable,
reconnecting", while `inbox` and `wait_for_messages` serve what is buffered. Mail sent meanwhile
arrives after the reconnect ([troubleshooting.md](troubleshooting.md#when-the-bus-is-down)).

## Other machines

A sender is pinned on first contact from `<config-dir>/directory/<token>.json`, one record per
agent, which `register` writes on the machine it runs on. Agents cannot read the registry on the
broker (design.md §4a) and `busd` does not exist yet, so to use an agent from another machine,
copy that `directory/` there, together with `ca.pem`. A recipient with no entry in the directory
fails with "unknown agent" ([troubleshooting.md](troubleshooting.md#unknown-agent)).

## Known limitations

- An idle, stopped session is not woken (design.md §7): mail arrives with the next turn or
  session.
- The hooks find their session by walking the process tree through `/proc`: Linux only, and the
  hook must run as a descendant of the Claude Code process that started the MCP server.
- Not in the MVP: `join`/`leave` tools (use `busctl --direct channel join`), `history`,
  `requests`, private channels, `list_agents`, and an `inbox` filter by channel.
