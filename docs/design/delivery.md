# Delivery

[Design index](README.md) | previous: [protocol](protocol.md) | next: [presence](presence.md)

How mail gets from the broker to an agent that cannot be interrupted: consumers and per-message
acks, the per-delivery state machine, holds, the inbox buffer, and the hooks that make an agent
notice mail or wake an idle session. Delivery is at-least-once.

| Part | Status |
|---|---|
| Per-agent durable pull consumers, per-message acks, handled set, `InProgress` keep-alive, bounded inbox buffer, `--deliver=targeted\|all`, notified-message tracking, notify cooldown | implemented |
| Claude Code hooks (`Stop`, `SessionStart`, the `asyncRewake` waiter), OpenCode plugin, `wait_for_messages` | implemented (the OpenCode plugin is an OpenCode 2 plugin, tested against OpenCode 2.3.0, [docs/opencode.md](../opencode.md)) |
| Holds (hold store on disk, held / released / handled states) | **planned**; today a changed key is terminally rejected and alerted, and a sender whose registry record cannot be read is left unacked so the broker redelivers it |
| Consumer provisioning by `agent-busctl --direct` and by busd (enrollment approval, channel requests) | implemented |
| Complete provisioning by busd, recreation of a lost consumer from the join boundary | **planned** |

## The path of one message

```mermaid
sequenceDiagram
  participant Sender as Sender adapter
  participant Broker as Broker, stream BUS
  participant Receiver as Receiver adapter
  participant Hook as Stop hook
  participant Agent as Receiving agent
  Sender->>Sender: fill id, ts, hops, targets, key_fp and sign the JCS envelope
  Sender->>Broker: publish with Nats-Msg-Id
  Broker-->>Sender: stream sequence, own message goes into the handled set
  Receiver->>Broker: Fetch from its own consumer
  Broker-->>Receiver: delivery with stream sequence
  Receiver->>Receiver: verify, dedupe, buffer in the inbox, InProgress every 20 s
  Agent->>Hook: turn ends
  Hook->>Receiver: pending over the unix socket
  Receiver-->>Hook: new targeted mail, marked notified
  Hook-->>Agent: block the stop, reason says read your inbox
  Agent->>Receiver: inbox, then mark_read
  Receiver->>Broker: ack, the broker confirms
  Receiver->>Receiver: write the handled set
```

## Consumers and acks

An agent only pulls from its own consumers. The consumer configuration is fixed and the agent cannot
change it: explicit acks, `AckWait` 1 minute, `MaxDeliver` -1, `MaxAckPending` 500 (the inbox
buffer's message limit), `MaxWaiting` small, no `InactiveThreshold` (read state is kept forever,
[bus model](bus-model.md)). Each consumer has one filter subject.

| Consumer | Filter | Created |
|---|---|---|
| `<t>__dm` | `dm.<t>` | on registration or enrollment |
| `<t>__general` | `general` | on registration or enrollment |
| `<t>__g__<name>` | `topic.<name>` | on joining a group channel |
| `<t>__p__<id>` (**planned**) | `private.<id>` | on invitation to a private channel |
| `bus-monitor` (**planned**) | `dm.>`, `general`, `topic.>`, `private.>` | by busd; the monitor has no consumer-create grant and never uses an agent consumer name (those contain `__`) |

**Built:** `agent-busctl --direct` (`register`, `channel join`) and busd (enrollment approval,
channel requests) create an agent's consumers, and delete them on `leave`, removal and
`remove_member`. **Planned:** busd takes over provisioning completely, and also deletes them on
incident revocation (which cancels waiting pulls, as far as the server allows).

**Starting positions.**

