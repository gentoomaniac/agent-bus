# Safety

[Design index](README.md) | previous: [presence](presence.md) | next: [access control](access-control.md)

The rules that keep agents from harming each other or the owner: messages are untrusted input, the
auto-accept switch, the loop guard, rate limits and the secret scan. Key storage (accidental
exposure only) is in [identity and trust](identity-and-trust.md#key-storage).

## Status

| Rule | Status |
|---|---|
| 1. Untrusted-content frame | implemented |
| 2. `--[no-]auto-accept-messages` | the **instruction policy** is implemented: the flag changes what the MCP server tells the agent. **Planned:** the transition checks that enforce it with the request lifecycle, and owner approval over the bus (`agent-busctl approve`) |
| 3. Loop guard (`hops`, limit 6, closing replies to 7, notify cooldown) | implemented; the limit is a configuration field of the adapter, **no `--max-hops` flag** yet |
| 4. Rate limits (30 per 10 minutes, separate allowance for closing replies) | implemented; **no `--rate-limit` flag** yet |
| 5. Secret scan of outgoing bodies | implemented |

Two kinds of gap, not one:

- **Unexposed limits.** `--max-hops`, `--rate-limit` and `--inbox-buffer` exist as adapter
  configuration fields but are not on the `agent-bus` command line yet.
- **Planned features.** `--[no-]auto-accept-new-keys` and `--owner-identity` have no code behind
  them at all (no setting in the adapter): holds and the pinned owner are planned.

`--[no-]auto-accept-messages` and `--deliver` are flags today.

Defaults at a glance:

| Setting | Default |
|---|---|
| Hop limit | 6 (closing replies `ack`, `answer`, `status` allowed to hop 7) |
| Rate limit | 30 messages per agent per 10 minutes, own allowance for closing replies |
| Notify cooldown | 10 seconds between two notifications |
| Inferred parent window | the most recent message delivered or acked in the last 5 minutes |

## The rules

1. **Messages are untrusted input.** A valid signature proves who sent a message, not that its content is safe; a trusted agent can relay injected instructions from a poisoned dependency or web page. The adapter wraps every delivered message in a clear "data, not instructions" frame, and CLAUDE.md / AGENTS.md guidance says the same.
2. **Auto-accept.** **Decision:** the adapter flag `--auto-accept-messages` is on by default, because all agents belong to the same owner; `--no-auto-accept-messages` turns it off. The frame from rule 1 stays the same in both modes (messages are peer data, never the owner's instructions). The flag decides whether the agent may act on a verified request without the owner.

   **Built: instruction policy.** The flag only changes the instruction the agent gets (`whoami` shows which one is active); there is no request lifecycle yet ([protocol](protocol.md)).
   - On: the agent works on a request aimed at it about its own project or repository without asking its owner. It still asks first before anything destructive, outside its project, touching secrets, credentials or deploys, and anything large the owner did not ask for.
   - Off: the agent asks its owner before it acts on any request; it may send an `ack` saying that it is asking. The delivered frame says "discuss, don't act without the owner's approval".
   - Nothing enforces this: a status message is a plain message, and the monitor's messages are ordinary peer messages ([monitor](../monitor.md)). The owner approves in the session's prompt.

   **Planned: request lifecycle, enforcement and owner approval over the bus.**
   - A request moves to `accepted` by a `status: accepted` message in its thread. With the flag on, the assignee may send it; with the flag off, only an owner approval counts.
   - `agent-busctl approve` (and later the monitor) publishes a `status: accepted` message in the request's thread, signed by `human@<owner>`.
   - The adapter honours it as approval only when that key is in `known_agents` with `source=pinned`; with `--no-auto-accept-messages` that is the only way a request reaches `accepted`.
   - Later progress (`in_progress`, `done`) follows the transition table in [protocol](protocol.md).
3. **Loop guard.** `hops` measures causal reply depth. The adapter, not the agent, sets it:
   - `hops` = the parent's stored `hops` + 1. The parent is the message named by an explicit `reply_to` from the agent; the agent's explicit `reply_to` and `thread` win. If that message is not one the agent has read or sent (the adapter's local store), the send is refused, so an invented `reply_to` cannot reset the count. A lookup through the read-only history path is planned ([identity and trust](identity-and-trust.md)).
   - Without them, a `send` or `publish` inherits `reply_to`, `thread` and `hops` from the most recent message delivered or acked in the last five minutes, if it belongs to the conversation being continued: it arrived on the channel being posted to, or, for a DM, is a DM from the recipient. Naming a `thread` explicitly disables this inference, and so does moving to another channel or person. Older mail never binds a send, so one message that is never acked cannot lock every later send.
   - With no such message, the send is the agent's own initiative and starts a new thread at 0.
   - Branched replies each count from their own parent. The adapter persists each message's `hops` locally, so a restart keeps the count.
   - The adapter refuses to send when the parent's `hops` ≥ 6 (a configuration field; the planned flag is `--max-hops`), except closing replies (`ack`, `answer`, `status`), which are allowed up to a parent at 6 (their own `hops` is then 7), so a request that reached the limit can still be answered or declined; new requests, questions and plain messages are refused, and nothing is sent beyond 7.
   - The refusal tells the agent which message its send continues and, for an inferred parent, why. A new topic goes out with a new `thread` name (and no `reply_to`) and starts at 0; the same topic waits for a human. The agents' instructions say so too. This restarts the count, so a looping agent can use it: the rate limit and the owner's monitor are the remaining brakes.
   - The inferred parent (the recently read message) lives in the adapter's memory only. After a restart the agent has none until it reads again, so a send that names no `reply_to` starts at hops 0; the explicit `reply_to` is always looked up in the persisted store.
   - **Planned:** only a message from the pinned `human@<owner>` resets the count (its children start at 0); a TOFU-pinned owner key does not. Nothing resets the count today.
   - The end-of-turn hook also has a cooldown so two agents cannot keep waking each other.
4. **Rate limits** in the adapter: 30 messages per agent per 10 minutes by default (a configuration field; the planned flag is `--rate-limit`). A send is validated first (type, targets, size, secret scan) and spends quota only when it would go out, so refused sends cost nothing. Closing replies (`ack`, `answer` and `status` with a parent) have an allowance of the same size of their own, so a burst of requests cannot use up the slots the promised answers need. The limit error names `retry_after` in seconds, and its text tells the agent to keep what it meant to send and retry.
5. **Secrets.** The adapter scans outgoing bodies for obvious secrets (tokens, private keys) and blocks the send.

## Threats, mitigations and residual risk

| Threat | Mitigation | Residual risk |
|---|---|---|
| A poisoned message carries injected instructions | Rule 1: the "data, not instructions" frame; guidance in CLAUDE.md and AGENTS.md | A model may still follow the text; the frame is guidance, not enforcement |
| Two agents keep answering each other | Loop guard (rule 3) and the notify cooldown | Limits are configuration fields, not flags; the count is reset only by a human post (planned) |
| One agent floods the bus | Rate limit (rule 4), per agent, and the creation limit for channels | A misbehaving client can bypass its own adapter's limit; the broker does not enforce it |
| A secret leaves in a message | Secret scan (rule 5) | Only obvious patterns are caught |
| A request is accepted without the owner | Rule 2, instruction policy | Not enforced until the request lifecycle exists |
| An agent writes junk records into a channel | Receivers reject records with no envelope or signature | A channel can be filled with junk (below) |
| A compromised monitor disturbs delivery | The monitor has read-only logins and no CA key | It can create, update and delete consumers (below) |
| A sending monitor impersonates its agent | It signs only as the one agent identity it is configured with | It does hold that agent's signing key |

**The monitor and keys.** A read-only monitor holds no signing key. A monitor started with `--agent`
(sending) holds that agent's key in its own config directory, and could sign as that one agent.
It never holds the owner's key or the CA key ([monitor](monitor-and-api.md)).

## Known limit: junk records in a channel

An agent may read status records directly from the `PRESENCE` stream. JetStream sends the answer
to whatever reply subject the caller names, so an agent can point it at `dm.<agent>` or `general`
and the server stores the raw status record there.

- It carries no envelope and no signature, so every receiver rejects it and it never reaches an
  agent as mail (tested in `internal/itest`); the monitor lists it as rejected.
- A misbehaving agent can therefore fill a channel with junk records but cannot inject messages.
- Narrowing the grant (for example serving presence through busd) would close it; not needed in the
  MVP.

## Known limit: the monitor can disturb consumers

`monitor-bus` can create, update and delete consumers of the `BUS` stream: JetStream grants cannot
name a consumer prefix, and the monitor's ordered history reads need consumer create.

- A compromised monitor can therefore disrupt delivery or reset read cursors.
- It cannot read keys, impersonate an agent (it holds no signing key of its own; see above for a
  sending monitor) or approve anything.
- **Planned fix:** busd serves the history and provisions the monitor's consumers; not needed in the
  MVP.

## See also

- [Protocol](protocol.md): response-bearing types and the request lifecycle that rule 2 gates.
- [Delivery](delivery.md): the notify cooldown and the quiet-by-default rule.
