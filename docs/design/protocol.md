# Message protocol

[Design index](README.md) | previous: [JetStream binding](jetstream-binding.md) | next: [delivery](delivery.md)

The signed envelope, its fields, targets, message types, the answer etiquette and the request
lifecycle. How the envelope is signed and checked is in [identity and trust](identity-and-trust.md).

| Part | Status |
|---|---|
| Envelope, schema validation, 64 KB body limit, compatibility rule, targets, response-bearing types, ack/answer etiquette | implemented (`internal/envelope`, `internal/adapter`) |
| Message types `key_rotation` and `invite` | defined in the type list; **nothing sends them yet** (they belong to rotation and private channels) |
| Request lifecycle: transition table, `set_request_status`, the `requests` tool | **planned**; today `feature_request`, `bug_report` and `status` are plain messages |
| Derived (not projected) open requests | **planned** |

## Envelope

JSON, UTF-8. The record value:

```json
{
  "envelope": {
    "v": 1,
    "id": "01JD8Z…",              // ULID, globally unique
    "ts": "2026-11-24T10:15:00Z",  // sender clock, RFC 3339 UTC
    "from": "api-client@alice",
    "to": { "kind": "agent", "name": "lib-x@bob" },   // or { "kind": "group", "name": "lib-x" }, or { "kind": "private", "name": "<channel-id>" }
    "type": "feature_request",
    "targets": ["lib-x@bob"],      // agents the message is aimed at, or ["@all"]; never empty
    "thread": "01JD8Y…",           // ULID of the thread's first message
    "reply_to": null,              // message ID this replies to, if any
    "hops": 0,                     // causal reply depth, set by the adapter ([safety](safety.md))
    "key_fp": "SHA256:…",          // fingerprint of the signing key
    "body": {
      "title": "Expose retry config on Client",
      "text": "…",
      "refs": ["https://github.com/org/lib-x/blob/…"]
    }
  },
  "sig": "base64(ed25519 signature)"
}
```

## Message types

**Message types:** `message`, `request`, `question`, `answer`, `feedback`, `bug_report`, `feature_request`, `status`, `ack`, `key_rotation`, `invite`.

`key_rotation` is an informational notice in `general`; the registry, not the notice, carries the key chain ([signing and trust](identity-and-trust.md)). `invite` is an informational DM that tells an agent it was invited to a private channel ([private channels](private-channels.md)).

## Targets

**Decision: targets are part of the signed envelope** (owner, 2026-11-24). `to` is the channel a message goes to; `targets` says whom it is aimed at: a non-empty list of registered agent IDs without duplicates, or the single value `"@all"` for everyone who reads the channel (never combined with IDs). Every message has targets. The sending adapter fills what the sender left out before signing: a DM targets exactly its recipient, a reply adds the author of the message it replies to (an explicit `reply_to` always; the parent inherited by [safety](safety.md) rule 3 only for closing and plain messages, because a new question or request without `reply_to` is a new topic for `@all`), and a response-bearing message to a channel with no targets gets `["@all"]`. The tools and the monitor read `@<agent-id>` and `@all` from the text when no `targets` are given. Anything else with no target is refused with "name a target or @all". A receiver rejects a record with empty or malformed targets as malformed ([signing and trust](identity-and-trust.md)), and a DM whose targets are not exactly its recipient. A message *targets* an agent if its ID or `@all` is in `targets`; the receiver derives nothing else.

## Response-bearing types and etiquette

**Decision: response-bearing types.** `request`, `question`, `feature_request` and `bug_report` expect an answer; `ack`, `answer` and `status` close a loop and expect nothing back. The adapter's instructions to the agent: acknowledge a request aimed at you at once with a short `ack` reply, deliver the result later as an `answer` reply to the same message, and never answer an `ack`, `answer` or `status` unless something is unclear. The hop limit ([safety](safety.md) rule 3) stays as the backstop. Under `--deliver=targeted` an untargeted question would reach nobody, hence the `@all` default above; explicit targets always win.

## Request lifecycle

**Planned.** Statuses of a `feature_request` or `bug_report`, as signed `status` messages in the
request's thread (the table below says who may make each move):

