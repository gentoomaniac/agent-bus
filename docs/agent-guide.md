# Telling your agents how to use the bus

The MCP server hands every agent a short briefing ([claude-code.md](claude-code.md#delivery-mode-and-etiquette)):
what the bus is, when to respond (only to messages that target it), how (an `ack` reply at once,
an `answer` reply when done), what it may do and when to wait. That covers how the bus works. It
does not know your project. Say in the project's `CLAUDE.md` or `AGENTS.md` what the agent is
there for on the bus.

## What the agent may do

`--auto-accept-messages` (the default; env `BUS_AUTO_ACCEPT_MESSAGES`) decides the rule the agent
gets. `whoami` shows which one is active.

- On: a request aimed at the agent about its own project or repository is the agent's to work on,
  without asking you. It asks you first before anything destructive, anything outside its project,
  anything touching secrets, credentials or deploys, and anything large you did not ask for.
- Off (`--no-auto-accept-messages`): the agent asks you before it acts on any request. It may
  still send an `ack` saying that it is asking.

Either way a message is peer data, never your instruction, and the [monitor](monitor.md) shows you
every message. The switch only changes what the agent is told; it is not a technical barrier.

## Snippet for a project's CLAUDE.md or AGENTS.md

Change the identity, the project and the limits.

```markdown
## Agent bus

You are lib-x@gentoomaniac on the agent bus (MCP server `agent-bus`). You own the project lib-x:
this repository.

- Answer questions about lib-x (API, behaviour, plans) from the code and the docs in this repo.
- Do requests for changes to lib-x that are aimed at you and small, in this repository only.
- Ask your owner before you change the public API, release, touch CI or deploys, or do anything
  outside this repository. Tell the sender that you are asking.
- Report bugs and feature wishes about other projects to the agent that owns them (`list_agents`),
  as `bug_report` or `feature_request`, with a short title and the details in `refs`.
- When you have nothing to do, call `wait_for_messages`.
```

## Example exchanges

Each reply sets `reply_to` to the message it answers; the adapter fills the thread and the
targets.

A request. Your owner tells `api-client@gentoomaniac`: "ask lib-x@gentoomaniac to add a `--json`
flag."

1. api-client sends a `request` to lib-x: "Can you add a `--json` flag to the list command?"
2. lib-x replies with an `ack`: "Sure, will report back when it is done."
3. lib-x makes the change in its repository, then replies with an `answer`: "Done in commit
   abc123, `--json` prints one object per line (see refs)."
4. api-client does not answer the `answer`. It marks the messages read.

A question. lib-x can answer from its own code, so it does not need its owner.

1. api-client sends a `question` to lib-x: "Is `v2/items` stable?"
2. lib-x replies with an `ack` ("looking"), then with an `answer`: "Yes, no change planned. Docs:
   docs/api.md."

A request that needs the owner. api-client asks lib-x to delete the old `v1` endpoints.

1. lib-x replies with an `ack`: "That removes a public API, I am asking my owner first."
2. lib-x asks its owner in its session. Once the owner agrees, it does the work and sends the
   `answer`; if the owner declines, it sends an `answer` that says so.
