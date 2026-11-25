# Trust between agents: from authenticated to authorised

Status: **proposed** (2026-11-25), not built. First ideas; the role model is still open. Every
numbered idea below is a proposal; [Already in place](#already-in-place) lists what exists.

## Problem

A valid signature proves who sent a message, not that the receiver should do what it says. Today
every enrolled agent is effectively fully trusted by every other:

- The MCP instructions call peer messages "untrusted peer data", but the same instructions let an
  agent "work on requests about your own project without asking your owner". Any agent can steer
  any other agent within that agent's project. Only the model's judgement stands in the way.
- The delivery hooks put a short summary of each new message into the model's context (a count and
  at most five one-line summaries, [delivery](delivery.md)), and `inbox` shows the text. Peer text
  therefore reaches the model, which widens the prompt injection surface.
- Agents relay content: a watcher that read a web page, a log or a PR can pass injected text on
  under its own valid signature. One compromised or confused agent reaches everyone through
  `@all`.

Goal: limit what another agent can make an agent do, per receiver, with the owner in control.
No layer makes prompt injection impossible: the model still reads the text it declines. The aim
is to shrink who can reach which agent, and what a message can trigger.

## Ideas, cheapest first

### 1. Per-receiver trust policy (enforced in code)

A local file per agent identity, kept by the owner, for example
`~/.config/agent-bus/<agent-id>/policy.json`, mapping senders to roles:

| Role | What its messages may do |
|---|---|
| `owner` | The owner's monitor identity. May direct work. |
| `requester` | May ask for work in this agent's project. |
| `peer` | Information only: answers, status, context. Never triggers actions. The default for every agent not listed. |
| `muted` | Held; not shown to the model unless the owner asks. |

- The policy lives on the receiving side, not in the directory: the directory proves identity,
  and the CA or busd should not decide what an agent obeys.
- The adapter attaches `sender_role` to each message in `inbox` and `wait_for_messages`.
- The MCP instructions key on it: only `owner` and `requester` requests may lead to work; a
  request from a `peer` is answered with "not accepted from you" or reported to the owner.
- Open: `requester` per sender only, or per sender and project/topic?

**An example policy file (proposed).** Not built; the format is a sketch:

```json
{
  "default": "peer",
  "senders": {
    "monitor@alice": "owner",
    "api-client@alice": "requester",
    "lib-x@bob": "peer",
    "noisy-bot@bob": "muted"
  }
}
```

A message from `api-client@alice` could then lead to work in this agent's project, one from
`lib-x@bob` is information only, and one from `noisy-bot@bob` is held.

### 2. Owner approval for real actions

A `requester` asking for anything beyond reading and answering (code changes, commits, anything
outward-facing) makes the agent ask the owner through the monitor and wait. The owner's monitor
becomes the single source of authority. The requirement is that the approval rule is explicit on the
bus and visible in the monitor, not only written into an agent's own instruction files.

### 3. Taint marking

An agent whose message carries content from outside the bus (web, files, tool output, another
agent's message) marks it `derived`. Receivers treat `derived` messages as `peer`, whatever the
sender's role. This relies on the sender being honest, so it only limits accidental relaying,
not a compromised agent.

### 4. Containing damage

- Hooks inject only `targets_me` messages, and only from `owner` and `requester`; everything else
  waits for an explicit `inbox` call.
- Keep size limits; links and paths in `refs`, not inline bodies; first contacts held until
  accepted (holds are not built, [delivery](delivery.md)).
- `@all` from a `peer` reaches others only as information.
- Each agent's own harness permissions (Claude Code permission mode, sandbox) stay the real
  backstop.

## Already in place

- Signatures, TOFU pins and revocation by agent removal ([identity and trust](identity-and-trust.md)).
  First-contact holds are **not** built: today a changed key is rejected and alerted.
- Untrusted-data framing, loop guard, rate limits, secret scan ([safety](safety.md)).
- Stale messages (older than a day) are context only and never announced by hooks.

## Open decisions

| Question | Options | Needed before |
|---|---|---|
| Role granularity | Per sender only, or per sender and project or topic | Step 2 of the plan |
| Where the policy lives | A file per identity (idea 1), or in the monitor | Step 2 |
| Default role for an unlisted agent | `peer` (information only), or `requester` | Step 2 |
| Who may edit the policy | The owner by hand, or through the monitor | Step 3 |
| How `derived` is set | The sender's honesty only, or detected by the adapter | Step 4 |

## Plan

1. Settle the role model with the owner (the open question in idea 1).
2. Build 1 and the hook part of 4: policy file, `sender_role`, instructions, hook filter.
3. Then 2 (owner approval via the monitor).
4. 3 only if relaying turns out to be a real problem.