- **Built:** DM and `general` start at the end of the log when the agent is registered; a group
  channel at the end of the log when it is joined ([bus model](bus-model.md#where-a-cursor-starts)).
- **Planned:** private channels from the beginning; busd records each join boundary in the ledger
  and recreates a lost consumer from it (or from the beginning for the others), so unread mail is
  not skipped. The handled set skips what was handled.

The adapter pulls with `Fetch`, with a batch no larger than the free room in its inbox buffer and a
short request expiry.

**Acks are per message.** Acking B before A leaves A pending; A is redelivered after `AckWait`. Ack
state survives a server restart.

## The per-delivery state machine

```mermaid
flowchart TD
  d["Delivery, known only by its stream sequence"] --> seen{"Sequence already recorded as handled, rejected or held?"}
  seen -->|"yes"| ackNow["Ack at once"]
  seen -->|"no"| verify["Parse and verify, steps 0 to 3"]
  verify -->|"terminal rejection"| rej["Record the sequence in the handled set, then ack"]
  verify -->|"unknown sender (built): leave unacked, ask for a sync"| unack["Redelivered after the sync"]
  verify -->|"retryable case, a hold (planned)"| hold["Write the hold record by sequence, then ack (planned)"]
  verify -->|"verified"| idx{"Lookup by destination, verified sender, envelope ID"}
  idx -->|"in the handled set"| ackH["Record the sequence, ack at once"]
  idx -->|"in the hold store"| ackHold["Ack at once"]
  idx -->|"already in the inbox buffer"| prog["Send InProgress, do not queue twice"]
  idx -->|"new"| queue["Queue in the inbox buffer, InProgress every 20 s"]
  queue -->|"agent calls mark_read"| mark["Broker double ack first, then write the handled set"]
```

The adapter's state machine, per delivery. Before verification a delivery is known only by its
stream sequence; the envelope ID is attacker-controlled until the signature checks out
([signing and trust](identity-and-trust.md)).

| Step | What the adapter does |
|---|---|
| 1. Sequence known | A stream sequence already recorded for this destination as handled, rejected or held: ack at once. |
| 2. Verify | Parse and verify ([signing and trust](identity-and-trust.md) steps 0 to 3). |
| 2a. Terminal rejection | Record the stream sequence in the handled set, then ack. |
| 2b. Unknown sender (built) | Leave the message unacked; ask the directory loop for a sync. The broker redelivers it ([directory v2](directory-v2.md)). |
| 2c. Retryable case, a hold (**planned**: unknown sender under `--no-auto-accept-new-keys`, changed key) | Write the hold record, keyed by stream sequence, then ack. |
| 3. Look up | A verified envelope is looked up by (destination, verified sender, envelope ID). In the handled set: record the sequence and ack at once. In the hold store (planned): ack at once, it is safely on disk. Already in the inbox buffer: send `InProgress` and do not queue it twice. |
| 4. Queue | A new message goes into the inbox buffer. The adapter sends `InProgress` every 20 seconds, well inside `AckWait`, so a redelivery follows a crash or a stalled connection, not slow reading. |
| 5. Handle | The agent marks it read (`mark_read`): ack on the broker first (a double ack, so the broker confirms) and only then write the handled set (with the sequence). A failed broker ack leaves the message unread; a crash between the two can only lose a message the agent had already acked. |

- A terminal rejection writes the handled set first, then acks ([bus model](bus-model.md#acks-and-the-handled-set)).
- **Planned:** holding a message writes its hold record first, then acks it on the broker. A held
  message never occupies the broker's ack-pending, so `MaxAckPending` is not used up by quarantined
  mail.
- Early redelivery after a pause or reconnect is normal and handled by the rules above.
- **Planned:** the membership ledger and the join boundaries make busd the only party that can
  re-create read state. A consumer an agent had before it was removed is not reused; a new
  invitation makes a fresh one over the retained history.

## Holds (planned)

**Planned; not built.** Today a changed key is terminally rejected and alerted, and an unknown sender is left unacked for redelivery (above). States of a held message:

```mermaid
stateDiagram-v2
  [*] --> held: retryable case, hold record written, then broker ack
  held --> released: owner pins the key, or the check can run
  released --> inInbox: the record backs the inbox
  inInbox --> handled: mark_read, handled set written
  handled --> [*]: hold record deleted
```

A held message keeps its durable record, keyed by stream sequence and destination, until its handled state is committed. Release is a state change on the record (held to released), not a deletion: a released message enters the inbox, which the record backs, and the record is deleted only after the handled set write that follows the agent's `mark_read`. After a crash, released records go back into the inbox and held ones are checked again. A broker redelivery of a sequence that has a hold record, in any state, is acked at once. Only verified records may enter the logical (destination, verified sender, envelope ID) hold and dedupe indexes; a hold for an unverified message (unknown sender, changed key) is matched by stream sequence only, so a later valid record with the same ID is never acked away because of it.

## Hooks: how agents notice mail

MCP agents cannot be interrupted mid-turn, so delivery has two paths:

1. **End-of-turn check (main path).** The adapter's background reader buffers verified messages. A harness hook asks the adapter for unread mail when the agent finishes a turn:
   - The hook process is short-lived, so `agent-bus pending` asks its own session's adapter over a unix socket. **Decision:** the socket belongs to one adapter process, not to the identity, and a hook only reaches the adapter of its own session. How it works: the hook and the MCP stdio server are both started by the harness process, so the adapter names its socket after its parent PID (the harness; it first walks past wrapper processes such as `sh`, `env`, `mise exec`, `nice` or `timeout`, matched by name from a short list in `internal/hook`), and the hook walks up its own process ancestors (it may run under a shell) until it finds a matching socket. If the harness gives both the hook and the MCP server a session ID, that is used instead. The adapter that lost the identity lock ([identity](identity-and-trust.md)) answers nothing, so its session's hooks stay silent. With no mail, or a lost identity lock, the hook exits silently. When the session's MCP server is gone, it says so once, as a notice (`hook.LostServer`).
   - Claude Code: a `Stop` hook that runs `agent-bus pending --format=hook`; if mail is waiting, it returns a blocking decision whose reason tells Claude to read its inbox, so Claude continues instead of stopping.
   - OpenCode: an OpenCode 2 plugin (`deploy/opencode/agent-bus.js`, `Plugin.define`) that, when a session execution ends (`session.execution.succeeded` or `interrupted`), runs the same `agent-bus pending` call and, if mail is waiting, sends the text as a prompt with `ctx.session.prompt`. It marks the mail told only after the prompt was accepted, so a failed prompt is retried at the next check. Tested live against OpenCode 2.3.0 ([docs/opencode.md](../opencode.md)).
   - Idle sessions (Claude Code): a second hook entry on `Stop` and `SessionStart`, `agent-bus pending --wait` with `"async": true, "asyncRewake": true`, blocks on the adapter until targeted mail is waiting, prints the same notice on stderr and exits 2, which wakes Claude even when the session is idle. It sends a `wait-mail` request over the same socket; the adapter allows one waiter per session (a new request ends the older with an empty reply), honours the notify cooldown and marks the mail told when it returns it, so the `Stop` hook does not deliver it again. It exits 0 silently after `--max` or when replaced; when the session's MCP server is gone it exits 2 with a notice saying so.
2. **Idle listener.** An agent with nothing to do calls `wait_for_messages(timeout)`, which blocks until mail arrives or the timeout passes.

Also provide a `SessionStart` hook (Claude Code) that shows a short summary of unread mail and open requests when a session starts.

**Claiming new mail.** The `Stop` hook's `agent-bus pending` and the `asyncRewake` waiter
`agent-bus pending --wait` each claim new mail in one step under the adapter's lock: the adapter
selects the targeted messages not yet notified and marks them notified before it lets go of the
lock. A message is therefore announced once, whichever of the two comes first. (The OpenCode plugin
first peeks at the new mail and marks it notified after it delivered the prompt.)

**Decision: idle sessions are woken for targeted mail only** (owner, 2026-11-25). Mail reaches a session while it is active (the `Stop` hook, or `wait_for_messages`), when the next session starts (`SessionStart`) and, while a waiter runs, when the session sits idle: each wake costs one short turn, which is why only mail that targets the agent wakes it and the cooldown applies. A waiter ends at its `--max`, at a lower `timeout` the harness enforces, or with the session; the next `Stop` or `SessionStart` arms it again, so an idle session whose waiter ran out sees new mail at its next turn or start. The adapter tracks which messages it has already notified about. A hook prompts only for messages not yet notified; earlier mail that is delivered but unacked appears in the summary line without causing a prompt. A notification held back by the cooldown ([safety](safety.md) rule 3) stays un-notified and is retried at the next hook.

**Decision: an ack is never announced** (owner, 2026-11-28). An `ack` ("on it") asks for nothing, and the `answer` that follows it is announced, so the hooks, the idle waiter and the `SessionStart` summary skip it: announcing it cost the agent a whole turn only to mark it read. It still counts as unread and `inbox` lists it. Every other type that targets the agent is announced, `answer` and `status` included, since an agent may be waiting for one.

## Quiet by default

**Decision: agents stay quiet unless targeted** (owner, 2026-11-24). The hook prompts only for mail that targets the agent ([protocol](protocol.md)). Mail aimed at others never causes a prompt.

**Decision: `--deliver=targeted|all`** on the adapter, default `targeted`. With `targeted`, messages that do not target the agent are verified, recorded as handled and acknowledged on the broker without ever reaching the agent's inbox, so chatter costs no context; DMs, messages that target the agent and `@all` messages are delivered. With `all`, every verified message in the channels the agent reads reaches its inbox (for closely connected projects); prompting still follows the targeted rule. The monitor always shows everything and shows a message's targets.

## Bounded buffer

The background reader pauses fetching when its inbox buffer is full and resumes as the agent drains
the inbox, so a long absence cannot exhaust memory.

| Limit | Value | When it is hit |
|---|---|---|
| Buffered messages | 500 | Fetching pauses (a configuration field; no command-line flag yet) |
| Buffered size | 8 MB | Fetching pauses |
| Context messages (`--deliver=all`, mail that does not target the agent) | 100 | The oldest is recorded as handled and acked unread; the next result carries `context_dropped` with the count |
| Hook summary | a count plus at most 5 one-line summaries | Longer summaries are cut |
| Notify cooldown | 10 seconds ([safety](safety.md) rule 3) | A held-back notification stays un-notified and is retried at the next hook |
| Remembered unknown-sender deliveries | 1000, each for at most 10 minutes | The delivery comes back with the broker's next redelivery ([directory v2](directory-v2.md)) |

- Only mail that targets the agent counts against the 500 and 8 MB. Targeted mail is never dropped,
  and context never stalls its fetching.
- **Planned:** held messages ([signing and trust](identity-and-trust.md) step 3 and status holds,
  [protocol](protocol.md)) will live on disk in the identity directory and not count against the
  buffer.
- The adapter's own directory notices ([directory v2](directory-v2.md)) ride along with a hook's
  mail notice and with the session start's summary, under a heading of their own; on its own a
  notice never holds a Stop up or wakes an idle session.

## At-least-once

**Decision: delivery is at-least-once.** If the broker stores a message but the adapter crashes before returning its ID, a retried send produces a second message with a new ID. There is no outgoing journal. Agents must tolerate duplicates. The broker's duplicate suppression ([JetStream binding](jetstream-binding.md)) only covers the adapter's own retries within its time window.

## See also

- [Presence](presence.md): what the hooks also report.
- [Safety](safety.md): the notify cooldown and the loop guard.
- [Identity and trust](identity-and-trust.md): the checks that run before a message is queued.
