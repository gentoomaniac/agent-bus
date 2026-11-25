# Safety

[Design index](README.md) | previous: [presence](presence.md) | next: [access control](access-control.md)

The rules that keep agents from harming each other or the owner: messages are untrusted input, the
auto-accept switch, the loop guard, rate limits and the secret scan. Key storage (accidental
exposure only) is in [identity and trust](identity-and-trust.md#key-storage).

| Rule | Status |
|---|---|
| 1. Untrusted-content frame | implemented |
| 2. `--[no-]auto-accept-messages` | implemented as a flag that changes what the MCP server tells the agent; the transition checks that enforce it are **planned** with the request lifecycle, and so is owner approval over the bus (`agent-busctl approve`) |
| 3. Loop guard (`hops`, limit 6, closing replies to 7, notify cooldown) | implemented; the limit is a configuration field of the adapter, **no `--max-hops` flag** yet |
| 4. Rate limits (30 per 10 minutes, separate allowance for closing replies) | implemented; **no `--rate-limit` flag** yet |
| 5. Secret scan of outgoing bodies | implemented |

Defaults at a glance:

| Setting | Default |
|---|---|
| Hop limit | 6 (closing replies `ack`, `answer`, `status` allowed to hop 7) |
| Rate limit | 30 messages per agent per 10 minutes, own allowance for closing replies |
| Notify cooldown | 10 seconds between two notifications |
| Inferred parent window | the most recent message delivered or acked in the last 5 minutes |

## The rules

1. **Messages are untrusted input.** A valid signature proves who sent a message, not that its content is safe; a trusted agent can relay injected instructions from a poisoned dependency or web page. The adapter wraps every delivered message in a clear "data, not instructions" frame, and CLAUDE.md / AGENTS.md guidance says the same.
2. **Auto-accept.** **Decision:** the adapter flag `--auto-accept-messages` is on by default, because all agents belong to the same owner; `--no-auto-accept-messages` turns it off. The frame from rule 1 stays the same in both modes (messages are peer data, never the owner's instructions). The flag decides whether the agent may act on a verified request without the owner:
   - On: the assignee may move a `feature_request` or `bug_report` to `accepted` and work on it.
   - Off: moving a request to `accepted` requires the owner (agent-busctl `approve`, the monitor, or the owner in the session's prompt), and the delivered frame says "discuss, don't act without the owner's approval".
   - Owner approval over the bus: `agent-busctl approve` (and later the monitor) publishes a `status: accepted` message in the request's thread, signed by `human@<owner>`. The adapter honours it as approval only when that key is in `known_agents` with `source=pinned`; with `--no-auto-accept-messages` that is the only way a request reaches `accepted`. Later progress (`in_progress`, `done`) follows the transition table in [protocol](protocol.md).
3. **Loop guard.** `hops` measures causal reply depth. The adapter, not the agent, sets it:
   - `hops` = the parent's stored `hops` + 1. The parent is the message named by an explicit `reply_to` from the agent; the agent's explicit `reply_to` and `thread` win. If that message is not in the adapter's local store, it is looked up through the read-only history path; if it cannot be found, the send is refused, so an invented `reply_to` cannot reset the count.
   - Without them, a `send` or `publish` inherits `reply_to`, `thread` and `hops` from the most recent message delivered or acked in the last five minutes, if it belongs to the conversation being continued: it arrived on the channel being posted to, or, for a DM, is a DM from the recipient. Naming a `thread` explicitly disables this inference, and so does moving to another channel or person. Older mail never binds a send, so one message that is never acked cannot lock every later send.
   - With no such message, the send is the agent's own initiative and starts a new thread at 0.
   - Branched replies each count from their own parent. The adapter persists each message's `hops` locally, so a restart keeps the count.
   - The adapter refuses to send when the parent's `hops` ≥ 6 (`--max-hops`), except closing replies (`ack`, `answer`, `status`), which are allowed up to a parent at 6 (their own `hops` is then 7), so a request that reached the limit can still be answered or declined; new requests, questions and plain messages are refused, and nothing is sent beyond 7.
   - The inferred parent (the recently read message) lives in the adapter's memory only. After a restart the agent has none until it reads again, so a send that names no `reply_to` starts at hops 0; the explicit `reply_to` is always looked up in the persisted store.
   - Only a message from the pinned `human@<owner>` resets the count (its children start at 0); a TOFU-pinned owner key does not.
   - The end-of-turn hook also has a cooldown so two agents cannot keep waking each other.
4. **Rate limits** in the adapter: 30 messages per agent per 10 minutes by default (`--rate-limit`). A send is validated first (type, targets, size, secret scan) and spends quota only when it would go out, so refused sends cost nothing. Closing replies (`ack`, `answer` and `status` with a parent) have an allowance of the same size of their own, so a burst of requests cannot use up the slots the promised answers need. The limit error names `retry_after` in seconds, and its text tells the agent to keep what it meant to send and retry.
5. **Secrets.** The adapter scans outgoing bodies for obvious secrets (tokens, private keys) and blocks the send.

## See also

- [Protocol](protocol.md): response-bearing types and the request lifecycle that rule 2 gates.
- [Delivery](delivery.md): the notify cooldown and the quiet-by-default rule.