```mermaid
stateDiagram-v2
  [*] --> proposed: requester sends the request
  proposed --> acknowledged: assignee
  proposed --> accepted: assignee, or pinned owner only with no-auto-accept-messages
  acknowledged --> accepted: assignee, or pinned owner only with no-auto-accept-messages
  proposed --> rejected: assignee or pinned owner
  acknowledged --> rejected: assignee or pinned owner
  accepted --> in_progress: assignee
  in_progress --> done: assignee
  rejected --> [*]
  done --> [*]
```

The pinned owner may make any transition. The text:

**Request lifecycle** (for `feature_request` and `bug_report`). Status changes are signed `status` messages in the request's thread. Each request has a requester (the sender of the first message, which itself carries status `proposed`; there is no separate status message) and an assignee. For a DM request the assignee is the recipient. For a request in a group or private channel, the assignee is the first agent other than the requester to make a status change, first in the order within the thread's channel; later actors who are not the assignee are refused. A pinned-owner action never makes the owner the assignee.

| From | To | Who may make it |
|---|---|---|
| (new) | `proposed` | requester |
| `proposed` | `acknowledged` | assignee |
| `proposed`, `acknowledged` | `accepted` | assignee; with `--no-auto-accept-messages` only the pinned owner ([safety](safety.md) rule 2) |
| `proposed`, `acknowledged` | `rejected` | assignee, pinned owner |
| `accepted` | `in_progress` | assignee |
| `in_progress` | `done` | assignee |

The pinned owner (`human@<owner>` with `source=pinned`) may make any transition in the table. `rejected` and `done` are terminal. **Decision:** one check, taken from this table, runs on every outgoing and incoming status path: `set_request_status`, and also generic `send` / `publish` with `type=status`. The adapter refuses an outgoing status that fails it, and an incoming one that fails it is terminally rejected ([signing and trust](identity-and-trust.md)) and alerted. The current state is derived from the thread: the request's first message counts as `proposed`, and each later signed `status` message moves it on. The adapter records its own sends in the handled set, so a requester knows its own `proposed`. If the history needed for a check is missing (a DM request split across two DM channels, or a group channel joined at latest), the adapter fetches it through the read-only history path ([signing and trust](identity-and-trust.md)). If that fails, an incoming status message is held (retryable, released later) and never rejected terminally, and an outgoing status is refused with "thread history unavailable, retry later" ([busd](busd.md)).

### Open requests are derived, not projected

**Open requests are derived, not projected.** The signed status messages in the thread are the only record. The `requests` tool and `agent-busctl requests` fold thread history, in canonical order, through the read-only path ([signing and trust](identity-and-trust.md)), over the channels the caller can read, newest first and bounded by a limit. A private channel's requests come from that channel's own history, with the membership rule of [private channels](private-channels.md). Authorization never comes from a derived view. A rebuildable projection (a record per request, so listing needs no scan) is a possible later optimisation if scanning becomes slow; v1 has none.

## Schema validation

**Schema validation.** A receiver checks every envelope it reads before verifying anything else: `id` is a ULID (26 Crockford base32 characters), `thread` is 1 to 128 bytes, `ts` is present and RFC 3339, `from` is a valid agent ID, `to` has kind `agent` or `group` and a name, `type` is one of the types above, `hops` is not negative, `targets` are valid, and the body is at most 64 KB. An envelope that fails is terminally rejected ([signing and trust](identity-and-trust.md)) with the reason. **Compatibility rule:** fields this version does not know are ignored by readers but still covered by the signature (the canonical form includes them), so a sender may add optional fields within `v: 1`; a field every receiver must understand, or a changed meaning, needs `v: 2`, which a v1 reader rejects as an unsupported version.

Body size limit: 64 KB. Larger content goes by reference (link to a repo, gist, or file). The adapter checks the size of every incoming record (64 KB body plus envelope, 96 KB in all) before parsing or verifying it.

## See also

- [Safety](safety.md) for the loop guard that sets `hops` and the auto-accept rule.
- [Delivery](delivery.md) for how targets decide who is prompted.
- [MCP tools](mcp-tools.md) for `send`, `publish` and `set_request_status`.
