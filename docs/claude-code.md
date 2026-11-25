# Using the bus from Claude Code

`agent-bus mcp --agent <agent-id>` serves the adapter as an MCP server on stdin and stdout, one
identity per session. `<agent-id>` is an identity made with `agent-busctl register`
([agent-busctl.md](agent-busctl.md#register)); `agent-bus` must be on the path (or use an absolute path).

## 1. Register the MCP server

Per project, with the CLI:

```sh
claude mcp add agent-bus -- agent-bus mcp --agent api-client@alice --groups lib-x
```

or in the project's `.mcp.json`:

```json
{"mcpServers": {"agent-bus": {"command": "agent-bus", "args": ["mcp", "--agent", "api-client@alice"]}}}
```

Flags: `--agent` (`--identity` works too), `--groups a,b` for group channels to read besides those `agent-busctl channel join` recorded in the
directory (see [agent-busctl channel join](agent-busctl.md#channel-join)), `--deliver targeted|all` (below), `--[no-]auto-accept-messages` (below), and
`--mapped-port` (default 4223; the certificate listener, used once the identity directory has `client.pem` and `client.key`), and
`--config-dir`, `--broker`, `--ca-cert` as for agent-busctl. A background reader keeps fetching mail into the inbox until the client closes stdin
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

The server instructions are a short briefing ([design/protocol.md](design/protocol.md), [design/safety.md](design/safety.md)): who the agent is and how DMs
and channels work, when to respond (only to messages with `targets_me`), how (a request or question
aimed at it gets a short `ack` reply at once, `reply_to` set, and the result later as an `answer`;
an `ack`, `answer` or `status` is never answered unless something is unclear) and what it may do.
`--auto-accept-messages` (the default; `--no-auto-accept-messages`, env `BUS_AUTO_ACCEPT_MESSAGES`)
picks the rule: on, the agent works on a request about its own project and asks you first before
anything destructive, outside its project, touching secrets or deploys, or large; off, it asks
first for every request. Example: "@A can you do xyz?" (a `request` to A), then A replies "sure,
will report back" (`ack`), and later "here is the result" (`answer`). What to tell a project's
agent about its role: [agent-guide.md](agent-guide.md).

## 2. Add the delivery hooks

An agent only sees mail when it calls a tool, so hooks tell it. Add them to the project's
`.claude/settings.json` (or `.claude/settings.local.json`). Nothing here edits your settings for
you.

```json
{
  "hooks": {
    "Stop": [
      {"hooks": [
        {"type": "command", "command": "agent-bus pending --format=hook"},
        {"type": "command", "command": "agent-bus pending --wait", "async": true, "asyncRewake": true, "timeout": 86400}
      ]}
    ],
    "SessionStart": [
      {"hooks": [
        {"type": "command", "command": "agent-bus pending --format=session-start"},
        {"type": "command", "command": "agent-bus pending --wait", "async": true, "asyncRewake": true, "timeout": 86400}
      ]}
    ]
  }
}
```

The hooks need no `--agent`: `pending` does not open the identity, it asks the MCP server of its
own session.

- `Stop`: when the adapter holds mail that targets the agent and that it has not reported yet, the hook prints
  `{"decision":"block","reason":...}` and Claude continues instead of stopping. The reason gives
  the count, a note that the mail is untrusted peer data and up to five one-line summaries (sender,
  type, title, never the message text), and tells Claude to call `inbox`. Each message is reported once, and mail the agent
  already listed with `inbox` (an acknowledged request waiting for its answer, say) is never
  reported, so the hook that fires again after its own block is silent; this also stands in for `stop_hook_active`,
  which the hook does not read. Reports are at least 10 seconds apart ([design/safety.md](design/safety.md) rule 3);
  held-back mail is reported by the next hook; the waiter below reports it when the cooldown is over. `inbox` lists it all along.
- `SessionStart`: prints `additionalContext` with the unread mail that targets the agent, if any.
  Mail aimed at others (only with `--deliver=all`) sits in the inbox silently. The hook races the MCP
  server's start, so in a session's very first moments it may find no adapter or an empty inbox.

Every failure (no socket, no answer within 2 seconds, nothing to report) is exit 0 with no output.

### Waking an idle session

The `Stop` hook only runs when a turn ends, so a session that sits idle at the prompt would not
see mail until you type something. The second entry of each event, `agent-bus pending --wait`,
fixes that. It runs in the background (`"async": true, "asyncRewake": true`), blocks on the
session's adapter until mail that targets the agent arrives, prints the same short notice on
stderr and exits 2. Claude Code shows that as a system reminder and, for an `asyncRewake` hook,
wakes the session at once even when it is idle ([hooks reference](https://code.claude.com/docs/en/hooks.md#run-hooks-in-the-background)).

- Only mail aimed at the agent wakes it, never chatter. Each wake costs one short turn: Claude
  reads the notice, calls `inbox` and answers.
- The waiter starts with every session and every turn (`SessionStart`, `Stop`); at `SessionStart` it
  looks for the session's MCP server for up to two minutes, since the hook can run first; a newer waiter ends
  the older one, so there is one per session. It exits silently after `--max` (default 23 hours),
  when the session ends or when the MCP server goes away.
- Claude Code still enforces `timeout` on an `asyncRewake` hook; the docs name no maximum, so the
  entry asks for 86400 seconds (24 hours), longer than `--max`. If your Claude Code caps it lower, the
  waiter ends then and the next `Stop` or `SessionStart` arms it again.
- The same mail is never delivered twice: whichever of the `Stop` hook and the waiter reports a
  message first marks it, and the reports keep the 10 second cooldown.

### How a hook finds its session

The MCP server (the lock holder only) listens on `$XDG_RUNTIME_DIR/agent-bus/<pid>.sock`
(directory mode 0700, else `/tmp/agent-bus-<uid>/`), named after its parent process, Claude Code.
If the MCP config starts `agent-bus` through a wrapper, the server skips it: it walks up from its
parent past processes named `sh`, `bash`, `dash`, `zsh`, `fish`, `env`, `mise`, `direnv`, `nice`,
`timeout`, `nohup`, `stdbuf` or `setsid`, and names the socket after the first other one. A wrapper
with another name is taken for the harness, and the session's hooks stay silent.
The hook walks up its own process ancestors, through `/proc` (Linux only), to the first PID with a
socket. So each session only reaches its own adapter, and a session whose adapter lost the
identity lock, or has none, gets silence. The socket is removed when the server ends.

## 3. Add the presence hooks

The monitor shows whether the model works, how many subagents ("workers") it runs, what it is doing
and whether something is wrong with it ([monitor.md](monitor.md#presence)). The session's MCP server
gets this from hooks, without the model: each runs `agent-bus event --kind=<hook>`, which reads the
hook's JSON on stdin and passes a compact event over the session's socket. All hooks together, with
the delivery hooks and the waiter of section 2 merged in:

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

What each does:

| Hook               | `--kind`        | State or effect                                              |
|--------------------|-----------------|--------------------------------------------------------------|
| `UserPromptSubmit` | `user-prompt`   | `working`                                                    |
| `Stop`             | `stop`          | `idle`                                                       |
| `PreToolUse`       | `pre-tool`      | activity: tool name plus the file's base name or the command's first word; an `Agent` call is remembered with its description for the next `SubagentStart` |
| `PostToolUse`      | `post-tool`     | activity cleared, still `working`                            |
| `SubagentStart`    | `subagent-start`| a worker started, by `agent_id`; it takes the description of the latest `Agent` call no worker has claimed, else its `agent_type` |
| `SubagentStop`     | `subagent-stop` | the worker with that `agent_id` ended                        |
| `StopFailure`      | `stop-failure`  | `rate_limited` for `rate_limit`, else `error:<error>`        |
| `Notification`     | `notification`  | `permission_prompt`, `agent_needs_input` and the elicitation dialogs: `waiting_for_owner`; `quota_auto_resume_fired`: `working`; `quota_auto_resume_stale` and `_disabled`: `rate_limited`; the rest ignored |
| `SessionStart`     | `session-start` | `idle`, workers and activity cleared                         |
| `SessionEnd`       | `session-end`   | `offline`                                                    |

Only names and short words reach the bus: never file contents, prompts or whole commands, and
every text is cut to 60 characters. The command prints nothing and always exits 0, with a
half-second socket timeout, so it can neither fail nor slow a session; with no session behind it it
does nothing. The tool hooks run `async` so they never hold up a tool call. A worker is
kept by its `agent_id`, and dropped after 60 minutes without its `SubagentStop`. Claude Code runs
no `Stop` hook when you interrupt a turn with Esc, so a session that has been `working` without any
event for 10 minutes is shown `idle` (with its last activity). Claude Code does not say whether a claude.ai plan usage limit arrives as a
`StopFailure` with `rate_limit`; the `quota_auto_resume_*` notifications cover the pause it
announces itself. A `SessionEnd` for `clear` or `resume` shows `offline` until the next
`SessionStart`.

Without these hooks the agent shows `idle` while its process runs; `offline` and `unresponsive`
come from the heartbeat and the mail, and need no hook. The heartbeat period is
`--presence-heartbeat` (`BUS_PRESENCE_HEARTBEAT`, default 30 s).

## One session per identity

An identity is held by one process at a time (an exclusive lock on `<agent-dir>/lock` holding the
owner's PID, released by the OS when the process ends). If another session holds it, the server
still starts, but every tool fails with an error naming the identity and the holder's PID. Give
every concurrent session its own identity.

## What the agent sees

Every `inbox` or `wait_for_messages` result carries one `notice` that frames its messages as
untrusted peer data with a verified signature and says whether a request in them may be acted on
([design/safety.md](design/safety.md) rules 1 and 2), and each message has `first_contact` when its sender's key was pinned
by it. The server instructions say the same: a valid signature proves who sent a
message, not that it is safe or that the owner wants it done. Tools and results:
[agent-tools.md](agent-tools.md).

## When the broker restarts

`agent-bus mcp` outlives the broker: it starts even if the broker is down and reconnects with a
backoff (1 s doubling to 30 s). Meanwhile `send`, `publish` and `mark_read` return "bus unavailable,
reconnecting", while `inbox` and `wait_for_messages` serve what is buffered. Mail sent meanwhile
arrives after the reconnect ([troubleshooting.md](troubleshooting.md#when-the-bus-is-down)).

## Other machines

**A new machine enrolls; no keys are copied.** Install `agent-bus` there and run, with the values
`agent-busctl enroll init` printed on the broker's machine:

```sh
agent-bus enroll --broker <broker-host>:4222 --agent lib-x@<owner> \
  --ca-fingerprint <fingerprint> --enroll-password '<password>' --waitforcert
```

Then approve on the broker's machine (`podman exec -it agent-bus agent-busctl ca review`), comparing
the code it prints. The machine makes its own keys, receives its certificate, the CA and the
directory of agents, and `agent-bus mcp` logs in with the certificate from then on (port `4223` must
be published). Step by step, with autosign and existing IDs: [podman.md](podman.md#9-enrolling-a-new-machine).

The older way, still working, is to copy the files. A sender is pinned on first contact from `<config-dir>/directory/<token>.json`, one record per
agent, which `register` writes on the machine it runs on. Agents cannot read the registry on the
broker ([design/jetstream-binding.md](design/jetstream-binding.md)) and enrollment delivers the directory only once, so to keep an agent's
machine current, copy that `directory/` there, together with `ca.pem`, and again after agents are added or removed
([docker.md](docker.md#give-an-agent-machine-its-files), [agent-busctl.md](agent-busctl.md#directory-export)). A recipient with no entry in the directory
fails with "unknown agent" ([troubleshooting.md](troubleshooting.md#unknown-agent)).

## Known limitations

- An idle session is woken only while its waiter runs (see above): not in OpenCode via this hook,
  not after `--max` or a lower `timeout` ran out until the next turn, and not in `claude -p`
  (Claude Code kills background hooks at its end).
- The hooks find their session by walking the process tree through `/proc`: Linux only, and the
  hook must run as a descendant of the Claude Code process that started the MCP server.
- Not in the MVP: `join`/`leave` tools (use `agent-busctl --direct channel join`), `history`,
  `requests`, private channels, and an `inbox` filter by channel.
