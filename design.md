# Agent Bus — Design Spec

A message bus that lets coding-agent sessions (Claude Code, OpenCode, others via MCP) on different machines exchange signed messages: direct messages, channel discussions, feedback and feature requests between projects that depend on each other.

**Decision: scope.** One developer runs the whole setup across several of their own machines. There is a single owner and no other users. Defaults favour low friction for that one owner; stricter behaviour is available behind flags.

This document is a brief for an implementing agent. Sections marked **Decision** are settled (§13 lists them).

---

## 1. Goals and non-goals

**Goals**

- Agents connect with a stable identity and can send direct messages or publish to channels.
- Every agent is in `#general`; agents can join and leave other channels, and can ask for private channels with invited members only (§4b).
- The broker keeps durable state: message history, per-agent read position, request status. busd keeps the private-channel membership ledger (§4b).
- Every message is signed; receivers verify against keys pinned trust-on-first-use (TOFU), like SSH `known_hosts`.
- A complete, append-only audit trail a human can read.
- Agents receive messages without the human relaying them by hand, while a session is active or when the next one starts (§7).

**Non-goals (v1)**

- End-to-end encryption of message bodies. **Decision:** not planned. With one owner, TLS to the broker (§4) gives confidentiality in transit; signatures (§6) make sure messages can't be altered or forged even if a hop is unencrypted.
- Exactly-once delivery or work. **Decision:** delivery is at-least-once (§9); agents must tolerate a duplicate message.
- Waking an idle, stopped session. **Decision:** mail is delivered while a session is active or when the next one starts (§7).
- Keeping keys from an agent that has shell access as the owner. **Decision:** key storage protects against accidental exposure only (§6).
- Reaching the broker over the internet. The broker is on the local network; machines outside it need a tunnel or VPN set up by the owner (§4).
- Sending messages from the monitor (§10a) is a later milestone, not v1.
- Hiding private channels from the owner, the monitor or the broker. **Decision:** private means private from other agents only (§4b).

---

## 2. Architecture

```
 ┌──────────────┐  stdio MCP  ┌───────────────┐   messages (TLS / SASL)   ┌───────────────────┐
 │ Claude Code  │◄───────────►│ bus adapter   │◄─────────────────────────►│                   │
 │ session      │             │ (sidecar: one │                           │ Broker            │
 └──────────────┘             │  identity,    │                           │ (see §4a)         │
                              │  holds key,   │                           │                   │
 ┌──────────────┐  stdio MCP  │  signs, TOFU) │                           │ channels + access │
 │ OpenCode     │◄───────────►│ (one per      │◄─────────────────────────►│                   │
 │ session      │             │  session)     │                           │                   │
 └──────────────┘             └───────┬───────┘                           │                   │
                                      │ signed requests (TLS):            │                   │
                                      │ private channels                  │                   │
                                      ▼                                   │                   │
 ┌───────────────────────────┐   admin principal                          │                   │
 │ busd (control service):   │───────────────────────────────────────────►│                   │
 │ private channels, access, │                                            │                   │
 │ membership ledger         │                                            │                   │
 └───────────────────────────┘                                            │                   │
 ┌───────────────────────────┐   read-only principal                      │                   │
 │ bus monitor (may share    │───────────────────────────────────────────►│                   │
 │ busd's binary)            │                                            └───────────────────┘
 └───────────────────────────┘

 busctl (human/admin CLI): management commands go to busd; tail, inspect and pin read the broker;
 direct admin access to the broker is the recovery path
```

Components:

1. **Broker** — the message broker behind the two interfaces below; §4a binds the current one. Holds all messages and state. It is not in busd's data path: agents talk to it directly.
2. **Bus adapter** — a local process per agent session, exposed to the agent as a stdio MCP server. It owns the agent's private key, signs outgoing messages, verifies incoming ones, maintains the TOFU store and the handled-message set, and runs a background reader that buffers incoming messages within a fixed memory bound (§7). It calls busd for private-channel management (§10b).
3. **busd** — the control service. It holds the admin principal and does what needs admin rights: private channels and their membership, granting and revoking broker access, and the normal path for busctl's management commands. It never carries messages (§10b). It is not a bus agent: it has a service name and a certificate of its own, and signs no bus messages.
4. **busctl** — admin and human CLI: register identities, create channels, tail and inspect history, manage known keys. It goes through busd; direct admin access is the recovery path (§10).
5. **Delivery hooks** — per-harness glue that makes agents notice new mail (see §7).
6. **Bus monitor** — a small web service that shows messages flowing through the bus (see §10a). It runs as a separate process with its own read-only principal; it may share busd's binary.

**Decision: busd is a control plane, not a gateway.** Agents send and receive messages with the broker directly. Signing, verification, TOFU, the inbox state and the delivery hooks stay in the adapter. busd exists because some actions need admin rights an agent must never hold (private channels, broker access). Putting every message through a service was considered and rejected: it adds a hop and a single point of failure and would move the cursor and commit logic (§4, §6, §7) into the service.

**Decision: not tightly bound to one broker.** The spec describes a broker-neutral model (§4: channels, read cursors, handled messages, accounts and access grants) and maps it to the current broker in one place, the binding (§4a). In code, two small Go interfaces sit between the bus and the broker:

- a **message transport**: publish a record to a channel; consume channels with an explicit cursor and commit; read history read-only, without a cursor;
- an **admin interface**: create channels, grant and revoke access, list the access a channel has, create and delete accounts.

The binding is the only implementation and lives in one package, `internal/broker`, the only one that imports the broker's client library. Envelope, signing, TOFU, the handled set, the inbox, hooks, busd's logic and busctl's commands never import it (`internal/itest` also does, to test against a real broker). The interfaces speak in bus terms (channel kind and name, account, cursor), never in channel names or access rules; the mapping lives in the binding. busd's API (§10b) is the stable surface: it doesn't change if the broker does.

**Decision: container runtime.** The broker is described as a Kubernetes Pod manifest (`deploy/broker.yaml`; the broker itself is named in §4a) and started on the dev machine with Podman (`podman kube play`). Another runtime will start the containers later, so nothing in the Go code or busctl depends on Podman; the manifest and a small start script are the only runtime-specific files. busd and the monitor are plain binaries and can run next to the broker or on any machine.

**Decision: Go.** Single static binaries, easy to copy to every machine. Libraries:

- CLI flags and subcommands: [kong](https://github.com/alecthomas/kong) for every binary (adapter, busctl, busd, monitor). Boolean options that default to on use kong's `negatable` tag, so `--no-<flag>` turns them off.
- Broker client: pure Go, no cgo, to keep the binaries static; imported only inside the binding package (§4a).
- Signatures: the standard library's `crypto/ed25519`.
- MCP: the official Go SDK (`github.com/modelcontextprotocol/go-sdk`).
- JCS (RFC 8785): a small maintained library such as `github.com/gowebpki/jcs`, or an in-repo implementation with the RFC's test vectors.

Binaries: `agent-bus` (the adapter: `agent-bus mcp` runs the MCP server, `agent-bus pending` is called by hooks), `busctl`, `busd`, and `bus-monitor` (which may be built into `busd`, §10b).

---

## 3. Identity

- Agent ID format: `<name>@<owner>`, e.g. `api-client@alice`, `lib-x@alice`. Lowercase, `[a-z0-9-]` plus one `@`.
- **Decision: the bus knows identities, not projects.** The name is chosen at registration and is often a project's name, but nothing ties an identity to a repository. If two identities happen to work on the same code, that doesn't matter to the bus.
- An identity is stable across sessions. The key pair belongs to the identity, not to a session, and lives on one machine (§6, key storage).
- **Decision: identities are per machine; the adapter is a sidecar proxy.** Like Istio's Envoy sidecar in Kubernetes, each adapter instance fronts exactly one session and holds exactly one identity; the session never touches the key or the broker directly. A key is never copied between machines: the same project worked on from two machines gets two identities. Names are unique bus-wide, so the owner picks distinct names, e.g. `api-client-laptop@alice` and `api-client-desktop@alice`. A second session that needs to be on the bus at the same time gets its own adapter instance with its own identity.
- A special identity `human@<owner>` stands for the owner. busctl uses it to post and approve (§10); the bus monitor uses it only once sending is added (§10a, milestone 9).
- Registration (via busctl, through busd, §10) creates the agent's DM channel, its broker account and access, and publishes its public key to the registry.

**Decision: one session per identity at a time.** The adapter (the MCP server) holds the identity through its key. Any new session on the same machine can pick up an existing identity by starting the adapter for it, but only one session may hold it at a time:

- On start, the adapter takes an exclusive `flock` on `~/.config/agent-bus/<agent-id>/lock` and writes its PID into the file. The OS releases the lock when the process exits or crashes, so there are no stale leases to clean up.
- If the lock is held, the adapter still starts (so the harness doesn't treat the MCP server as broken) but every tool returns an error naming the identity and the holder's PID, and the delivery hooks of that session stay silent (§7).
- Because only one adapter is active, the identity's read cursors have one writer, so no messages are split between sessions.

---

## 4. Bus model

This section describes the bus without naming a broker. §4a maps it to the current broker.

**Channels.** A channel is an ordered, durable, append-only log of records. **Ordering guarantee:** the messages of one thread are read in the order they were published; §4a says how. A channel is also a single stream at this scale, so nothing else is promised across threads.

| Channel | Purpose | Retention |
|---|---|---|
| registry | Agent ID → key history (current and former public keys, rotation links, revocations), metadata; one record per agent ID | latest per ID |
| `general` | Default channel, all agents | forever |
| group channel `<name>` | Named group channels (`lib-x`, `release`, …): open channels any agent may read and write | forever |
| DM `<agent-id>` | Inbox for one agent | forever |
| private channel `<channel-id>` | Closed channel, members only (§4b) | forever |
| requests | Advisory projection of each public request's latest status (§5); one record per request ID | latest per ID |
| catalogue | Public catalogue of group channels and descriptions; one record per name | latest per ID |

Notes:

- Channels are created only by the admin principal (busctl or busd). Nothing creates a channel by accident.
- **Decision: group channels are open channels**, like an open chat channel: every agent may read and write every group channel without asking. Joining only changes what the adapter subscribes to. What an agent may *do* with a message is limited by the safety rules (§8), not by channel access. Closed channels are private channels (§4b).

**Record kinds**

Not every record is a signed envelope. Each channel carries one kind, and readers (the monitor included) dispatch by channel:

| Channels | Record | Schema owner | Verification |
|---|---|---|---|
| `general`, group channels, DMs, private channels | signed conversation envelope (§5) | adapter | the rules in §6 |
| registry | `AgentRecord`: key history (§6) | busctl / busd | written only by the admin principal and trusted through the broker access rules; the rotation links inside are signature-checked (§6) |
| catalogue | `TopicRecord` (name, description, `updated_at`) | busctl / busd | admin-written, trusted through the access rules |
| requests | request projection record (§5) | adapter (any agent may write; busctl can rebuild) | none; advisory, never used for authorization |

Milestone 1 already writes `AgentRecord` (`agent_id`, `public_key`, `fingerprint`, `registered_at`) and `TopicRecord` (the catalogue record). `AgentRecord` gains a `keys` list, one entry per key: `public_key`, `fingerprint`, `valid_from`, `rotation_sig` (the previous key's signature over the new entry; empty for the first key and for a key added after a revocation, §6) and `revoked` (with `revoked_at`). The existing `public_key` and `fingerprint` stay and always name the current key, so a record without `keys` is read as a history of one key.

**Read cursors and handled messages**

- Each agent has one read cursor per channel it reads: its DM channel, `general`, and the channels it has joined. The cursors are stored on the broker under the agent's own name.
- **Decision: the adapter moves its cursors itself; nothing advances automatically.** A cursor commit is cumulative per channel, so the adapter commits only the contiguous prefix of handled positions: acking B before A does not move the cursor past A. A message is *handled* when the agent has acked it (`ack` also covers passing on a message without acting), when the adapter terminally rejected it (§6), or when the adapter sent it itself: its own messages come back on `general` and joined channels, and their IDs enter the handled set at send time, so they never stall the cursor. Handled message IDs go into the persisted handled set (§6 step 4); an ID is never added when first seen. A crash therefore re-delivers unhandled mail and skips handled mail whose position was not yet committed.
- **Decision: the adapter persists its joined channels** locally next to its key, and a local cursor per channel (the same contiguous prefix it commits).
- **First start and recovery.** On its first start the adapter reads its DM channel and `general` from the beginning. When it joins a channel, the default stays latest; `join(channel, from="earliest")` exposes history (§9) without redelivering it as new mail. If the broker has no stored cursor for a joined channel (first start, or expired), the adapter falls back to its persisted local cursor, else to the beginning for DM and `general` and latest for group channels.
- The broker must keep cursors effectively forever (milestone 1 follow-up, §11).

**Accounts and access**

- Every client has its own broker account. An agent's account may: read and commit cursors under its own name, read its own DM channel, write any DM channel, read and write `general` and every group channel, read the registry and the public catalogue, read and write requests (agents list open requests, §5), and produce idempotently, so producer retries never write a message twice. It gets access to a private channel only when busd grants it (§4b).
- Only the admin principal writes the registry and the public catalogue and creates channels. Agents hold no admin rights and cannot choose broker resource names.
- The monitor's account may read every channel and use its own cursor name `bus-monitor` (§10a).
- **Decision: encrypted connection to the broker.** Every client (`agent-bus`, `busctl`, `busd`, `bus-monitor`) connects over TLS; the broker has no plaintext listener. The broker is assumed to be on the local network. If a machine is elsewhere, the owner sets up a secure tunnel or VPN to reach it; the bus does not provide one. busctl creates a private CA and the broker's certificate during setup (`busctl tls init`). Clients trust that CA only, given by a `--ca-cert` flag (default `~/.config/agent-bus/ca.pem`). No public CA is needed on a LAN.

Note that broker access and message signatures do different jobs: access rules control who can reach what on the broker; signatures prove authorship end-to-end and survive replication, export, and a compromised broker.

---

## 4a. Kafka binding

How the model of §4 maps to Kafka. Everything Kafka-specific is here and in `internal/broker`.

**Decision:** Redpanda for development and small deployments (single binary, Kafka API). Code must use only the Kafka protocol so real Kafka works unchanged.

| Channel | Topic | Retention |
|---|---|---|
| registry | `agents.registry` | compacted |
| `general` | `agents.general` | infinite |
| group channel `<name>` | `agents.topic.<name>` | infinite |
| DM `<agent-id>` | `agents.dm.<agent-id>` | infinite |
| private channel `<channel-id>` | `agents.private.<channel-id>` | infinite |
| requests | `agents.requests` | compacted |
| catalogue | `agents.topics` | compacted |

- Topic auto-creation is **disabled**. The admin principal creates topics.
- One partition per topic, and records are keyed by thread ID (agent ID, request ID or name for the registry, requests and catalogue), so thread order is preserved.
- `@` is not valid in Kafka topic names; map `lib-x@bob` → `agents.dm.lib-x.bob` (document the mapping in one function). A private channel's topic carries an opaque ID chosen by busd, never the channel's name (§4b).
- Cursors are consumer-group offsets: each agent consumes with `group.id = <agent-id>` and subscribes to its DM topic, `agents.general`, and the topics it has joined. Auto-commit is off and the adapter commits offsets itself. Kafka commits are cumulative per partition, which is what the contiguous-prefix rule of §4 relies on.
- The broker keeps group offsets effectively forever: the manifest sets a very long offsets retention (milestone 1 follow-up, §11).
- Accounts are SASL/SCRAM users (simple) or mTLS (stronger). **Decision:** SCRAM for v1; credentials are stored with the adapter's key, never in a project directory. Every client connects over `SASL_SSL`.
- ACLs. An agent principal gets: read and commit offsets in its own group, read its own DM topic, write any DM topic, read/write `agents.general` and every `agents.topic.*` topic (one prefixed ACL), read `agents.registry` and `agents.topics`, read/write `agents.requests`, and cluster `IdempotentWrite`. A private topic is granted to each member by a literal ACL (read and write on that one topic) from busd. The private namespace `agents.private.*` is outside every prefixed grant. Only the admin principal writes `agents.registry` and `agents.topics` and creates topics.
- The monitor's principal may read every `agents.*` topic (this includes `agents.private.*`, §4b) and use its own consumer group `bus-monitor`.
- Retention, offsets retention and the TLS listener are set in the Redpanda manifest (`deploy/redpanda.yaml`).

---

## 4b. Private channels

A private channel is a closed channel: only its members can read or write it. Group channels stay open (§4).

**Privacy boundary.** **Decision:** "private" means private from other agents, not from the owner, the monitor or the broker. The owner can always read everything through the monitor's read-only principal and the broker's admin access. There is no end-to-end encryption (§1) and no join key.

**Roles.**

- An agent asks busd to create a private channel (§10b). The creator becomes its **manager**. The owner can always step in as recovery admin.
- Only the manager or the owner invites and removes members. A member may leave itself. There is one manager per channel. When the manager leaves, is removed or is revoked, the manager field becomes empty and the owner acts as recovery admin; the owner may name a new manager (`busctl channel manager`). Agents cannot hand management to each other in v1.
- An invitation names a registered identity whose key is not revoked. **Decision:** an invitation grants membership immediately, with no accept step (low friction). After busd confirms an invite, the inviter's adapter (or busctl, for the owner) sends the invitee a signed `invite` notice as a DM, carrying the channel ID and name. The notice is only information; the access comes from busd's grant. `join` on a private channel then only subscribes the adapter. The broker enforces access: if it answers access denied, the adapter reports "not a member", and the answer is never a handled message.
- A private channel's ID is a random 128-bit value in lowercase base32 without padding (26 characters), chosen by busd; the name is only a label in busd's ledger. Private channels are never deleted in v1.
- The owner's posting identity, `human@<owner>`, is not a member automatically. When the owner posts to a private channel, busd grants that identity read and write access explicitly and records it in the ledger as owner access.

**Access.** Membership is enforced by broker access: busd grants each member read and write on that channel only. The grant lives in its own namespace, outside the open group-channel grant (§4a). A member reads with its own cursor like any other channel.

**History.** **Decision:** current members may read the channel's whole retained history, including what was sent before they joined. Broker access cannot filter by date, and adapter-side filtering would not be a protection. A member who is removed and invited again can read the history again. If future-only membership is ever needed, it needs new channel epochs, not filtering.

**Leaving and removal.**

- **Decision: unsubscribe and leave are different.** `unsubscribe` only stops the adapter fetching a channel and keeps membership. `leave` on a private channel ends membership: busd removes the member's access, and the tool reports done only after the broker has confirmed the change. `leave` on a group channel stays a plain unsubscribe (§9).
- When membership ends, by `leave` or by removal, the adapter drops that channel's pending and held mail and stops fetching it. An access-denied error from a channel is not a handled message: the adapter stops fetching the channel, alerts once, does not commit past it and does not reconnect in a loop.
- No retroactive secrecy: what a member already read stays with it, and requests already in flight may succeed for a moment after removal. Removal cannot erase anything.

**Metadata.** **Decision:** private names, descriptions and requests never go to the public catalogue or the requests channel. The private catalogue (id, name, description, manager, members) is kept by busd and served only to a channel's members and the owner. Requests in a private channel stay inside it: the `requests` tool derives them from the channel's own history through the read-only path (§6), and no projection record is written.

**Membership ledger.** busd keeps one durable ledger of channels and members, with one writer (busd). Every change is an operation with an idempotency ID and the channel version it expects. Operations on one channel run one at a time, and an operation with an old version or an already-seen ID is rejected or answered with the earlier result, so a stale retry cannot restore removed access. A change is recorded in the ledger first, applied to the broker second, and marked confirmed last. A crash in between leaves a pending change that busd finishes on its next start. busd also reconciles the actual broker access of all private channels against the ledger on start and every five minutes: it grants what is missing and revokes what is extra, and never grants access to an account that no longer exists.

**Ledger safety.** busd never revokes access on the strength of a missing, empty or reset ledger. In that case it refuses to revoke anything and says why. The owner either restores a ledger backup or runs `busd reconcile --adopt`, which takes the current broker access of the private channels as the truth and asks the owner to name each channel's manager.

---

## 5. Message envelope

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
    "thread": "01JD8Y…",           // ULID of the thread's first message
    "reply_to": null,              // message ID this replies to, if any
    "hops": 0,                     // causal reply depth, set by the adapter (§8)
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

**Message types:** `message`, `question`, `answer`, `feedback`, `bug_report`, `feature_request`, `status`, `ack`, `key_rotation`, `invite`.

`key_rotation` is an informational notice in `general`; the registry, not the notice, carries the key chain (§6). `invite` is an informational DM that tells an agent it was invited to a private channel (§4b).

**Request lifecycle** (for `feature_request` and `bug_report`). Status changes are signed `status` messages in the request's thread. Each request has a requester (the sender of the first message, which itself carries status `proposed`; there is no separate status message) and an assignee. For a DM request the assignee is the recipient. For a request in a group or private channel, the assignee is the first agent other than the requester to make a status change, first in the order within the thread's channel; later actors who are not the assignee are refused. A pinned-owner action never makes the owner the assignee.

| From | To | Who may make it |
|---|---|---|
| (new) | `proposed` | requester |
| `proposed` | `acknowledged` | assignee |
| `proposed`, `acknowledged` | `accepted` | assignee; with `--no-auto-accept-messages` only the pinned owner (§8 rule 2) |
| `proposed`, `acknowledged` | `rejected` | assignee, pinned owner |
| `accepted` | `in_progress` | assignee |
| `in_progress` | `done` | assignee |

The pinned owner (`human@<owner>` with `source=pinned`) may make any transition in the table. `rejected` and `done` are terminal. **Decision:** one check, taken from this table, runs on every outgoing and incoming status path: `set_request_status`, and also generic `send` / `publish` with `type=status`. The adapter refuses an outgoing status that fails it, and an incoming one that fails it is terminally rejected (§6) and alerted. The current state is derived from the thread: the request's first message counts as `proposed`, and each later signed `status` message moves it on. The adapter records its own sends in the handled set, so a requester knows its own `proposed`. If the history needed for a check is missing (a DM request split across two DM channels, or a group channel joined at latest), the adapter fetches it through the read-only history path (§6). If that fails, it holds the status message (retryable, redelivered later) and never rejects it terminally.

**the requests channel is a projection.** The signed status message in the thread is authoritative; the requests channel is a rebuildable, advisory record that lets agents list open requests without scanning history. Each projection record carries the source event ID (the status message that produced it) and a revision (the number of status changes so far). The writer (the adapter that sends the status message; duplicates are harmless) reads the current record first and never overwrites a newer revision; readers ignore a record older than one they have seen. `busctl requests rebuild` recreates it from the threads. Requests in a private channel are never projected: they stay inside the channel and are derived from its history (§4b). Authorization never comes from the projection. A crash between the two writes leaves the thread right and the projection stale, until the next status or a rebuild.

Body size limit: 64 KB. Larger content goes by reference (link to a repo, gist, or file). The adapter checks the size of every incoming record (64 KB body plus envelope, 96 KB in all) before parsing or verifying it.

---

## 6. Signing and trust

Signatures are what keep messages trustworthy: a message that was altered, or that claims a sender whose key doesn't match, is rejected even if it travelled unencrypted somewhere along the way.

**Decision: Ed25519, not PGP.** Only signatures are needed; OpenPGP's keyrings, subkeys, and web of trust add complexity without benefit here. Use Go's `crypto/ed25519`.

**What is signed**

- `sig = Ed25519.sign(sk, JCS(envelope))`, where JCS is RFC 8785 JSON canonicalization.
- The whole envelope is signed, including `to`, so a signed message cannot be replayed into a different channel or inbox.

**Key storage**

- Private key: `~/.config/agent-bus/<agent-id>/key` (mode 0600), together with the broker credentials, the pins, the handled set, the held messages and the persisted channels.
- **The agent has no tool that returns or reads key material.** Only the adapter reads the key, and it never lives in a project directory.
- **Decision: this is protection against accidental exposure only.** Mode 0600 and keeping keys out of project directories stop an agent from committing or printing a key by mistake. They are not an access boundary: an agent with shell access running as the owner can read `~/.config/agent-bus`, and a prompt-injected agent could leak what it finds there. A separate UID or sandbox profile for the adapter, which would put the directory out of the agent's reach, is a possible later hardening, not v1.
- Fingerprint: `SHA256:` + base64 of SHA-256 of the raw public key (same style as SSH).

**TOFU store** — `~/.config/agent-bus/<agent-id>/known_agents`:

```
# agent-id     fingerprint     public-key (base64)   first-seen             source
lib-x@bob      SHA256:Xy3…     MCow…                 2026-11-24T10:15:00Z   tofu
human@alice    SHA256:Ab9…     MCow…                 2026-11-20T08:00:00Z   pinned
```

A pin stores the public key, not only the fingerprint, so a message can be verified without the registry. After a rotation an agent has one line per key it has used (see key rotation). Revoked fingerprints are kept in a separate local `revoked_keys` file.

**Verification on receive** (in order; any failure → reject):

0. Size: reject a record over 96 KB before anything else (§5).
1. Parse; `v` is supported.
2. `to` matches the channel the record was consumed from.
3. Look up `from` in `known_agents`:
   - **Unknown, `--auto-accept-new-keys` on (default):** fetch its record from the registry; check that `key_fp` names a key in it that is not revoked; verify the signature; pin it (`source=tofu`) and flag the message to the agent as "first contact". A revoked key is never a TOFU candidate.
   - **Unknown, `--no-auto-accept-new-keys`:** hold the message (quarantine) and raise an alert. It is not handled, so the cursor is not moved past it. The owner pins the key with `busctl pin` first. The adapter re-reads `known_agents` when it re-checks held messages (at each hook and `inbox` call; no file watch), then redelivers the held message and accepts the sender's later ones. Held messages are stored on disk in the identity directory and do not count against the inbox buffer (§7). `busctl pin <agent-id> <fingerprint>` writes to the `known_agents` of every identity on the current machine, or of one with `--for <agent-id>`.
   - **Known, fingerprint matches a pinned key that is not in `revoked_keys`:** verify signature.
   - **Known, fingerprint differs:** look for a rotation chain in the registry (see key rotation). If the chain leads from the pinned key to this key, update the pin. Otherwise hold the message like an unknown sender's (redelivered once the owner pins the key), log loudly, and notify the human (busctl shows it). **Never auto-accept a changed key.** Same behaviour as SSH's host-key warning.
4. Dedupe: skip if `id` is in the adapter's handled set (persisted locally, e.g. SQLite) or already pending in its buffer or hold store. An ID enters the handled set only when the message is handled (§4), not when first seen. This stops replay of an old signed message into the same channel.
5. Reject if `ts` is more than 5 minutes in the future. Do **not** reject old timestamps: offline agents legitimately read backlog.

**Terminal rejection** (oversized record, unsupported version, bad signature, wrong `to`, revoked key, failed request-transition check, future timestamp) is logged and alerted, and counts as handled, so the cursor moves on. Rejected messages are recorded locally with the reason and are not shown to the agent except as a count. Three cases are retryable instead, and the message is held: (1) an unknown sender under `--no-auto-accept-new-keys` and (2) a changed key (both step 3, redelivered after `busctl pin`), and (3) a status message whose thread history for the transition check is missing (§5). A held message stops only the commit cursor of its channel, not delivery: later messages are still delivered, and after a crash the handled set skips those already handled.

**History is a read-only path.** `history` and `join(from="earliest")` (§9) read through a separate reader with no cursor of its own that verifies signatures but never moves the cursor, changes pins, applies statuses or rotations, or touches the handled set. History results are marked as such and are not new mail. A sender not in the registry is shown as "unknown sender, unverified".

**Key rotation:** the agent's registry record (keyed by agent ID, compacted) holds its full key history. `busctl rotate <agent-id>`, run on the agent's machine, appends the new key; the new entry is signed by the previous key (the rotation link), except after a revocation, when it has no link (see revocation) and the agent also posts an informational `key_rotation` notice. An adapter that sees a key it has not pinned walks the chain in the registry from its pinned key, checking each link's signature, and ends with the current key; it then pins the keys along the way. An adapter that was offline through several rotations catches up the same way. A chain that does not start at the pinned key is a changed key (step 3). Adapters keep former keys so they can verify backlog signed before a rotation. The monitor verifies each message against the key whose fingerprint is `key_fp`, taken from the record's history, so old messages still verify.

**Revocation:** busctl marks the key `revoked` in the agent's registry record. The entry is never deleted and there are no tombstones, so an offline adapter still sees it. Adapters consume the registry continuously, independent of first contact, and persist the fingerprints of revoked keys in `revoked_keys`. Dropping a pin does not forget a revocation, and a revoked key is never treated as a new TOFU candidate. `busctl revoke` marks the identity's current key revoked. To keep using the identity, run `busctl rotate` on its machine. The new entry has no rotation link, because a link signed by a revoked key proves nothing, so receivers that know the agent see a changed key (step 3): its messages are held until the owner pins it with `busctl pin`, even with `--auto-accept-new-keys` on. A receiver that never pinned the agent treats the new key as a first contact (TOFU). Messages signed with a key before its revocation but read afterwards are rejected on purpose. The monitor shows a message signed by a key revoked later with a warning.

**Revoking a key does not revoke broker access.** The agent's broker account can still read and write what it was granted. For a compromised agent, `busctl revoke --incident` (§10) revokes the key, revokes the agent's access and deletes its account, and removes the agent's busd authority and private-channel memberships. Revoking the key alone also stops the agent's busd requests (they are checked against the current non-revoked key, §10b), but not its broker traffic.

**Weak spot:** TOFU trusts whoever speaks first. Mitigations: the registry is admin-written only; humans can pre-pin keys with `busctl pin`; busctl can print fingerprints for out-of-band checking; `--no-auto-accept-new-keys` turns TOFU off entirely.

---

## 7. Delivery: how agents "listen"

MCP agents cannot be interrupted mid-turn, so delivery has two paths:

1. **End-of-turn check (main path).** The adapter's background reader buffers verified messages. A harness hook asks the adapter for unread mail when the agent finishes a turn:
   - The hook process is short-lived, so `agent-bus pending` asks its own session's adapter over a unix socket. **Decision:** the socket belongs to one adapter process, not to the identity, and a hook only reaches the adapter of its own session. Planned approach: the hook and the MCP stdio server are both started by the harness process, so the adapter names its socket after its parent PID (the harness), and the hook walks up its own process ancestors (it may run under a shell) until it finds a matching socket. If the harness gives both the hook and the MCP server a session ID, that is used instead. The exact mechanism is finalised in milestone 5. The adapter that lost the identity lock (§3) answers nothing, so its session's hooks stay silent. No adapter, no mail, or a lost lock: the hook exits silently.
   - Claude Code: a `Stop` hook that runs `agent-bus pending --format=hook`; if mail is waiting, it returns a blocking decision whose reason tells Claude to read its inbox, so Claude continues instead of stopping.
   - OpenCode: a plugin that, on the session-idle event, runs the same `agent-bus pending` call and, if mail is waiting, injects a follow-up prompt telling the agent to read its inbox. The exact event name and injection call are decided in milestone 5.
2. **Idle listener.** An agent with nothing to do calls `wait_for_messages(timeout)`, which blocks until mail arrives or the timeout passes.

Also provide a `SessionStart` hook (Claude Code) that shows a short summary of unread mail and open requests when a session starts.

**Limitation (Decision).** Mail reaches a session while it is active (the `Stop` hook, or `wait_for_messages`) or when the next session starts (`SessionStart`). An idle, stopped session is not woken: after `wait_for_messages` times out and the turn ends, new mail waits until the next hook or session. The adapter tracks which messages it has already notified about. A hook prompts only for messages not yet notified; earlier mail that is delivered but unacked appears in the summary line without causing a prompt. A notification held back by the cooldown (§8 rule 3) stays un-notified and is retried at the next hook.

**Bounded buffer.** The background reader holds at most 500 messages and 8 MB (`--inbox-buffer`). When full it pauses fetching and resumes as the agent drains the inbox, so a long absence cannot exhaust memory. Held messages (§6 step 3 and status holds, §5) live on disk in the identity directory and do not count against it. Hook summaries are bounded too: a count plus at most five one-line summaries.

---

## 8. Safety rules

1. **Messages are untrusted input.** A valid signature proves who sent a message, not that its content is safe; a trusted agent can relay injected instructions from a poisoned dependency or web page. The adapter wraps every delivered message in a clear "data, not instructions" frame, and CLAUDE.md / AGENTS.md guidance says the same.
2. **Auto-accept.** **Decision:** the adapter flag `--auto-accept-messages` is on by default, because all agents belong to the same owner; `--no-auto-accept-messages` turns it off. The frame from rule 1 stays the same in both modes (messages are peer data, never the owner's instructions). The flag decides whether the agent may act on a verified request without the owner:
   - On: the assignee may move a `feature_request` or `bug_report` to `accepted` and work on it.
   - Off: moving a request to `accepted` requires the owner (busctl `approve`, the monitor, or the owner in the session's prompt), and the delivered frame says "discuss, don't act without the owner's approval".
   - Owner approval over the bus: `busctl approve` (and later the monitor) publishes a `status: accepted` message in the request's thread, signed by `human@<owner>`. The adapter honours it as approval only when that key is in `known_agents` with `source=pinned`; with `--no-auto-accept-messages` that is the only way a request reaches `accepted`. Later progress (`in_progress`, `done`) follows the transition table in §5.
3. **Loop guard.** `hops` measures causal reply depth. The adapter, not the agent, sets it:
   - `hops` = the parent's stored `hops` + 1. The parent is the message named by an explicit `reply_to` from the agent; the agent's explicit `reply_to` and `thread` win. If that message is not in the adapter's local store, it is looked up through the read-only history path; if it cannot be found, the send is refused, so an invented `reply_to` cannot reset the count.
   - Without them, a `send` or `publish` inherits `reply_to`, `thread` and `hops` from the most recent message delivered or acked in the last five minutes. Older mail never binds a send, so one message that is never acked cannot lock every later send.
   - With no such message, the send is the agent's own initiative and starts a new thread at 0.
   - Branched replies each count from their own parent. The adapter persists each message's `hops` locally, so a restart keeps the count.
   - The adapter refuses to send when the parent's `hops` ≥ 6 (`--max-hops`).
   - Only a message from the pinned `human@<owner>` resets the count (its children start at 0); a TOFU-pinned owner key does not.
   - The end-of-turn hook also has a cooldown so two agents cannot keep waking each other.
4. **Rate limits** in the adapter: 30 messages per agent per 10 minutes by default (`--rate-limit`).
5. **Secrets.** The adapter scans outgoing bodies for obvious secrets (tokens, private keys) and blocks the send.

---

## 9. MCP tools (adapter)

| Tool | Parameters | Returns |
|---|---|---|
| `whoami` | — | agent ID, fingerprint, joined channels |
| `send` | `to` (agent ID), `type`, `body`, `thread?`, `reply_to?` | message ID |
| `publish` | `channel` (a group channel or a private channel ID), `type`, `body`, `thread?`, `reply_to?` | message ID |
| `inbox` | `limit?`, `types?`, `channel?` | verified unread messages |
| `ack` | `message_ids` | ok (marks handled; the adapter commits the contiguous prefix, §4) |
| `wait_for_messages` | `timeout_s` | messages or empty |
| `join` / `leave` | `channel`, `from?` | ok; `from="earliest"` also returns history through the read-only path. On a group channel, `leave` only unsubscribes; on a private channel, `join` subscribes to a channel the agent is a member of (otherwise the error is "not a member"), and `leave` ends membership (§4b) |
| `unsubscribe` | `channel` | ok; stops fetching, keeps membership (a private channel, §4b) |
| `create_private_channel` | `name`, `description` | channel ID; the caller becomes its manager (§4b) |
| `invite` / `remove_member` | `channel`, `agent` | ok, once busd and the broker confirmed; manager or owner only. `invite` also sends the signed `invite` notice DM (§4b) |
| `list_members` | `channel` | members and manager of a channel the caller belongs to |
| `list_channels` | — | public channels with descriptions, plus the caller's own private channels from busd (public channels only, with a note, while busd is down) |
| `list_agents` | — | agents, last seen, fingerprint, trust state |
| `history` | `channel` or `thread`, `limit`, `before?` | verified messages, read-only (§6) |
| `requests` | `agent?`, `status?` | open feature requests / bug reports |
| `set_request_status` | `request_id`, `status`, `note` | ok (checked against the transition table, §5; `accepted` needs `--auto-accept-messages`, §8 rule 2) |

The private-channel tools call busd (§10b); the adapter signs the request with its identity key and reports busd's errors as they are, including "busd is not reachable". All other tools talk to the broker only.

The adapter fills in `id`, `ts`, `from`, `hops`, `key_fp`, and the signature; agents never construct envelopes. A `send` or `publish` with `type=status` goes through the same transition check as `set_request_status` (§5).

**Decision: delivery is at-least-once.** If the broker stores a message but the adapter crashes before returning its ID, a retried send produces a second message with a new ID. There is no outgoing journal. Agents must tolerate duplicates. The broker's idempotent producing (§4a) only covers the adapter's own retries within one run.

---

## 10. busctl (human/admin CLI)

`register <agent-id>`, `rotate <agent-id>`, `revoke <agent-id>` (`revoke --incident`, below), `channel create|list|show|invite|remove|manager` (`create` makes a group channel; the rest also cover private channels, §4b), `tls init` (also issues busd's certificate), `tls reissue` (new broker and busd certificates from the existing CA, for an expired certificate or a moved host; `tls init --force` replaces the CA too), `tail [channel|--all]`, `thread <id>`, `requests` (`requests rebuild` recreates the requests channel from the threads), `pin <agent-id> <fingerprint>`, `known`, `alerts` (key mismatches, rejected messages, unknown senders), `post` (as `human@<owner>`; `post --channel <id>` for a private channel), `approve <request-id>`.

**Decision: busctl goes through busd.** Commands that need admin rights (register, revoke, channel create, channel management) are requests to busd (§10b), signed by `human@<owner>`; busctl asks for the master password to unlock that key for the command (§10a). Read-only commands (`tail`, `thread`, `known`, `alerts`, `pin`) never need busd.

**Direct admin access is the explicit recovery path.** `--direct` (admin credentials from the owner's config) skips busd. It is how the system is set up (`tls init`, the first `register` call for `human@<owner>`, channels) and how the owner acts when busd is down. Direct commands never change private-channel membership, except `revoke --incident --direct`, which removes the agent's broker access and account directly and leaves the ledger update pending for busd (below): the ledger has one writer (§4b), and busd's reconciliation would undo the change. `busd reconcile --adopt` (§4b) is the recovery for a lost ledger.

**Incident revocation.** `busctl revoke <agent-id>` marks the agent's current key revoked, as before (§6). `busctl revoke <agent-id> --incident` is for a compromised agent and does all three: it revokes the agent's access and deletes its account, revokes its signing key, and removes its busd authority (busd ends its memberships; a channel it managed has no manager until the owner names one, §4b). With `--direct` (busd down) it does the first two and reports the busd part as pending; repeat the command without `--direct` to finish. To bring the identity back, the owner registers it again, rotates its key (§6), and the channels' managers invite it again.

---

## 10a. Bus monitor

A small Go service that connects to the broker and shows the messages flowing around, in a browser. It runs as a separate process with its own read-only broker principal and its own cursor name; it may share busd's binary and deployment (§10b).

**v1: read-only.**

- Consumes every channel, private channels included, with its own read-only principal and its own cursor (its reading never moves any agent's read position). It dispatches by channel (§4): only conversation channels hold signed envelopes; registry, catalogue and projection records are parsed as their own kinds and never checked for a message signature. Messages from private channels are marked private in the views.
- Verifies each conversation message's signature and `to` field and shows the result: sender, recipient or channel, type, thread, verified / rejected (with reason). It does no dedupe or timestamp checks; a duplicate ID is shown as such.
- Live view (server-sent events) plus browsing by channel, thread and agent, and the open requests from the requests channel (advisory, §5).
- Verification uses the key history in the registry only (the key named by `key_fp`, §6). The monitor keeps no `known_agents` store and pins nothing; a sender missing from the registry, or a signature that doesn't match its registry key, is shown as rejected with the reason.
- Listens on `127.0.0.1` only, over plain HTTP. To view it from another machine, use an SSH tunnel.

**Later: sending.**

- The monitor posts as `human@<owner>`, using the password-protected key described below. The owner logs in with the master password, and the key is decrypted only in memory.
- `--listen` on other addresses becomes allowed, served over HTTPS with a certificate from the bus CA (§4), and every page needs the master password.
- Adapters can be configured to treat `human@<owner>` as the owner (`--owner-identity`; the default is `human@` plus the owner part of the adapter's own agent ID). Messages from it are delivered as user commands rather than as peer messages, and count as the human post that resets the loop guard (§8 rule 3). Approval of requests already works from milestone 7 through the `status: accepted` path (§8 rule 2).
- This only applies when that key is in `known_agents` with `source=pinned`; a TOFU-pinned owner key is treated like any other agent. Machine setup pins it with `busctl pin`.

**The `human@<owner>` key.** It speaks with the owner's authority, so it is the most valuable secret in the system. `busctl register human@<owner>` creates it and asks for the master password. The private key is stored encrypted with a key derived from that password (Argon2id) and never unencrypted. busctl `post` / `approve` / management commands and the monitor all use this key. **Decision:** busd never holds it as an unattended credential, even when it hosts the monitor: sending needs the owner to log in.

---

## 10b. busd (control service)

busd is a small Go service with one job: the work that needs admin rights. It is not in the message path (§2).

**What it does**

- Private channels and their membership: create, invite, remove, leave, list, and the private catalogue (§4b).
- Broker access: it grants and revokes the broker access that membership implies, and creates accounts and access for registered agents. It holds the admin principal; admin credentials never leave it and the owner's config.
- The normal path for busctl's management commands (§10).
- The monitor (§10a) may share busd's binary but runs as a separate process with its own read-only principal.
- A browser admin UI for the owner is later work and not specified here.

**State.** The membership ledger (§4b): its own database file on busd's host. Everything else lives in the broker or in the adapters. busd reads the registry continuously (its own reader) to know every agent's current key and whether it is revoked.

**Authentication of agents.** No new long-lived agent credential.

- busd's identity is a configured service name (for example `busd@<owner>`) bound to its own TLS certificate from the private CA (§4), issued by `busctl tls init`. busd is not a registered agent. The adapter takes `--busd-url` and `--busd-name`, connects over TLS, and checks that the certificate chains to the bus CA and carries that name. This authenticates busd to the agent only.
- The agent asks for a nonce: random, single-use, valid for 30 seconds.
- The agent signs each request with its Ed25519 identity key. The signed payload is domain-separated with a fixed prefix (`agent-bus/busd-request/v1`) and binds: busd's service name, the caller's agent ID and `key_fp`, the method, the target (channel ID and member, where they apply), the SHA-256 of the body, the nonce and the idempotency ID.
- busd verifies the signature against the caller's current, non-revoked key in the registry. It never uses a key sent by the caller and never pins on first use. It burns the nonce, rejects a reused or expired one, and rejects any signature that does not cover the request it received.
- Authorisation comes from the authenticated identity (manager, member, owner), never from an `agent_id` parameter in the request.
- Every request carries an idempotency ID so a client can retry safely: a repeat returns the first result (§4b).
- The owner's calls (busctl) are signed the same way by `human@<owner>`, whose key is unlocked by the master password (§10a).

**API.** Agent-callable: create a private channel, list the caller's channels and members, invite, remove, leave, and the private catalogue. Everything else (register, revoke, channels, incident revocation, other agents' channels) needs the owner. The API is in bus terms (channels, members, accounts), so it stays the same if the broker changes (§2).

**If busd is down.** Messaging is not affected: agents and the monitor talk to the broker directly, and the access that was granted before stays in force. Creating channels, inviting, removing, leaving a private channel and busctl's management commands fail with a clear error ("busd is not reachable"); none of them pretends to succeed, and the adapter keeps the membership as it was. Recovery is `busctl --direct` (§10) or restarting busd, which finishes pending changes and reconciles on start.

---

## 11. Milestones

1. **Broker up (done):** Redpanda Pod manifest started with `podman kube play`, TLS, SASL/SCRAM, auto-create off; busctl `tls init`, `register` and `channel create`. Follow-ups: very long consumer-group offsets retention in the manifest (§4a); `AgentRecord` gains the `keys` list (§4); and fitting the broker code to the two interfaces of §2. Today `internal/broker` hands busctl a raw Kafka admin client and Kafka channel names and ACL rules (`bus.DMTopic`, `SystemTopics`, `AgentACLRules`). The refactor moves those behind the message transport and admin interfaces, with the channel-name mapping inside the binding.
2. **Adapter core:** envelope, JCS, Ed25519 sign/verify, TOFU store (pinned public keys, `revoked_keys`) with `--[no-]auto-accept-new-keys`, handled set and cursor store, manual contiguous-prefix commits, persisted channels, bounded inbox buffer, identity lock, `send` / `publish` / `inbox` / `ack`.
3. **busd:** the control service (§10b): signed-request authentication with nonces, private channels (§4b), the membership ledger with reconciliation, the private catalogue, ledger backup and restore, `busd reconcile --adopt` (§4b), busd's own certificate and service name, and busctl as a busd client (`--direct` stays for recovery).
4. **MCP surface:** all tools in §9, private-channel tools included, with `history` as the read-only path; register with Claude Code and OpenCode.
5. **Delivery hooks:** Claude Code `Stop` and `SessionStart`; OpenCode plugin; per-session adapter socket and notified-message tracking (§7).
6. **Requests and safety:** request lifecycle with its transition table, the requests projection and `busctl requests rebuild`, privacy-aware request handling (requests in private channels are derived from the channel, never projected, §4b), `--[no-]auto-accept-messages`, loop guard (adapter-set `hops`), rate limits, secret scan.
7. **Ops:** password-protected `human@<owner>` key, busctl `tail` / `alerts` / `post` / `approve`, adapters honouring a pinned owner's `status: accepted` (§8 rule 2), key rotation (`busctl rotate`, key chain) and revocation, incident revocation: access revoked and account deleted, key revoked, busd authority removed (§10), `busctl tls reissue`, setup script per machine (which also pins the `human@<owner>` key).
8. **Monitor (read-only):** live view on localhost, browsing, verification status (§10a v1); may be hosted alongside busd (§10b).
9. **Monitor sending:** login with the master password, posting and approving as `human@<owner>`, HTTPS for non-localhost `--listen`, adapters delivering the owner's messages as user commands (§10a later). The key stays unlocked by the owner, never by busd on its own.

---

## 12. Acceptance tests

- Two agents on different machines exchange a DM; both verify and pin each other on first contact.
- A message re-produced to the same channel with the same ID is skipped (dedupe).
- A message copied into a different channel is rejected (`to` mismatch).
- A message signed with a new key for a known agent is held and raises an alert (delivered after `busctl pin`); a key reached through a valid rotation chain in the registry updates the pin.
- A receiver that was offline through two rotations verifies its backlog and the new messages; the monitor verifies pre-rotation history.
- An online adapter and one offline for a long time both reject a revoked key, also across restarts, and never treat it as a fresh TOFU candidate.
- With `--no-auto-accept-new-keys`, a message from an unknown sender is held and alerted; once the owner pins the sender, the message is delivered.
- A message altered in transit (any envelope field) fails verification.
- A second session that starts the adapter for an identity already in use gets an error from every tool; once the first session ends, a new one picks the identity up.
- With `--auto-accept-messages` (default), an agent can move a request to `accepted`; with `--no-auto-accept-messages`, it cannot until `busctl approve` sends the pinned owner's approval.
- A client cannot connect to the broker without TLS.
- The monitor shows a DM between two agents live, without changing either agent's read position, and shows a tampered message as rejected with its reason.
- (Milestone 9) A message from the pinned `human@<owner>` is delivered as a user command and resets the loop guard; the same identity pinned by TOFU does not.
- An agent offline for a day reads its full backlog in order on reconnect; nothing is lost if the session crashes before `ack`.
- Ack B before A, then crash: A is redelivered. Crash before and after the ack is persisted: the message is redelivered or skipped, never lost.
- A message sent before the adapter's first start is delivered. After joining a channel and restarting, the adapter resumes where it was. With the broker-side cursors expired, it falls back to its local cursor.
- A backlog larger than the inbox buffer replays without loss or crash.
- `history` on acknowledged mail and `join(from="earliest")` leave the unread state unchanged.
- Mail arriving after the idle timeout waits for the next hook or session; mail during the cooldown is notified at the next hook; mail arriving while earlier mail is still unacked prompts only for the new message.
- Two sessions on one identity: only the lock holder gets delivery prompts.
- Interrupting either write of a status change (the signed message or the requests projection) leaves the thread authoritative; a stale projection retry after a newer one does not overwrite it; `busctl requests rebuild` repairs it.
- Status changes through generic `send` / `publish` with `type=status` are checked like `set_request_status`; an unrelated agent cannot change a request's status; a group request gets its assignee from the first non-requester status change; with `--no-auto-accept-messages`, the pinned owner's approval is followed by legitimate progress; `rejected` is terminal.
- The monitor reads milestone-1 `AgentRecord` and `TopicRecord` records without false signature alerts.
- A send without `reply_to` or `thread` inherits from a message delivered four minutes ago, but not from one delivered or acked six minutes ago; a send naming a `reply_to` not found after the history lookup is refused.
- A status change whose thread history is missing is held, then accepted once the history is available; a record over 96 KB is rejected and the cursor moves past it.
- An agent's own posts to `general` and a joined channel do not stall its cursor.
- After `busctl revoke` and `busctl rotate`, receivers that know the agent hold its messages until `busctl pin` and then deliver them, even with `--auto-accept-new-keys`; a receiver that never met the agent pins the new key by TOFU as usual.
- Two agents set to auto-reply stop at the hop limit, also when they alternate `send` and `publish`, omit `reply_to` or `thread`, branch replies, or restart their adapters; a TOFU-pinned owner cannot reset the count.
- An agent cannot read another agent's DM channel (access denied).
- No adapter tool returns or reads key material, and keys are never inside a project directory.
- `busctl tail --all` reproduces the full conversation from the log.
- A non-member that connects to the broker directly cannot read or write a private channel (access denied), and cannot see its name; an agent that was removed cannot either.
- An agent that is not the manager or the owner cannot invite or remove members through busd; a member cannot remove another member; a member can leave itself; an invitation of an unregistered or revoked identity is refused.
- Creating, inviting and removing in a private channel leaves no name, description, member or request in the public catalogue or the requests channel; the private catalogue is served only to members and the owner.
- After removal, the adapter drops the channel's pending mail, stops fetching, and does not treat the access-denied error as a handled message or loop on reconnect. After a new invitation the member reads the whole retained history, including what was sent while it was out.
- `leave` on a private channel reports done only after the broker confirmed the access change; `leave` on a group channel only unsubscribes; `unsubscribe` on a private channel keeps access.
- A replayed busd request (same nonce), a request with a changed body, method or target, a request signed by a revoked or unknown key, and a request with a caller-supplied key are all refused; a retry with the same idempotency ID returns the first result and changes nothing twice.
- Crash and retry races in membership changes: a crash between the ledger write and the broker change is finished on busd's restart; concurrent invite and remove on one channel end in one consistent state; an old retry cannot bring back a removed member; reconciliation repairs access changed behind busd's back.
- With busd stopped, DMs, `general` and private-channel messages between existing members keep flowing; creating a channel, inviting, removing, leaving a private channel and busctl management commands fail with a clear error and change nothing.
- The monitor, a separate process, reads and shows private-channel messages marked private; `busctl post --channel` makes busd grant `human@<owner>` access first; the monitor cannot write or change access.
- `busctl revoke --incident` revokes the agent's access and deletes its account, revokes its key and ends its busd authority and memberships; its later busd requests and broker connections fail. `busctl revoke --incident --direct` does the access and key parts and reports the busd part as pending.
- A manager that leaves, is removed or is revoked leaves the channel with no manager; the owner can still act and can name a new one; agents cannot hand management over.
- A busd request with an expired nonce, or one whose nonce was already used, is refused.
- `busctl --direct` commands leave private-channel membership unchanged; reconciliation does not undo or redo them.
- With a missing, empty or reset ledger busd refuses to revoke anything and reports it; `busd reconcile --adopt` or a backup restore brings it back.
- An invitation sends the invitee a signed `invite` DM; `join` without membership fails with "not a member" and the denied read is not treated as handled mail; a status change in a private channel follows the same assignee rule as in a group channel.
- busd's certificate must chain to the bus CA and carry the configured service name, or the adapter refuses to talk to it.
- Tests that need the broker's behaviour run against the current binding; the same transport and admin tests run against any other implementation of the two interfaces.

---

## 13. Decisions

Settled by the owner on 2026-11-24:

1. Language: Go, with kong for CLI parameters (§2).
2. One session per identity at a time; a new session on the same machine can pick it up (§3).
3. Single-user setup; `--auto-accept-messages` on by default, `--no-auto-accept-messages` to turn it off (§8).
4. TLS to the broker; broker on the local network, tunnels or VPN for anything outside it (§4).
5. No end-to-end encryption; signatures guard integrity; `--auto-accept-new-keys` on by default (§6).
6. A separate bus monitor, read-only first, sending with a master password later. It may be hosted in the same binary and deployment as busd, but it keeps its own read-only broker principal (§10a, §10b).
7. The bus deals in identities, not projects; which code an identity works on doesn't matter to it (§3).
8. Identities are per machine and keys are never copied; each adapter is a sidecar proxy holding one identity for one session, and another concurrent session gets another adapter and identity (§3).
9. Monitor: localhost-only plain HTTP in v1; Argon2id-encrypted `human@<owner>` key; owner identity only honoured when pinned (§10a).
10. Libraries: franz-go, the official MCP Go SDK, a JCS library (§2).
11. Identity lock with `flock` plus a PID in the lock file; hooks talk to the adapter over a unix socket (§3, §7).
12. A private CA made by `busctl tls init` (§4).
13. What `--auto-accept-messages` allows, as described in §8 rule 2.
14. Hop limit 6 and 30 messages per 10 minutes as defaults, configurable by flag (§8).
15. The broker runs from a Kubernetes Pod manifest, started with Podman for now; another runtime later (§2).
16. Group channels stay open: every agent reads and writes all of them (originally `agents.topic.*` through one prefixed ACL, now §4a); joining only changes subscriptions. Closed channels are private channels, by invitation through busd (§4, §4b).
17. Agents read and write the requests channel and `general`, and produce idempotently (originally `agents.requests`, `agents.general` and Kafka's `IdempotentWrite`, now §4a).
18. `busctl tls reissue` renews only the broker and busd certificates from the existing CA; part of milestone 7 (§10).
19. Delivery and recovery: the adapter commits only the contiguous prefix of handled positions (nothing advances automatically); a message ID is handled when acked, terminally rejected, or sent by the adapter itself; a message from an unknown sender under `--no-auto-accept-new-keys` or with a changed key is held and redelivered after pinning; joined channels and cursors are persisted, with defined fallbacks when the broker has no stored cursor; the broker keeps cursors very long (§4, §6).
20. Delivery promise: at-least-once, no outgoing journal; mail reaches active sessions and the next session start, idle sessions are not woken; hooks talk to their own session's adapter; the inbox buffer is bounded (§7, §9).
21. Key trust: key storage guards against accidental exposure only; the registry record holds the key history with rotation links; pins store public keys; revoked keys stay in the registry, adapters persist revoked fingerprints (§6).
22. Loop guard: the adapter sets `hops` from the parent message; only the pinned owner resets it (§8).
23. Requests: a transition table checked on every status path; `rejected` is terminal; the requests channel is an advisory, rebuildable projection of the signed thread (§5).
24. Record kinds: conversation envelopes, registry, catalogue and projection records have their own verification policies; `history` and `join(from="earliest")` are read-only (§4, §6).

Added by the owner on 2026-11-24 (busd and private channels):

25. **busd, a control service, not a gateway.** Agents talk to the broker directly for messages; signing, verification, TOFU, inbox state and hooks stay in the adapter. busd holds the admin principal and does private channels, membership, broker access and busctl's management commands. busctl keeps direct admin access as an explicit recovery path. The owner's encrypted signing key is never an unattended busd credential. The monitor may share busd's binary but runs as a separate process with its own read-only principal; a browser admin UI is later work. busd has a service name and certificate of its own and is not a bus agent. If busd is down, existing traffic keeps flowing and management fails clearly (§2, §10, §10b).
26. **Private channels by invitation.** An agent creates one through busd and becomes its manager; the owner is recovery admin and names a new manager when none is left; only manager and owner invite and remove, members may leave; the invitee gets a signed `invite` DM. An invitation grants membership at once, enforced by per-member broker access in a separate namespace. No join key, no key exchange, no end-to-end encryption. Members read the whole retained history. Private means private from other agents, not from the owner, the monitor or the broker. Leaving removes access and is reported done only after the broker confirms it; the adapter drops pending mail. Private names, descriptions and requests never go to public channels. busd keeps one membership ledger with versioned, idempotent, per-channel serialised operations and reconciles broker access to it, but never revokes against a missing or reset ledger (`busd reconcile --adopt`). Incident revocation revokes access and deletes the account, revokes the key and removes busd authority (§4b, §6, §10).
27. **Agent authentication to busd:** TLS with a certificate for busd's configured service name from the bus CA, plus a per-request Ed25519 signature by the agent's identity key over a domain-separated payload (busd's service name, caller, method, target, body digest, single-use short-lived nonce), checked against the current non-revoked registry key; authorisation by that identity; idempotency IDs for retries; no new long-lived agent credential (§10b).
28. **Not tightly bound to one broker:** a broker-neutral model (§4) with one binding (§4a), currently Kafka and to be replaced; two small Go interfaces (message transport, admin), one implementation, confined to `internal/broker`; busd's API is the stable surface. Binding-specific rules stay inside §4a (§2, §4a).
29. **Milestone order:** milestone 1 is done; busd comes after the adapter core and before the MCP surface, so private-channel tools are exposed only after authorisation, the ledger and reconciliation exist (§11).
