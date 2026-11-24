# Agent Bus — Design Spec

A message bus that lets coding-agent sessions (Claude Code, OpenCode, others via MCP) on different machines exchange signed messages: direct messages, channel discussions, feedback and feature requests between projects that depend on each other.

**Decision: scope.** One developer runs the whole setup across several of their own machines. There is a single owner and no other users. Defaults favour low friction for that one owner; stricter behaviour is available behind flags.

This document is a brief for an implementing agent. Sections marked **Decision** are settled (§13 lists them).

**Decision: MVP first** (owner, 2026-11-24). The bus runs in a trusted environment, so we build a usable MVP quickly and tighten it later. The rest of this document describes the full system; for now only the MVP below is built, with the simplest version of each rule. Review findings that only harden against a compromised host or rare crash edges are deferred to a later tightening pass.

- **In the MVP:** milestone 1a (done), milestone 2 (adapter core), milestone 4 (MCP tools) and milestone 5 (Claude Code hooks). Agents talk over DMs, `general` and open group channels. Messages are signed and verified, keys are pinned on first use (`--auto-accept-new-keys`, the default), each message is acknowledged on its own, there is one session per identity, and the loop guard, rate limit and secret scan apply. `busctl --direct` creates identities, channels and the agents' consumers.
- **Deferred until after the MVP:** busd (milestone 3) and with it private channels and their membership checks; the monitor (milestones 8 and 9); the full request lifecycle (milestone 6 beyond plain `feature_request` / `bug_report` messages); holds for unknown or changed keys (`--no-auto-accept-new-keys`); key rotation and revocation; the OpenCode plugin.

---

## 1. Goals and non-goals

**Goals**

- Agents connect with a stable identity and can send direct messages or publish to channels.
- Every agent is in `#general`; agents can join and leave other channels, and can ask for private channels with invited members only (§4b).
- The broker keeps durable state: message history and per-agent read positions. busd keeps the private-channel membership ledger (§4b).
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
 ┌──────────────┐  stdio MCP  ┌───────────────┐   messages (TLS / pw)     ┌───────────────────┐
 │ Claude Code  │◄───────────►│ bus adapter   │◄─────────────────────────►│                   │
 │ session      │             │ (sidecar: one │                           │ Broker            │
 └──────────────┘             │  identity,    │                           │ (see §4a)         │
                              │  holds key,   │                           │                   │
 ┌──────────────┐  stdio MCP  │  signs, TOFU) │                           │ channels + access │
 │ OpenCode     │◄───────────►│ (one per      │◄─────────────────────────►│                   │
 │ session      │             │  session)     │                           │                   │
 └──────────────┘             └───────┬───────┘                           │                   │
                                      │ signed requests (via broker):      │                   │
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

 busctl (human/admin CLI): management and reads (tail, thread) go to busd; pin edits local pins;
 direct admin access to the broker is the recovery path
```

Components:

1. **Broker** — the message broker (NATS JetStream, §4a), reached through the two small interfaces below. Holds all messages and state. It is not in busd's data path: agents talk to it directly.
2. **Bus adapter** — a local process per agent session, exposed to the agent as a stdio MCP server. It owns the agent's private key, signs outgoing messages, verifies incoming ones, maintains the TOFU store and the handled-message set, and runs a background reader that buffers incoming messages within a fixed memory bound (§7). It calls busd for private-channel management (§10b).
3. **busd** — the control service. It holds the admin principal and does what needs admin rights: private channels and their membership, granting and revoking broker access, creating the agents' consumers, serving history and registry reads, and the normal path for busctl's management commands. It never carries messages (§10b). It is not a bus agent: it has a service name and a broker account of its own, and signs no bus messages.
4. **busctl** — admin and human CLI: register identities, create channels, tail and inspect history, manage known keys. It goes through busd; direct admin access is the recovery path (§10).
5. **Delivery hooks** — per-harness glue that makes agents notice new mail (see §7).
6. **Bus monitor** — a small web service that shows messages flowing through the bus (see §10a). It runs as a separate process with its own read-only broker principal and its own signing identity, `monitor@<owner>` (§10a); it may share busd's binary.

**Decision: busd is a control plane, not a gateway.** Agents send and receive messages with the broker directly. Signing, verification, TOFU, the inbox state and the delivery hooks stay in the adapter. busd exists because some actions need admin rights an agent must never hold (private channels, broker access). Putting every message through a service was considered and rejected: it adds a hop and a single point of failure and would move the ack logic (§4, §6, §7) into the service.

**Decision: one internal boundary to the broker.** The spec describes a broker-neutral model (§4: channels, read cursors, handled messages, accounts and access grants) and maps it to JetStream in one place, the binding (§4a). In code, two small Go interfaces sit between the bus and the broker, so the broker's client library stays in one package:

- a **message transport**: publish a record to a channel; consume channels with explicit per-message acknowledgement; read history read-only, without touching a cursor;
- an **admin interface**: create channels, grant and revoke access, list the access a channel has, create and delete accounts and the read cursors an account owns.

The binding is the only implementation and lives in one package, `internal/broker`, the only one that imports the broker's client library. Envelope, signing, TOFU, the handled set, the inbox, hooks, busd's logic and busctl's commands never import it (`internal/itest` also does, to test against a real broker). The interfaces speak in bus terms (channel kind and name, account, acknowledgement), never in subjects or access rules; the mapping lives in the binding. Another broker (Kafka was the first one) is a possible later option, not a design goal, and the interfaces are not shaped for it.

**Decision: the broker is its own process, not embedded in busd.** `nats-server` runs separately (§4a), so a busd crash or restart doesn't drop agent connections. On the dev machine it is a plain binary started by a script or user service unit; the server configuration and that start script are the only runtime-specific files. If a container runtime is used later, a Pod manifest replaces the start script. Nothing in the Go code or busctl depends on how the process starts. busd runs on the broker's host (it rewrites the server's authorization file and reloads it, §4a); the monitor is a plain binary and can run anywhere.

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

**Channels.** A channel is an ordered, durable, append-only log of records. **Ordering guarantee:** history is read in the channel's canonical order, and messages are first delivered in it; a redelivery after a crash can arrive later (§4a). Order-sensitive decisions, such as folding request status changes (§5), use the canonical order from history, never arrival order. Nothing else is promised across threads.

| Channel | Purpose | Retention |
|---|---|---|
| registry | Agent ID → key history (current and former public keys, rotation links, revocations), metadata; one record per agent ID | latest per ID |
| `general` | Default channel, all agents | forever |
| group channel `<name>` | Named group channels (`lib-x`, `release`, …): open channels any agent may read and write | forever |
| DM `<agent-id>` | Inbox for one agent | forever |
| private channel `<channel-id>` | Closed channel, members only (§4b) | forever |
| catalogue | Public catalogue of group channels and descriptions; one record per name | latest per ID |

Notes:

- Channels are created only by the admin principal (busctl or busd). Nothing creates a channel by accident.
- **Decision: group channels are open channels**, like an open chat channel: every agent may read and write every group channel without asking. Joining only subscribes: it needs no approval, though busd sets up the read cursor (§4a). What an agent may *do* with a message is limited by the safety rules (§8), not by channel access. Closed channels are private channels (§4b).

**Record kinds**

Not every record is a signed envelope. Each channel carries one kind, and readers (the monitor included) dispatch by channel:

| Channels | Record | Schema owner | Verification |
|---|---|---|---|
| `general`, group channels, DMs, private channels | signed conversation envelope (§5) | adapter | the rules in §6 |
| registry | `AgentRecord`: key history (§6) | busctl / busd | written only by the admin principal and trusted through the broker access rules; the rotation links inside are signature-checked (§6) |
| catalogue | `TopicRecord` (name, description, `updated_at`) | busctl / busd | admin-written, trusted through the access rules |

Milestone 1 already writes `AgentRecord` (`agent_id`, `public_key`, `fingerprint`, `registered_at`) and `TopicRecord` (the catalogue record). `AgentRecord` gains a `keys` list, one entry per key: `public_key`, `fingerprint`, `valid_from`, `rotation_sig` (the previous key's signature over the new entry; empty for the first key and for a key added after a revocation, §6) and `revoked` (with `revoked_at`). The existing `public_key` and `fingerprint` stay and always name the current key, so a record without `keys` is read as a history of one key.

**Read cursors and handled messages**

- Each agent has one read cursor per channel it reads: its DM channel, `general`, and the channels it has joined. A cursor is the broker's record of which messages of that channel the agent has acknowledged; it is stored on the broker under the agent's own name.
- **Decision: the adapter acknowledges each message itself; nothing advances automatically.** Acknowledgement is per message: acking B before A leaves A unacknowledged, and the broker redelivers it. A message is *handled* when the agent has acked it (`mark_read` also covers passing on a message without acting), when the adapter terminally rejected it (§6), or when the adapter sent it itself: its own messages come back on `general` and joined channels, and they enter the handled set at send time, with the stream sequence the publish returned, so they are not delivered to the agent again. A verified message that was handled goes into the persisted handled set, keyed by destination, verified sender and envelope ID (§6 step 4); nothing is added when first seen. A rejected or unverified delivery is recorded by its stream sequence, never by its envelope ID, which an attacker can copy (§6). The handled set is written before the broker ack. A message that is held (§6) is not handled: it goes to the hold store on disk and is then acknowledged on the broker, so it needs no broker state; the hold record stays until the message is handled (§6). A crash therefore redelivers unhandled mail, and the handled set skips handled mail whose broker ack was lost.
- **Decision: the adapter persists its joined channels** locally next to its key.
- **First start and recovery.** On its first start the adapter reads its DM channel and `general` from the beginning. When it joins a group channel, the default stays latest (a private channel starts at the beginning, §4b); `join(channel, from="earliest")` exposes history (§9) without redelivering it as new mail. If the broker has no stored cursor for a channel (first start, or lost), the binding restarts it where it began: the beginning for DM, `general` and private channels, the point of joining for a group channel (§4a); the handled set skips what was handled before.
- The broker must keep cursors effectively forever.

**Accounts and access**

- Every client has its own broker account. An agent's account may: read and acknowledge under its own name, read its own DM channel, write any DM channel, read and write `general` and every group channel, read the registry and the public catalogue (through busd), and publish idempotently, so its own retries are not written twice within the broker's window. It gets access to a private channel only when busd grants it (§4b).
- Only the admin principal writes the registry and the public catalogue and creates channels. Agents hold no admin rights and cannot choose broker resource names. They cannot create or delete their own read cursors either: busd provisions them (on registration, `join` and invitation) and removes them when access ends (§4a, §10b).
- The monitor's account may pull from its own durable consumer `bus-monitor` over every channel; busd creates that consumer, the monitor creates none and never touches an agent's consumer (§4a, §10a).
- **Decision: encrypted connection to the broker.** Every client (`agent-bus`, `busctl`, `busd`, `bus-monitor`) connects over TLS; the broker has no plaintext listener. The broker is assumed to be on the local network. If a machine is elsewhere, the owner sets up a secure tunnel or VPN to reach it; the bus does not provide one. busctl creates a private CA and the broker's certificate during setup (`busctl tls init`). Clients trust that CA only, given by a `--ca-cert` flag (default `~/.config/agent-bus/ca.pem`). No public CA is needed on a LAN.

Note that broker access and message signatures do different jobs: access rules control who can reach what on the broker; signatures prove authorship end-to-end and survive replication, export, and a compromised broker.

---

## 4a. NATS JetStream binding

How the model of §4 maps to NATS JetStream. Everything NATS-specific is here and in `internal/broker`.

**Decision:** NATS JetStream (Apache 2.0, CNCF), run as `nats-server` v2.15.3 in its own process, with the Go client `github.com/nats-io/nats.go` v1.56.0 (`jetstream` package, pure Go). One owner needs no JWT accounts or `nsc`: the server's authorization config lists static users with passwords and per-user subject permissions, in two static accounts (below).

**What the spike proved, and what it did not.** Proved (all passed): TLS with the private CA; static users with per-user subject permissions; per-agent consumers with a pinned filter in the create subject; per-message acks (B before A, redelivery after `AckWait`, state kept across a server restart); `NakWithDelay` surviving a restart; `Nats-Msg-Id` dedupe; infinite retention; KV compare-and-set; a plain stream next to KV; core request/reply; runtime permission change by reload (live grants change, a removed user is disconnected); a denied JetStream call shows up only as a timeout plus an async error. **Proved later, by the milestone 1a integration test against a real server (`make itest`)**: the two-account setup, per-role grants with no consumer create, delete or update, `MaxDeliver` -1 and `MaxAckPending` at 500, `Nats-Expected-Last-Subject-Sequence` on `LEDGER`, reload confirmation through the monitoring endpoint, the stream sequence in the delivery metadata, the admin reading the stream's last sequence, and the raw-NATS part of the §12 reply-destination tests. The redirect result: a record pulled onto a subject of `BUS` through a reply subject is delivered to whoever listens there but is **not** stored again (the stream's message count and last sequence stay the same), and only JetStream API responses sent to a reply subject are stored, with the server's payload. **Not proved yet**: busd itself reading the stream's last sequence and serving history. The signed-busd parts of those tests (nonces, method checks, idempotency) are proved in milestone 3 (§11).

**Two accounts.** NATS delivers JetStream responses to a reply subject the *caller* names, and the permissions only check the request subject. So any user in an account that can call a JetStream read API or pull from a consumer can make the server write a stored payload to any subject of that account, `$KV.*` and `ledger.*` included. Authoritative data therefore lives in a second account, which no agent can reach. (Within one account the redirect writes only JetStream API responses, with the server's payload; a pulled record is delivered but not stored again, as the integration test showed.)

- **`BUS`:** stream `BUS` (every conversation channel), agents, the monitor's read-only user, busd's message connection, and the owner's posting user.
- **`CONTROL`:** the KV buckets `registry` and `catalogue`, and stream `LEDGER`. Only busd's control connection and the `control-admin` user exist in it. There is no import or export between the accounts.

Agents never touch `CONTROL`: busd serves what they need over request/reply, signed with busd's service key (§10b). Neither account lets an agent create a resource, a consumer included.

**Naming.** Alphabets: an agent ID is `[a-z0-9-]+@[a-z0-9-]+`. Its *agent token* replaces `@` with `_` (`lib-x@bob` → `lib-x_bob`; `human@alice` → `human_alice`), so a token has exactly one `_`. A group channel name is `[a-z0-9-]+` and a private channel ID is 26 characters `[a-z2-7]`. No name contains `__`, so the parts of a consumer name split unambiguously at `__`. One function does the mapping. The token is the NATS user name and the DM subject suffix. A private channel's subject carries an opaque ID chosen by busd, never the channel's name (§4b).

| Channel | Where | Retention |
|---|---|---|
| registry | `CONTROL` KV bucket `registry`, key = agent token | latest per key (KV history of up to 64 is a bonus; `AgentRecord` already holds the key list) |
| `general` | subject `general` in stream `BUS` | infinite |
| group channel `<name>` | subject `topic.<name>` in `BUS` | infinite |
| DM `<agent-id>` | subject `dm.<agent-token>` in `BUS` | infinite |
| private channel `<channel-id>` | subject `private.<channel-id>` in `BUS` | infinite |
| catalogue | `CONTROL` KV bucket `catalogue`, key = channel name | latest per key |
| membership ledger (busd only, §4b) | `CONTROL` stream `LEDGER`, subjects `ledger.>` | infinite |

**Stream `BUS`.** One stream over `dm.>`, `general`, `topic.>` and `private.>` (streams cannot overlap subjects). File storage, limits retention with `MaxAge=0`, `MaxBytes=-1`, `MaxMsgs=-1` (infinite).

**Channels exist in the catalogue, not in the broker.** Group channels exist when they are in the catalogue (admin-created, §4); a DM channel exists when its agent is registered. Consumers exist only for these channels, and the adapter refuses to publish to a channel that is not in the catalogue (or to an unregistered agent). **Decision (MVP, until busd serves the catalogue):** `busctl channel create`, `channel join` and `directory export` write a catalogue snapshot, the file `channels` in the directory (§4c copy model): each group with the agents that hold a read cursor on it. The adapter reads it at start to subscribe to the groups that list it (`--groups` adds more, so it is optional), refuses to publish to a group it neither reads nor finds in the snapshot ("unknown channel"), and warns in the result when an explicit target is not listed as a reader, because a message to a channel its target does not read is never seen. The broker does not enforce this: a misbehaving agent with the `topic.*` or `dm.*` publish grant can still append to an uncatalogued subject, but no consumer reads it, `history` (served by busd) refuses it, and nobody receives it.

**Order.** The canonical order of a channel is the stream sequence. A first delivery follows it; a redelivery (after a crash, an `AckWait` expiry or a reconnect) can arrive after later messages. The promise of §4 is therefore canonical order for reading history, and first-delivery order otherwise. Anything that must be order-correct, such as folding request status changes (§5), uses the stream sequence from authoritative history, never arrival order. The stream sequence is also what private-channel membership is checked against (§4b).

**Delivery: consumers and acks.** busd provisions every consumer; an agent only pulls from its own.

- busd creates an agent's durable pull consumers: on register (DM `<t>__dm` filtered on `dm.<t>`, and `<t>__general` filtered on `general`), on a group-channel `join` (`<t>__g__<name>` on `topic.<name>`) and on a private-channel invite (`<t>__p__<id>` on `private.<id>`). It deletes them on `leave`, removal and incident revocation (which also cancels the pulls that are waiting, as far as the server allows). Each has one filter subject and a fixed configuration the agent cannot change: explicit acks, `AckWait` 1 minute, `MaxDeliver` -1, `MaxAckPending` 500 (the inbox buffer's message limit, §7), `MaxWaiting` small, no `InactiveThreshold` (read state is kept forever, §4). busd also creates the monitor's durable consumer `bus-monitor` (filters `dm.>`, `general`, `topic.>`, `private.>`); the monitor has no consumer-create grant and never uses an agent consumer name (those contain `__`).
- Starting positions: DM, `general` and private channels from the beginning; a group channel from the sequence right after its join, which busd records in the ledger as the join boundary (the `BUS` stream's sequence at that point, §4b). A lost consumer is recreated by busd from that boundary (or from the beginning for the others), so unread mail is not skipped; the handled set skips what was handled.
- The adapter pulls with `Fetch`, with a batch no larger than the free room in its inbox buffer and a short request expiry.
- **Acks are per message.** Acking B before A leaves A pending; A is redelivered after `AckWait`. Ack state survives a server restart.
- The adapter's state machine, per delivery. Before verification a delivery is known only by its stream sequence; the envelope ID is attacker-controlled until the signature checks out (§6).
  - a stream sequence already recorded for this destination as handled, rejected or held: ack at once;
  - otherwise parse and verify (§6 steps 0 to 3). A terminal rejection records the stream sequence, then acks. A retryable case (unknown sender, changed key) writes the hold record, keyed by stream sequence, then acks;
  - a verified envelope is then looked up by (destination, verified sender, envelope ID): in the handled set, record the sequence and ack at once; in the hold store, ack at once, it is safely on disk; already in the inbox buffer, send `InProgress` and don't queue it twice;
  - a new key: queue it. While it sits in the buffer the adapter sends `InProgress` every 20 seconds, well inside `AckWait`, so a redelivery follows a crash or a stalled connection, not slow reading.
  - handling a message: when the agent marks it read (`mark_read`), ack on the broker first (a double ack, so the broker confirms) and only then write the handled set (with the sequence), so a failed broker ack leaves the message unread; a crash between the two can only lose a message the agent had already acked. A terminal rejection writes the handled set first, then acks.
  - holding a message (§6): write its hold record first, then ack it on the broker. A held message never occupies the broker's ack-pending, so `MaxAckPending` is not used up by quarantined mail.
- Early redelivery after a pause or reconnect is normal and handled by the rules above.
- The membership ledger and the join boundaries make busd the only party that can re-create read state; a consumer an agent had before it was removed is not reused (a new invitation makes a fresh one over the retained history).

**History.** There is no history consumer. `history` and `join(from="earliest")` (§9) are served by busd over request/reply (`busd.history`): busd checks that the caller may read the channel (open and catalogued, its own DM, or a private channel it belongs to; the owner and the monitor identity may read all). A *thread* history read of a DM thread returns the records of that thread from both participants' DM channels, merged by stream sequence, to a participant (sender or recipient of the thread's records), and, as for every channel, to the owner and the monitor identity, so a DM request split across two DM channels can be rebuilt after a restart. busd reads `BUS` by an ordered consumer of its own, and returns a page of records with their stream sequences, bounded by NATS's maximum payload, with `before` as a stream sequence. The reply is signed by busd; the adapter still verifies each record's signature and, on a private channel, applies the membership rule of §4b to each stream sequence (§6). It moves no read position.

**Dedupe.** The adapter publishes with `Nats-Msg-Id` set to the canonical destination subject plus the envelope ID (`jetstream.WithMsgID`), so an ID reused on another subject is not mistaken for a duplicate. The stream's `Duplicates` window is set explicitly to 24 hours (it must not exceed `MaxAge` if one is ever set). This is bounded, best-effort suppression of the adapter's own retries; it is not authorization, and a retry after the window appends a second record. Envelope verification and the handled set (§6 step 4) stay in force. `LEDGER` relies on busd's operation intents instead (below).

**Registry, catalogue and ledger (all in `CONTROL`).**

- Only busd (and `busctl --direct`) writes them. Writes to the registry and catalogue use compare-and-set (`Create` / `Update` with the revision).
- The membership ledger (§4b) is a plain stream, not KV: KV keeps at most 64 revisions per key and the ledger is an append-only log. An operation is published to `ledger.channel.<id>` with `Nats-Expected-Last-Subject-Sequence` set to the version it expects, so the broker refuses a stale one. The ledger also holds the group join boundaries, the reload generations (below) and busd's operation intents. Every confirmed join, removal and group join entry carries the `BUS` stream's sequence at that moment (§4b). An operation intent, `ledger.idem.<caller-token>.<idempotency-id>`, is created once (expected last sequence 0) *before* any effect, with the hash of the semantic request and every resource ID busd assigned (a new channel's ID, say). Recovery finishes that same operation; the result is derived from the operation's confirmation events (§10b). This works beyond the dedupe window and covers every mutating busd method.
- busd reads the registry and the ledger through its own ordered consumers.

**Accounts and permissions.** Every client is a static user with a password (stored bcrypt-hashed) and its own permissions. Credentials sit with the adapter's key, never in a project directory. NATS authentication identifies the account; the Ed25519 signature (§6, §10b) is the identity. A publisher can name any reply subject, so busd's responses go where the caller says; that is harmless because they are signed and inbox tokens are random. A request can also arrive by redirection; busd authorises from its signed content, not from the subject (§10b).

| Role (account) | Operation | Subjects |
|---|---|---|
| Agent `<t>` (`BUS`) | send and publish | `dm.*`, `general`, `topic.*`, and `private.<id>` for each private channel it belongs to |
| | receive, per own consumer `<c>` | `$JS.API.CONSUMER.MSG.NEXT.BUS.<c>`, `$JS.API.CONSUMER.INFO.BUS.<c>` (to notice a lost consumer), `$JS.ACK.BUS.<c>.>`; the consumers are `<t>__dm`, `<t>__general`, `<t>__g__<name>` for every catalogued group and `<t>__p__<id>` for each private channel it belongs to |
| | replies | subscribe `_INBOX.<t>.>` (the client uses `nats.CustomInboxPrefix("_INBOX.<t>")`) |
| | call busd | publish `busd.nonce`, `busd.channel.create`, `busd.channel.list`, `busd.channel.members`, `busd.channel.membership`, `busd.channel.invite`, `busd.channel.remove`, `busd.channel.leave`, `busd.channel.join`, `busd.history`, `busd.registry.get`, `busd.registry.changes`, `busd.catalogue.list`, `busd.agent.rotate` |
| | never | consumer create, delete or update; any `$JS.API.DIRECT.GET`; any wildcard consumer, `MSG.NEXT` or ack grant; any `$KV.*` or `ledger.*` subject; the owner-only busd methods |
| Owner `human_<owner>` (`BUS`, used by busctl) | post, and the agent's busd calls | as an agent for DMs, `general` and groups; `private.<id>` where busd granted it (§4b); no consumers |
| | owner-only busd methods | publish `busd.nonce`, `busd.agent.register`, `busd.agent.revoke`, `busd.agent.rotate`, `busd.channel.manager`, `busd.channel.create_group` |
| Monitor `monitor_<owner>` (`BUS`; also used by `busctl` read commands) | read, from the consumer busd made | `$JS.API.CONSUMER.MSG.NEXT.BUS.bus-monitor`, `$JS.API.CONSUMER.INFO.BUS.bus-monitor`, `$JS.ACK.BUS.bus-monitor.>`, subscribe `_INBOX.monitor.>` |
| | read-only busd methods | publish `busd.nonce`, `busd.history`, `busd.registry.get`, `busd.registry.changes`, `busd.catalogue.list`, `busd.channel.list`, `busd.channel.membership` (`busd.channel.membership` for all channels, with signed watermarks; `busd.channel.list` returns the private catalogue of all channels to this identity) |
| | never | any consumer create, update or delete; any publish on a message subject, any stream, KV or `CONTROL` subject; any other busd method |
| busd, message connection (`BUS`) | consumers (agents' and the monitor's), history, stream info | `$JS.API.>` |
| | its API | subscribe `busd.>`, subscribe `_INBOX.busd.>`, publish `_INBOX.>` (replies) |
| | acks | none for ordered consumers; a durable consumer of its own needs `$JS.ACK.BUS.<name>.>` |
| busd, control connection (`CONTROL`) | registry, catalogue, ledger | `$JS.API.>`, `$KV.>`, `ledger.>`, subscribe `_INBOX.busd.>`, and `$JS.ACK.<stream>.<name>.>` for a durable consumer of its own |
| `bus-admin` (`BUS`) and `control-admin` (`CONTROL`), used by `busctl --direct` over two connections | everything in its own account | publish and subscribe `>`; NATS usernames are server-wide, so the two are distinct users with separate passwords, which live only in the owner's config (§10) |

The monitor has its own narrow signing identity, `monitor@<owner>`, with its own Ed25519 key registered with the role `monitor`: busd lets it call only the read-only methods above. It never uses the owner's key. busd, not the monitor, creates the monitor's consumer, so the monitor needs no create grant. Read-only still has a limit: any raw JetStream reader, the monitor included, can make the server deliver a payload it can read to any subject of `BUS` it can name. That cannot be closed by grants within one account. The receiver checks (§4b, §6) are what make it harmless. The grants for the busd methods and the consumers are generated; the per-channel part is rewritten by reload (below).

**A denied call is a timeout.** JetStream answers a call the server refused only by silence: the client sees a timeout, and the real reason arrives asynchronously as a permissions error on the connection. The adapter therefore makes every call under a short context, reads the connection's async error handler, and reports the reason ("not a member", "permissions violation for …"). Such an error is never a handled message (§4b).

**Known limit of one account.** Any agent that can store a payload (a DM to itself) and pull from its own consumer can make the server deliver that payload to a subject of its choice in `BUS`, including a private channel's subject it may not publish to. The integration test showed that the server does **not** store that delivered copy: the stream's message count and last sequence stay the same, so no consumer of the private channel ever reads it. Only a JetStream API response sent to a reply subject is stored, with the server's payload, not the sender's. Listeners on a subject still see a redirected payload, so the publish grants prove nothing about authorship on a private channel, and receivers keep checking: besides the signature, `from` against the registry and `to` against the channel, they check that the message's stream sequence lies inside one of `from`'s membership intervals (§4b, §6 step 6). That check is defence in depth: it covers a future grant mistake and anything that does store.

**TLS.** The server's `tls { cert_file, key_file }` block is the only listener (no plaintext port). The certificate comes from the private CA (`busctl tls init`, §4); clients trust that CA as their root (`nats.Secure` with a `tls.Config{RootCAs}`). TLS protects the connection to the server; it does not authenticate busd (§10b).

**Server configuration and reload.** `nats-server.conf` holds the static settings (listener, TLS, the two accounts, JetStream store directory, a local monitoring endpoint) and includes `auth.conf`, the users and permissions of `BUS`. busd and `busctl --direct` are the only writers. A change is one serialised operation: take the file lock; write a candidate file with a new generation number; validate it (`nats-server -t`); atomically replace `auth.conf`; reload (`nats-server --signal reload` with the pid file); then confirm that the server loaded it (the monitoring endpoint's config load time moves past the reload). Only then is the change reported done. The ledger records the desired and the confirmed generation, so after a crash busd finishes an unconfirmed reload before anything else. A reload changes the permissions of live connections: a revoked grant removes the subscription at once, and removing a user disconnects it. Consumer cleanup (deleting the member's consumers, which cancels waiting pulls) is part of finishing a revocation, not an afterthought. Reconciliation (§4b) reads the confirmed generation, not just the file. busd and `nats-server` run on the same host. Because the server is a separate process, a busd crash or restart drops no agent connection.

**Private channels.** A member gets the channel's publish subject and its consumer grants (`<t>__p__<id>`); busd creates the consumer after the reload is confirmed. Removal rewrites `auth.conf` without them, reloads and confirms, then records the `BUS` stream's last sequence as the removal boundary (§4b) and deletes the member's consumer. The `private.>` subjects are in no wildcard grant. A new group channel adds its `<t>__g__<name>` grants to every agent in the same way.

**Deploy.** `nats-server` runs on the dev machine as a plain binary, started by a script or a user service unit (`deploy/`: `nats-server.conf` and the start script; `auth.conf` is generated, mode 0600). If a container runtime is used later, a Pod manifest replaces the start script and reload signals the container; nothing in the Go code or busctl depends on how the process starts. busd and the monitor are plain binaries next to it. busd runs supervised (a user service unit with automatic restart); there is no high availability, and §10b says what works while busd is down.

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

- **Decision: unsubscribe and leave are different.** `unsubscribe` only stops the adapter fetching a channel and keeps membership. `leave` on a private channel ends membership: busd removes the member's access, and the tool reports done only after the broker has confirmed the change. `leave` on a group channel ends the subscription: busd deletes its read cursor (§9); there is no membership.
- When membership ends, by `leave` or by removal, the adapter drops that channel's pending and held mail and stops fetching it. An access-denied error from a channel is not a handled message: the adapter stops fetching the channel, alerts once, does not record it as handled or acknowledge anything for it, and does not reconnect in a loop.
- No retroactive secrecy: what a member already read stays with it, and requests already in flight may succeed for a moment after removal. Removal cannot erase anything.

**Metadata.** **Decision:** private names, descriptions and requests never go to the public catalogue. The private catalogue (id, name, description, manager, members) is kept by busd and served only to a channel's members and the owner. Requests in a private channel stay inside it: the `requests` tool derives them from the channel's own history through the read-only path (§5, §6).

**Membership ledger.** busd keeps one durable ledger of channels and members, with one writer (busd). It also records where each group-channel subscription began (§4a). Every change is an operation with an idempotency ID and the channel version it expects. Operations on one channel run one at a time, and an operation with an old version or an already-seen ID is rejected or answered with the earlier result, so a stale retry cannot restore removed access. A change is recorded in the ledger first, applied to the broker second, and marked confirmed last. A confirmed join or removal records the `BUS` stream's sequence at that point: the join boundary is read before the grant is applied (so it is never later than the member's first possible message) and the removal boundary after the removal is confirmed (so it is never earlier than the member's last possible message; the short gap this leaves is described below). A crash in between leaves a pending change that busd finishes on its next start. busd also reconciles the actual broker access of all private channels against the ledger on start and every five minutes: it grants what is missing and revokes what is extra, and never grants access to an account that no longer exists.

**Membership by broker order.** **Decision:** the broker's publish grants do not prove who wrote a record on a private channel (§4a, known limit), and the sender's `ts` proves nothing, so receivers check broker order instead. The broker gives every stored message a stream sequence the sender cannot set. A record redirected onto a private subject through a pull reply subject is delivered to listeners but not stored again (§4a, proved by the integration test); the interval check stays as defence in depth, for a future grant mistake and anything that does store. From the ledger, busd knows each member's membership intervals: from its join boundary to its removal boundary, or open while it is a member. A receiver accepts a message on a private channel only if its stream sequence lies inside one of the sender's intervals; the pinned `human@<owner>` is always allowed.

busd serves the intervals to the channel's members and to the monitor identity, which may read all channels (`busd.channel.membership`, signed like every busd reply) with a watermark: "complete up to sequence N", meaning no boundary at or below N is missing. The watermark never passes the start of a change that is still unconfirmed. The adapter caches the answer on disk. For a message with a sequence above the cached watermark it refreshes first; if busd is unreachable it holds the message and never rejects it. A message outside the sender's intervals is terminally rejected and alerted (§6). History reads and status folding (§5) apply the same rule.

The boundaries are logical cutovers, not exact access cutovers. The interval is (join, removal]: a sequence above the join boundary and at or below the removal boundary. Each side has a short transition window. At join, the boundary is read just before the grant is applied, so a message stored in the gap before the grant takes effect would count as a member's, though the sender could not yet publish it; only the member's own key can sign it, so it is the member's own message. At removal, the boundary is read after confirmation, so a message a former member manages to store in the channel in the gap between the revocation taking effect and the boundary being recorded would fall inside the interval. If busd crashes before it records the removal boundary, that gap lasts until recovery records it, because the interval is still open. A boundary is immutable once recorded. busd serialises watermark snapshots with pending membership changes, so a snapshot never covers a sequence past a pending change.

So a former member's message, whatever its `ts` and whether newly written or replayed, that reaches the channel after its removal boundary is recorded gets a sequence above that boundary and is rejected. What the member stored while it was a member stays valid.

**Ledger safety.** busd never revokes access on the strength of a missing, empty or reset ledger. In that case it refuses to revoke anything and says why. The owner either restores a ledger backup or runs `busd reconcile --adopt`, which takes the current broker access of the private channels as the truth and asks the owner to name each channel's manager.

---

## 4c. Agent enrollment with mTLS (planned)

**Decision** (owner, 2026-11-24, after a design review): agents get client certificates from the bus CA through a Puppet-style enrollment flow, replacing agent passwords. Planned after agent removal, the protocol fixes and the minimal monitor API.

- **Request.** `agent-bus enroll --broker <host> --agent <id>` creates the TLS key and CSR on the agent's machine (an existing agent keeps its signing key, ID, consumers and local state; a new one also creates its Ed25519 key). Keys never leave the machine. The request carries the agent ID, CSR, public signing key with proof of possession, hostname and a fresh nonce; its approval code is derived from all of these, so it cannot be reused for another request. `--waitforcert` keeps retrying until the request is approved or rejected.
- **Trust in the broker.** The CA fingerprint is confirmed against the value the central machine shows (or the CA certificate is copied once), never taken from the enrollment endpoint alone.
- **Enrollment login.** A shared enrollment login on a separate listener may only send a request and receive its own reply (dedicated inbox namespace), with size, pending-count, expiry and rate limits and no BUS, JetStream or admin access. It requests approval; it never proves identity. Optional autosigning: a request carrying a configured enrollment secret (like Puppet's challenge password) is approved automatically; off by default.
- **Approval.** The monitor's "pending agents" list (ID, hostname, fingerprints, code) or `busctl ca list` / `busctl ca sign <id>` / `busctl ca reject <id>`. The monitor holds the CA key (mode 0600, never served, logged or exported); it is local-only and exposed only through a reverse proxy. Approvals are single-use and expire, and never overwrite an existing identity silently.
- **Certificates.** Built by us from the approved ID, never copied from the CSR: client-auth only, not a CA, bounded validity, one URI SAN that nats-server's `verify_and_map` maps to the agent's user. A renew/reissue command exists before certificates expire.
- **Broker.** The mapped mTLS listener and the password/enrollment listener are separate; admin users and the monitor get certificates before agents are switched; password logins are retired only after certificate login is verified (documented rollback). The generated authorization files model explicit roles (agent, admin, monitor, enrollment) and authentication modes, round-tripped without loss.
- **Removal.** Removing an agent drops its mapping and reloads; a removed ID is never reused while certificates issued for it are valid (tombstones in the registry). Agents learn about new and removed agents through a directory refresh and keep revoked fingerprints locally, checked even for pinned keys. **Implemented for password accounts** (`busctl agent remove`, `busctl directory export`, the adapter's local refresh; docs/busctl.md): the directory is copied by hand until busd serves it over the bus.
- **Docker.** One supported topology lets the monitor reload nats-server (for example sharing the broker container's PID namespace); config is mounted as a directory so atomic replacement stays visible.

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
    "targets": ["lib-x@bob"],      // agents the message is aimed at, or ["@all"]; never empty
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

**Message types:** `message`, `request`, `question`, `answer`, `feedback`, `bug_report`, `feature_request`, `status`, `ack`, `key_rotation`, `invite`.

**Decision: targets are part of the signed envelope** (owner, 2026-11-24). `to` is the channel a message goes to; `targets` says whom it is aimed at: a non-empty list of registered agent IDs without duplicates, or the single value `"@all"` for everyone who reads the channel (never combined with IDs). Every message has targets. The sending adapter fills what the sender left out before signing: a DM targets exactly its recipient, a reply adds the author of the message it replies to (an explicit `reply_to` always; the parent inherited by §8 rule 3 only for closing and plain messages, because a new question or request without `reply_to` is a new topic for `@all`), and a response-bearing message to a channel with no targets gets `["@all"]`. The tools and the monitor read `@<agent-id>` and `@all` from the text when no `targets` are given. Anything else with no target is refused with "name a target or @all". A receiver rejects a record with empty or malformed targets as malformed (§6), and a DM whose targets are not exactly its recipient. A message *targets* an agent if its ID or `@all` is in `targets`; the receiver derives nothing else.

**Decision: response-bearing types.** `request`, `question`, `feature_request` and `bug_report` expect an answer; `ack`, `answer` and `status` close a loop and expect nothing back. The adapter's instructions to the agent: acknowledge a request aimed at you at once with a short `ack` reply, deliver the result later as an `answer` reply to the same message, and never answer an `ack`, `answer` or `status` unless something is unclear. The hop limit (§8 rule 3) stays as the backstop. Under `--deliver=targeted` an untargeted question would reach nobody, hence the `@all` default above; explicit targets always win.

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

The pinned owner (`human@<owner>` with `source=pinned`) may make any transition in the table. `rejected` and `done` are terminal. **Decision:** one check, taken from this table, runs on every outgoing and incoming status path: `set_request_status`, and also generic `send` / `publish` with `type=status`. The adapter refuses an outgoing status that fails it, and an incoming one that fails it is terminally rejected (§6) and alerted. The current state is derived from the thread: the request's first message counts as `proposed`, and each later signed `status` message moves it on. The adapter records its own sends in the handled set, so a requester knows its own `proposed`. If the history needed for a check is missing (a DM request split across two DM channels, or a group channel joined at latest), the adapter fetches it through the read-only history path (§6). If that fails, an incoming status message is held (retryable, released later) and never rejected terminally, and an outgoing status is refused with "thread history unavailable, retry later" (§10b).

**Open requests are derived, not projected.** The signed status messages in the thread are the only record. The `requests` tool and `busctl requests` fold thread history, in canonical order, through the read-only path (§6), over the channels the caller can read, newest first and bounded by a limit. A private channel's requests come from that channel's own history, with the membership rule of §4b. Authorization never comes from a derived view. A rebuildable projection (a record per request, so listing needs no scan) is a possible later optimisation if scanning becomes slow; v1 has none.

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
   - **Unknown, `--no-auto-accept-new-keys`:** hold the message (quarantine) and raise an alert. It is held: written to the hold store on disk, then acked on the broker; it is not handled. The owner pins the key with `busctl pin` first. The adapter re-reads `known_agents` when it re-checks held messages (at each hook and `inbox` call; no file watch), then releases the held message (see Holds below) and accepts the sender's later ones. Held messages are stored on disk in the identity directory and do not count against the inbox buffer (§7). `busctl pin <agent-id> <fingerprint>` writes to the `known_agents` of every identity on the current machine, or of one with `--for <agent-id>`.
   - **Known, fingerprint matches a pinned key that is not in `revoked_keys`:** verify signature.
   - **Known, fingerprint differs:** look for a rotation chain in the registry (see key rotation). If the chain leads from the pinned key to this key, update the pin. Otherwise hold the message like an unknown sender's (released once the owner pins the key), log loudly, and notify the human (busctl shows it). **Never auto-accept a changed key.** Same behaviour as SSH's host-key warning.
4. Dedupe, for a message whose signature verified: skip if the key (destination, verified sender, `id`) is in the adapter's handled set (persisted locally, e.g. SQLite) or already pending in its buffer or hold store. A key enters the handled set only when the message is handled (§4), not when first seen, and the entry keeps a digest of the signed envelope: the same key with a different payload is alerted as an ID collision and not delivered. This stops replay of an old signed message into the same channel. Deliveries that did not verify never reach this step; they are tracked by stream sequence only (§4a).
5. Reject if `ts` is more than 5 minutes in the future. Do **not** reject old timestamps: offline agents legitimately read backlog.
6. On a private channel, check that the delivery's stream sequence lies inside one of `from`'s membership intervals (§4b); the pinned `human@<owner>` always passes. A message outside them is rejected; if the intervals cannot be had (sequence above the cached watermark and busd unreachable), hold the message.

**Terminal rejection** (oversized record, unsupported version, bad signature, wrong `to`, revoked key, failed request-transition check, future timestamp, stream sequence outside the sender's membership intervals on a private channel) is logged and alerted, and counts as handled: its stream sequence (never its envelope ID) is recorded and it is acked. Rejected messages are recorded locally with the reason and are not shown to the agent except as a count. A failed lookup is never an authoritative rejection: when a verification dependency is unavailable (busd unreachable for a first-contact registry lookup, a rotation-chain or revocation check, thread history or membership), the message is held, not rejected. The retryable cases, all held, are: (1) an unknown sender under `--no-auto-accept-new-keys` and (2) a changed key (both step 3, released after `busctl pin`), (3) a status message whose thread history for the transition check is missing (§5), (4) a private-channel message whose membership cannot be checked because it lies above the cached watermark and busd is unreachable (step 6), and (5) a message whose sender's key, rotation chain or revocation state cannot be looked up because busd is unreachable. A held message is stored on disk and then acked on the broker, so it never blocks later mail and a crash loses nothing; later messages are delivered and acked as usual, and the handled set skips those already handled.

**Holds.** A held message keeps its durable record, keyed by stream sequence and destination, until its handled state is committed. Release is a state change on the record (held to released), not a deletion: a released message enters the inbox, which the record backs, and the record is deleted only after the handled set write that follows the agent's `mark_read`. After a crash, released records go back into the inbox and held ones are checked again. A broker redelivery of a sequence that has a hold record, in any state, is acked at once. Only verified records may enter the logical (destination, verified sender, envelope ID) hold and dedupe indexes; a hold for an unverified message (unknown sender, changed key) is matched by stream sequence only, so a later valid record with the same ID is never acked away because of it.

**History is a read-only path.** `history` and `join(from="earliest")` (§9) read through a separate reader with no cursor of its own. It verifies signatures and applies the membership rule of §4b on private channels. It never moves a cursor, changes pins, applies statuses or rotations, or touches the handled set. History results are marked as such and are not new mail. A sender not in the registry is shown as "unknown sender, unverified".

**Key rotation:** the agent's registry record (keyed by agent ID; the latest record wins) holds its full key history. `busctl rotate <agent-id>`, run on the agent's machine, sends `busd.agent.rotate` (§10b), signed by the agent's current key, with the new key and the previous key's signature over it (the rotation link); busd appends the entry. After a revocation the old key is not accepted, so the owner sends `busd.agent.rotate` instead, signed by `human@<owner>`, with no link (see revocation), and the agent also posts an informational `key_rotation` notice. An adapter that sees a key it has not pinned walks the chain in the registry from its pinned key, checking each link's signature, and ends with the current key; it then pins the keys along the way. An adapter that was offline through several rotations catches up the same way. A chain that does not start at the pinned key is a changed key (step 3). Adapters keep former keys so they can verify backlog signed before a rotation. The monitor verifies each message against the key whose fingerprint is `key_fp`, taken from the record's history, so old messages still verify.

**Revocation:** busctl marks the key `revoked` in the agent's registry record. The entry is never deleted and there are no tombstones, so an offline adapter still sees it. Adapters consume the registry continuously, independent of first contact, and persist the fingerprints of revoked keys in `revoked_keys`. Dropping a pin does not forget a revocation, and a revoked key is never treated as a new TOFU candidate. `busctl revoke` marks the identity's current key revoked. To keep using the identity, run `busctl rotate` on its machine (it asks for the master password, because the revoked key can no longer sign the call). The new entry has no rotation link, because a link signed by a revoked key proves nothing, so receivers that know the agent see a changed key (step 3): its messages are held until the owner pins it with `busctl pin`, even with `--auto-accept-new-keys` on. A receiver that never pinned the agent treats the new key as a first contact (TOFU). Messages signed with a key before its revocation but read afterwards are rejected on purpose. The monitor shows a message signed by a key revoked later with a warning.

**Revoking a key does not revoke broker access.** The agent's broker account can still read and write what it was granted. For a compromised agent, `busctl revoke --incident` (§10) revokes the key, revokes the agent's access and deletes its account, and removes the agent's busd authority and private-channel memberships. Revoking the key alone also stops the agent's busd requests (they are checked against the current non-revoked key, §10b), but not its broker traffic.

**Weak spot:** TOFU trusts whoever speaks first. Mitigations: the registry is admin-written only; humans can pre-pin keys with `busctl pin`; busctl can print fingerprints for out-of-band checking; `--no-auto-accept-new-keys` turns TOFU off entirely.

---

## 7. Delivery: how agents "listen"

MCP agents cannot be interrupted mid-turn, so delivery has two paths:

1. **End-of-turn check (main path).** The adapter's background reader buffers verified messages. A harness hook asks the adapter for unread mail when the agent finishes a turn:
   - The hook process is short-lived, so `agent-bus pending` asks its own session's adapter over a unix socket. **Decision:** the socket belongs to one adapter process, not to the identity, and a hook only reaches the adapter of its own session. Planned approach: the hook and the MCP stdio server are both started by the harness process, so the adapter names its socket after its parent PID (the harness), and the hook walks up its own process ancestors (it may run under a shell) until it finds a matching socket. If the harness gives both the hook and the MCP server a session ID, that is used instead. The exact mechanism is finalised in milestone 5. The adapter that lost the identity lock (§3) answers nothing, so its session's hooks stay silent. No adapter, no mail, or a lost lock: the hook exits silently.
   - Claude Code: a `Stop` hook that runs `agent-bus pending --format=hook`; if mail is waiting, it returns a blocking decision whose reason tells Claude to read its inbox, so Claude continues instead of stopping.
   - OpenCode: a plugin (`deploy/opencode/agent-bus.js`) that, on `session.idle`, runs the same `agent-bus pending` call and, if mail is waiting, sends the text as a prompt with `client.session.promptAsync`. Written against the plugin types, not yet tried in a live session (docs/opencode.md).
2. **Idle listener.** An agent with nothing to do calls `wait_for_messages(timeout)`, which blocks until mail arrives or the timeout passes.

Also provide a `SessionStart` hook (Claude Code) that shows a short summary of unread mail and open requests when a session starts.

**Limitation (Decision).** Mail reaches a session while it is active (the `Stop` hook, or `wait_for_messages`) or when the next session starts (`SessionStart`). An idle, stopped session is not woken: after `wait_for_messages` times out and the turn ends, new mail waits until the next hook or session. The adapter tracks which messages it has already notified about. A hook prompts only for messages not yet notified; earlier mail that is delivered but unacked appears in the summary line without causing a prompt. A notification held back by the cooldown (§8 rule 3) stays un-notified and is retried at the next hook.

**Decision: agents stay quiet unless targeted** (owner, 2026-11-24). The hook prompts only for mail that targets the agent (§5). Mail aimed at others never causes a prompt.

**Decision: `--deliver=targeted|all`** on the adapter, default `targeted`. With `targeted`, messages that do not target the agent are verified, recorded as handled and acknowledged on the broker without ever reaching the agent's inbox, so chatter costs no context; DMs, messages that target the agent and `@all` messages are delivered. With `all`, every verified message in the channels the agent reads reaches its inbox (for closely connected projects); prompting still follows the targeted rule. The monitor always shows everything and shows a message's targets.

**Decision: presence is published by the adapter from hook events, for the owner's monitor** (owner, 2026-11-24). The owner wants to know when an agent cannot respond (session closed, process dead, model at a usage limit or API error, waiting for the human, not reacting) and what a working model is doing, without the model spending tokens. So:

- *Record.* Each adapter that holds its identity publishes one record: `state` (`working`, `idle`, `waiting_for_owner`, `rate_limited`, `error:<kind>`, `unresponsive`, `offline`), `since`, `updated` (the heartbeat), the current `activity` (tool name plus the base name of the file or the first word of a command, cut to 60 characters, never contents or whole commands), running `workers` (subagents, with a short description), the last ten activity entries and the number of unread targeted messages. It publishes on change, at most every 3 seconds, and every 30 seconds as a heartbeat; on a clean end it publishes `offline`.
- *Events.* The harness's hooks run `agent-bus event --kind=<hook>`, which reduces the hook's JSON to a compact event and passes it over the same per-session socket as `pending`, silently, with a half-second timeout, and always exits 0. Claude Code: `UserPromptSubmit` (working), `Stop` (idle), `PreToolUse` and `PostToolUse` (activity: the tool name plus a file's base name, or the base name of a command's first word only if that word is a plain name; a command starting with an assignment, quote, escape or variable shows only the tool name, so no part of a secret is published), `SubagentStart` and `SubagentStop` (workers, by `agent_id`; a worker takes the description of the latest unclaimed `Agent` tool call, else its `agent_type`, and is dropped after 60 minutes without its stop), `StopFailure` (`rate_limit` is `rate_limited`, any other `error` is `error:<kind>`), `Notification` (`permission_prompt`, `agent_needs_input` and the elicitation dialogs are `waiting_for_owner`; `quota_auto_resume_fired` is working again, `_stale` and `_disabled` are `rate_limited`), `SessionStart` (idle) and `SessionEnd` (offline). Whether a plan usage limit arrives as `StopFailure` with `rate_limit` is not documented; the `quota_auto_resume_*` notifications cover the pause Claude Code announces itself. The OpenCode plugin sends the same events from its session, error and tool events.
- *Readers decide offline and the rest.* Claude Code runs no `Stop` hook on an interrupt, so a `working` session silent for 10 minutes is shown `idle`. A reader calls an agent offline when its `updated` is more than 90 seconds old (a killed process cannot say so), or when it says `offline`. The adapter itself marks an idle agent `unresponsive` when targeted mail has gone unseen (no `inbox` or `wait_for_messages` call listed it) for 10 minutes, so an agent without hooks still shows something; without hooks it reads `idle` while its process lives.
- *Storage.* Stream `PRESENCE` in the `BUS` account, subjects `presence.*`, `MaxMsgsPerSubject` 1, direct get allowed. An agent may publish only `presence.<own token>` and may do a direct get on `presence.*` (so `list_agents` can show others' state, and nobody can write another agent's presence). The admin watches `presence.*` live and reads the stored records at start. A stream with one message per subject rather than a KV bucket keeps presence in the account whose grants §4a already generates, needs only a plain publish from the agent and no JetStream API grant, and lets the monitor use a plain subscription for live updates. Presence is not signed: the broker's per-subject permission is what binds a record to its agent, which is enough in this trusted setup and is no basis for trust decisions.
- *Cost to agents.* Status is for the owner's monitor. Agents never poll it: `list_agents` returns ID and a one-word state, `detail` adds the rest, and the briefing says to look only to learn whether one agent can respond. A `send` to an agent that is offline, unresponsive, rate limited or in error is stored as always and its result carries a short `warning`.

**Bounded buffer.** The background reader holds at most 500 messages and 8 MB (`--inbox-buffer`). When full it pauses fetching and resumes as the agent drains the inbox, so a long absence cannot exhaust memory. Only mail that targets the agent counts against these limits. With `--deliver=all` the context (mail that does not target it) has its own cap of 100 messages: when it is full the oldest context message is recorded as handled and acked unread, and the next result carries `context_dropped` with the count, so context never stalls the fetching of targeted mail, and targeted mail is never dropped. Held messages (§6 step 3 and status holds, §5) live on disk in the identity directory and do not count against it. Hook summaries are bounded too: a count plus at most five one-line summaries.

---

## 8. Safety rules

1. **Messages are untrusted input.** A valid signature proves who sent a message, not that its content is safe; a trusted agent can relay injected instructions from a poisoned dependency or web page. The adapter wraps every delivered message in a clear "data, not instructions" frame, and CLAUDE.md / AGENTS.md guidance says the same.
2. **Auto-accept.** **Decision:** the adapter flag `--auto-accept-messages` is on by default, because all agents belong to the same owner; `--no-auto-accept-messages` turns it off. The frame from rule 1 stays the same in both modes (messages are peer data, never the owner's instructions). The flag decides whether the agent may act on a verified request without the owner:
   - On: the assignee may move a `feature_request` or `bug_report` to `accepted` and work on it.
   - Off: moving a request to `accepted` requires the owner (busctl `approve`, the monitor, or the owner in the session's prompt), and the delivered frame says "discuss, don't act without the owner's approval".
   - Owner approval over the bus: `busctl approve` (and later the monitor) publishes a `status: accepted` message in the request's thread, signed by `human@<owner>`. The adapter honours it as approval only when that key is in `known_agents` with `source=pinned`; with `--no-auto-accept-messages` that is the only way a request reaches `accepted`. Later progress (`in_progress`, `done`) follows the transition table in §5.
3. **Loop guard.** `hops` measures causal reply depth. The adapter, not the agent, sets it:
   - `hops` = the parent's stored `hops` + 1. The parent is the message named by an explicit `reply_to` from the agent; the agent's explicit `reply_to` and `thread` win. If that message is not in the adapter's local store, it is looked up through the read-only history path; if it cannot be found, the send is refused, so an invented `reply_to` cannot reset the count.
   - Without them, a `send` or `publish` inherits `reply_to`, `thread` and `hops` from the most recent message delivered or acked in the last five minutes, if it belongs to the conversation being continued: it arrived on the channel being posted to, or, for a DM, is a DM from the recipient. Naming a `thread` explicitly disables this inference, and so does moving to another channel or person. Older mail never binds a send, so one message that is never acked cannot lock every later send.
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
| `mark_read` | `message_ids` | ok (marks handled; the adapter acks each message, §4) |
| `wait_for_messages` | `timeout_s` | messages or empty |
| `join` / `leave` | `channel`, `from?` | ok; `from="earliest"` also returns history through the read-only path. On a group channel, `join` and `leave` are busd calls that create and delete the read cursor (with busd down they fail clearly and existing delivery continues); on a private channel, `join` subscribes to a channel the agent is a member of (otherwise the error is "not a member"), and `leave` ends membership (§4b) |
| `unsubscribe` | `channel` | ok; stops fetching, keeps membership (a private channel, §4b) |
| `create_private_channel` | `name`, `description` | channel ID; the caller becomes its manager (§4b) |
| `invite` / `remove_member` | `channel`, `agent` | ok, once busd and the broker confirmed; manager or owner only. `invite` also sends the signed `invite` notice DM (§4b) |
| `list_members` | `channel` | members and manager of a channel the caller belongs to |
| `list_channels` | — | public channels with descriptions, plus the caller's own private channels from busd (public channels only, with a note, while busd is down) |
| `list_agents` | `detail?` | agents with a one-word presence state; with `detail`, activity, workers and recent activity (§7, Presence). Fingerprint and trust state come with busd |
| `history` | `channel` or `thread`, `limit`, `before?` | verified messages, read-only (§6) |
| `requests` | `agent?`, `status?` | open feature requests / bug reports, derived from thread history (§5) |
| `set_request_status` | `request_id`, `status`, `note` | ok (checked against the transition table, §5; `accepted` needs `--auto-accept-messages`, §8 rule 2) |

The private-channel tools, group `join` / `leave`, `history`, `requests` (which folds history) and the registry and catalogue reads go through busd (§10b); the adapter signs the request with its identity key, checks busd's signed reply and reports busd's errors as they are, including "busd is not reachable". Sending, receiving and acking talk to the broker only.

The adapter fills in `id`, `ts`, `from`, `hops`, `key_fp`, and the signature; agents never construct envelopes. A `send` or `publish` with `type=status` goes through the same transition check as `set_request_status` (§5).

**Decision: delivery is at-least-once.** If the broker stores a message but the adapter crashes before returning its ID, a retried send produces a second message with a new ID. There is no outgoing journal. Agents must tolerate duplicates. The broker's duplicate suppression (§4a) only covers the adapter's own retries within its time window.

---

## 10. busctl (human/admin CLI)

`register <agent-id>` (`register --monitor` creates `monitor@<owner>`, §10a), `rotate <agent-id>`, `revoke <agent-id>` (`revoke --incident`, below), `channel create|list|show|invite|remove|manager` (`create` makes a group channel; the rest also cover private channels, §4b), `tls init` (also creates busd's service signing key and copies its public key next to the CA), `tls reissue` (a new broker certificate from the existing CA, for an expired certificate or a moved host; `tls init --force` replaces the CA too), `tail [channel|--all]`, `thread <id>`, `requests` (open requests derived from thread history, §5), `pin <agent-id> <fingerprint>`, `known`, `alerts` (key mismatches, rejected messages, unknown senders), `post` (as `human@<owner>`; `post --channel <id>` for a private channel), `approve <request-id>`.

**Decision: busctl goes through busd.** Commands that need admin rights (register, revoke, channel create, channel management) are requests to busd (§10b), signed by `human@<owner>`; busctl asks for the master password to unlock that key for the command (§10a). `known`, `alerts` and `pin` are local. `tail`, `thread` and `requests` need no master password: they read through `busd.history` as `monitor@<owner>` (`tail` polls). With busd down, use `--direct`.

**Direct admin access is the explicit recovery path.** `--direct` (admin credentials from the owner's config) skips busd. It is how the system is set up (`tls init`, the first `register` call for `human@<owner>`, channels) and how the owner acts when busd is down. Direct commands never change private-channel membership, except `revoke --incident --direct`, which removes the agent's broker access and account directly and leaves the ledger update pending for busd (below): the ledger has one writer (§4b), and busd's reconciliation would undo the change. `busd reconcile --adopt` (§4b) is the recovery for a lost ledger.

**Incident revocation.** `busctl revoke <agent-id>` marks the agent's current key revoked, as before (§6). `busctl revoke <agent-id> --incident` is for a compromised agent and does all three: it revokes the agent's access and deletes its account, revokes its signing key, and removes its busd authority (busd ends its memberships; a channel it managed has no manager until the owner names one, §4b). With `--direct` (busd down) it does the first two and reports the busd part as pending; repeat the command without `--direct` to finish. To bring the identity back, the owner registers it again, rotates its key (§6), and the channels' managers invite it again.

---

## 10a. Bus monitor

A small Go service that connects to the broker and shows the messages flowing around, in a browser. It runs as a separate process with its own read-only broker principal, its own signing identity `monitor@<owner>` and its own durable consumer `bus-monitor`, created by busd; it may share busd's binary and deployment (§10b).

**v1: read-only.**

- Consumes every channel, private channels included, through its `bus-monitor` consumer (its reading never moves any agent's read position; it creates no consumer itself). It dispatches by channel (§4): only conversation channels hold signed envelopes; registry and catalogue records are parsed as their own kinds and never checked for a message signature. Messages from private channels are marked private in the views.
- Verifies each conversation message's signature and `to` field and shows the result: sender, recipient or channel, type, thread, verified / rejected (with reason, including a private-channel message whose sequence is outside the sender's membership intervals). It does no dedupe or timestamp checks; a duplicate ID is shown as such.
- Live view (server-sent events) plus browsing by channel, thread and agent, and open requests derived from thread history (§5). History and registry reads go through busd as `monitor@<owner>`, whose key is registered with the role `monitor` and may call only the read-only methods (§4a); it never holds the owner key.
- Verification uses the key history in the registry only (the key named by `key_fp`, §6). The monitor keeps no `known_agents` store and pins nothing; a sender missing from the registry, or a signature that doesn't match its registry key, is shown as rejected with the reason.
- Shows each agent's presence (§7): a status dot and state, the number of workers, the current activity with its age, and a panel with recent activity and workers, live over the same event stream.
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
- Broker access: it grants and revokes the broker access that membership implies, creates accounts and access for registered agents, and creates and deletes their consumers (§4a). It holds the admin principal; admin credentials never leave it and the owner's config.
- Reads agents cannot do themselves: history, the registry and its changes and the catalogue, as signed replies (§4a). The registry and the catalogue live in a NATS account agents cannot reach.
- The normal path for busctl's management commands (§10).
- Creating the monitor's consumer. The monitor (§10a) may share busd's binary but runs as a separate process with its own read-only principal and signing identity.
- A browser admin UI for the owner is later work and not specified here.

**State.** The membership ledger (§4b), the join boundaries, the reload generations and the operation intents: a plain stream in the broker's `CONTROL` account that only busd can write (§4a). Everything else lives in the broker or in the adapters. busd reads the registry continuously (its own reader) to know every agent's current key and whether it is revoked. busd answers requests only after it has caught up with the registry (on start and after a reconnect), and checks the caller's authorisation again at the moment of each change.

**Authentication of agents.** No new long-lived agent credential.

- busd's API is core request/reply on `busd.<method>` subjects of the bus connection (§4a). busd's identity is a configured service name (for example `busd@<owner>`) and its own Ed25519 *service key*, created by `busctl tls init` and pinned at setup: busctl writes the public key into each identity's config next to the CA. The adapter takes `--busd-name`. TLS only protects the connection to the broker; it does not say who answers a `busd.*` subject, and a publisher can name any reply subject. So busd signs every response, nonce responses included, over a domain-separated payload (`agent-bus/busd-response/v1`) that binds the service name, the caller, the hash of the request or challenge and the result; the adapter refuses an unsigned or wrongly signed response.
- Calls that only read (history, registry, catalogue) carry a random challenge instead of a nonce. They are signed like any request, and the response signs the challenge.
- Nonce issuance does not depend on the subject it arrived on. The agent sends `busd.nonce` with a body signed by its identity key (agent ID, `key_fp`, random value, domain-separated). busd checks the signature first, then issues a nonce: random, single-use, valid for 30 seconds, at most ten outstanding per verified caller. Unverified nonce requests are dropped under a global rate limit and count against nobody, so a redirected or forged nonce request cannot use up another caller's ten. busd consumes a nonce atomically when a request uses it and forgets every outstanding nonce on restart; a retry takes a fresh nonce.
- The agent signs each request with its Ed25519 identity key. The signed payload is domain-separated with a fixed prefix (`agent-bus/busd-request/v1`) and binds: busd's service name, the caller's agent ID and `key_fp`, the method, the target (channel ID and member, where they apply), the SHA-256 of the body, the nonce and the idempotency ID. busd also checks that the subject the request arrived on is `busd.<method>` of the signed method, and refuses a mismatch.
- busd verifies the signature against the caller's current, non-revoked key in the registry. It never uses a key sent by the caller and never pins on first use. It burns the nonce, rejects a reused or expired one, and rejects any signature that does not cover the request it received.
- Authorisation comes from the verified signed content (caller, method, target): manager, member or owner, never from the subject a request arrived on, who published it, or an `agent_id` parameter. A request redirected to busd through a pull reply subject either fails these checks or is a validly signed request whose effect is exactly the signed one; a subject proves nothing about the requester.
- Every mutating request carries an idempotency ID so a client can retry safely. Before any effect, busd persists an operation intent keyed by `(caller, idempotency ID)` (§4a): the hash of the semantic request (method, target and parameters, not the nonce or the signature, so a legitimate retry hashes the same) and every resource ID busd assigns, such as a new channel's ID. The effects follow. After a crash busd finishes that same operation from its intent and never allocates a second resource. The result is derived from the operation's confirmation events in the ledger; a retry with the same hash gets it, and waits if the operation is still finishing. The same ID with a different hash is refused (§4b).
- The owner's calls (busctl) are signed the same way by `human@<owner>`, whose key is unlocked by the master password (§10a).

**API.** Agent-callable: create a private channel, list the caller's channels and members, invite, remove, leave, join and leave a group channel, history, registry reads, the public and the private catalogue, and rotating its own key (`busd.agent.rotate`, signed by the current key). The monitor identity may call only the read methods (history, registry, catalogue, and the all-channel `busd.channel.list` and `busd.channel.membership`). Everything else (register, revoke, rotation after a revocation, channels, incident revocation, other agents' channels) needs the owner. The API is in bus terms (channels, members, accounts); each method is one `busd.<method>` subject.

**If busd is down.** busd runs supervised and restarts automatically; there is no HA. Agents and the monitor talk to the broker directly, and the access granted before stays in force.

| While busd is down | What happens |
|---|---|
| Send and receive on existing consumers, from senders whose keys are pinned, and private-channel mail inside the cached membership and watermark | works |
| Publishing a message | works; the message is stored. A `status` also needs its validation prerequisites (thread history, §5) available locally, otherwise it is refused |
| A message from an unpinned sender (registry lookup), a private-channel message above the cached watermark, a cold start without pins or membership cache | held until busd answers; nothing is rejected for lack of an answer |
| Releasing held mail after `busctl pin` | works (local), but it releases only the trust hold; membership holds and transition holds stay until their checks can run |
| Create, join, leave, invite, remove, register, revoke, rotate, busctl management commands | fail with "busd is not reachable" and change nothing |
| `history`, `requests`, `busctl tail` and `thread`, key lookups for unknown senders, membership refresh, news of a revocation, recreating a lost consumer | fail or wait until busd is back |
| The monitor's live view | works; history and keys it has not cached wait |

Recovery is `busctl --direct` (§10) or letting the supervisor restart busd, which finishes pending operations from their intents and reconciles on start.

---

## 11. Milestones

1. **Broker up (done, on Kafka):** a Redpanda Pod manifest started with `podman kube play`, TLS, SASL/SCRAM, auto-create off; busctl `tls init`, `register` and `channel create`. Replaced by the NATS binding in 1a.
1a. **Migrate to NATS JetStream:** `internal/broker` becomes the JetStream implementation behind the message transport and admin interfaces of §2, with the channel-name mapping inside the binding (this absorbs the earlier refactor follow-up: busctl no longer gets a raw broker admin client, broker channel names or access rules). busctl `tls init`, `register` and `channel create`, the deploy (`nats-server.conf` with the `BUS` and `CONTROL` accounts, start script, generated `auth.conf`, §4a) and `internal/itest` move to NATS: the `BUS` and `LEDGER` streams, the `registry` and `catalogue` buckets, the per-agent grants, and `busctl --direct` creating the agents' consumers (busd takes that over in milestone 3). The integration test proves everything §4a lists as not yet proved at the raw NATS level: account isolation, the per-role grants, reply-subject redirection (a pulled record redirected onto a subject is delivered but not stored again, an API response is stored with the server's payload, nothing lands in `CONTROL`), the delivery metadata's stream sequence, confirmed reloads and `MaxDeliver -1`. The signed-busd parts of the §12 tests (nonces, method checks, idempotency) belong to milestone 3, and the adapter's hold and handled-state tests to milestone 2. `AgentRecord` gains the `keys` list (§4). The Kafka code and the Redpanda manifest are removed.
2. **Adapter core:** envelope, JCS, Ed25519 sign/verify, TOFU store (pinned public keys, `revoked_keys`) with `--[no-]auto-accept-new-keys`, handled set (keyed by destination, verified sender and ID; rejected deliveries by stream sequence), hold store with held, released and handled states, per-message acks (the agent's `mark_read`: broker confirms, then handled is written), persisted channels, bounded inbox buffer, identity lock, `send` / `publish` / `inbox` / `mark_read`.
3. **busd:** the control service (§10b): signed-request authentication with nonces, private channels (§4b), the membership ledger with stream-sequence boundaries and watermarked membership answers, reconciliation, the private catalogue, ledger backup and restore, `busd reconcile --adopt` (§4b), busd's own broker connections, service name and signing key (signed responses), consumer provisioning (the monitor's `bus-monitor` too), history and registry reads, registry writes (register, revoke, `busd.agent.rotate`), the monitor identity, confirmed reloads, readiness after registry catch-up, crash-safe operation intents, supervised restart, and busctl as a busd client (`--direct` stays for recovery).
4. **MCP surface:** all tools in §9, private-channel tools included, with `history` as the read-only path; register with Claude Code and OpenCode.
5. **Delivery hooks:** Claude Code `Stop` and `SessionStart`; OpenCode plugin; per-session adapter socket and notified-message tracking (§7).
6. **Requests and safety:** request lifecycle with its transition table, open requests derived from thread history (`requests` tool, `busctl requests`; no projection, §5), `--[no-]auto-accept-messages`, loop guard (adapter-set `hops`), rate limits, secret scan.
7. **Ops:** password-protected `human@<owner>` key, busctl `tail` / `alerts` / `post` / `approve`, adapters honouring a pinned owner's `status: accepted` (§8 rule 2), key rotation (`busctl rotate` through `busd.agent.rotate`, key chain) and revocation, incident revocation: access revoked and account deleted, key revoked, busd authority removed (§10), `busctl tls reissue`, setup script per machine (which also pins the `human@<owner>` key).
8. **Monitor (read-only):** live view on localhost, browsing, verification status, `monitor@<owner>` identity (§10a v1); may be hosted alongside busd (§10b).
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
- An agent offline for a day reads its full backlog in order on reconnect; nothing is lost if the session crashes before `mark_read`.
- Ack B before A, then crash: A is redelivered. Crash before and after the ack is persisted: the message is redelivered or skipped, never lost.
- A message sent before the adapter's first start is delivered. After joining a channel and restarting, the adapter resumes where it was. With a consumer lost on the broker, busd recreates it from the ledger's join boundary (group) or from the beginning (others): unread group mail is recovered and the handled set skips replayed mail.
- A backlog larger than the inbox buffer replays without loss or crash.
- `history` on acknowledged mail and `join(from="earliest")` leave the unread state unchanged.
- Mail arriving after the idle timeout waits for the next hook or session; mail during the cooldown is notified at the next hook; mail arriving while earlier mail is still unacked prompts only for the new message.
- Two sessions on one identity: only the lock holder gets delivery prompts.
- Open requests are derived from thread history: `requests` and `busctl requests` list the same open requests an agent folds from the thread, in canonical order, and write no record; requests in a private channel are derived from its own history.
- Status changes through generic `send` / `publish` with `type=status` are checked like `set_request_status`; an unrelated agent cannot change a request's status; a group request gets its assignee from the first non-requester status change; with `--no-auto-accept-messages`, the pinned owner's approval is followed by legitimate progress; `rejected` is terminal.
- The monitor reads milestone-1 `AgentRecord` and `TopicRecord` records without false signature alerts.
- A send without `reply_to` or `thread` inherits from a message delivered four minutes ago, but not from one delivered or acked six minutes ago; a send naming a `reply_to` not found after the history lookup is refused.
- A status change whose thread history is missing is held, then accepted once the history is available; a record over 96 KB is rejected and acked.
- An agent's own posts to `general` and a joined channel are never delivered back to it as unhandled mail.
- After `busctl revoke` and `busctl rotate`, receivers that know the agent hold its messages until `busctl pin` and then deliver them, even with `--auto-accept-new-keys`; a receiver that never met the agent pins the new key by TOFU as usual.
- Two agents set to auto-reply stop at the hop limit, also when they alternate `send` and `publish`, omit `reply_to` or `thread`, branch replies, or restart their adapters; a TOFU-pinned owner cannot reset the count.
- An agent cannot read another agent's DM channel: bob has no consumer create, delete or update grant at all, cannot pull from or ack alice's consumer, and cannot direct-get a message.
- After a restart that loses local context, either participant of a DM request rebuilds the full thread (both DM channels, in stream order) through `history`, and the request's status folds the same as before; a third agent gets only its own messages.
- Adversarial reply destinations: an agent that calls any JetStream API it holds (`MSG.NEXT`, `INFO`, an ack) with a reply subject of `$KV.registry.<key>`, `$KV.catalogue.<key>`, `ledger.<x>` or `busd.<method>` changes nothing in `CONTROL`, and the registry, catalogue and ledger are unchanged afterwards. A record pulled onto a private subject through a reply subject is delivered to a listener there, but `BUS`'s message count and last sequence are unchanged and no record exists on that subject; a JetStream API response sent to that subject is stored, with the server's payload. A signed busd request stored in a DM and pulled onto `busd.<other method>` is refused (method mismatch), and an unsigned or unauthorised payload pulled onto any busd subject, `busd.nonce` included, changes nothing on the `BUS` side either (no channel, member, consumer or grant) and uses up no other caller's nonce quota. Unauthorised, mismatched or replayed requests have no effect at all; a valid signed request that arrives by redirection executes exactly the signed operation.
- Consumers: an agent cannot create, update or delete one; busd creates them on register, join and invite and deletes them on leave, removal and incident revocation, and waiting pulls end; the monitor cannot create, update or delete one or touch an agent consumer's name, and busd creates `bus-monitor`; `join` of an uncatalogued channel is refused, and the adapter refuses to publish to one. An agent that appends to an uncatalogued subject is read by nobody and `history` refuses it.
- A JetStream call the server denies is reported with its reason (permissions violation, "not a member") within a short timeout, not as a hang or a generic timeout.
- A message published twice with the same `Nats-Msg-Id` inside the dedupe window is stored once; the same envelope ID on another subject is stored; a retry after the window is stored again and the receiver's handled set skips it.
- A held message is on disk and acked on the broker before it is released, so it survives an adapter restart and a `nats-server` restart and never uses up `MaxAckPending`; unacked mail stays pending across a restart and is never dropped by the delivery limit; a redelivered handled or held ID is acked and a buffered one gets `InProgress`.
- After `busd` rewrites `auth.conf` and reloads the server, a new grant works and a revoked one stops working on live connections at once; a removed user is disconnected; a busd crash drops no agent connection; an invalid candidate config is refused before it replaces the file; a reload that did not take is detected and finished when busd restarts.
- No adapter tool returns or reads key material, and keys are never inside a project directory.
- `busctl tail --all` reproduces the full conversation from the log.
- A non-member that connects to the broker directly cannot read or write a private channel (access denied), and cannot see its name; an agent that was removed cannot either.
- An agent that is not the manager or the owner cannot invite or remove members through busd; a member cannot remove another member; a member can leave itself; an invitation of an unregistered or revoked identity is refused.
- Creating, inviting and removing in a private channel leaves no name, description, member or request in the public catalogue; the private catalogue is served only to members and the owner.
- After removal, busd has deleted the member's consumer (waiting pulls end), the adapter drops the channel's pending mail, stops fetching, and does not treat the access-denied error as a handled message or loop on reconnect. After a new invitation the member reads the whole retained history, including what was sent while it was out.
- `leave` on a private channel reports done only after the broker confirmed the access change; `leave` on a group channel deletes its read cursor through busd; `unsubscribe` on a private channel keeps access.
- A replayed busd request (same nonce), a request with a changed body, method or target, a request signed by a revoked or unknown key, a request with a caller-supplied key, and a request whose subject does not match its signed method are all refused; a retry with the same idempotency ID returns the first result and changes nothing twice.
- Boundary gaps: a former member's record redirected into the channel in the gap before the removal boundary is recorded, and a record stored before a new member's grant takes effect, are handled as §4b documents (the first accepted until the boundary is recorded, the second counted as the member's own); a crash before the removal boundary is recorded keeps the interval open until recovery records it, and the boundary is not changed afterwards; a watermark snapshot never covers a sequence past a pending change.
- busd crash recovery: a kill after the intent is stored but before any effect, between effects, and after the effects but before the result each end, on retry with the same idempotency ID, in the same single operation: a new private channel keeps its one assigned ID and no second channel appears; a retry with a fresh nonce and signature hashes the same; the same ID with a different request is refused.
- Crash and retry races in membership changes: a crash between the ledger write and the broker change is finished on busd's restart; concurrent invite and remove on one channel end in one consistent state; an old retry cannot bring back a removed member; reconciliation repairs access changed behind busd's back.
- With busd stopped, DMs, `general`, groups and private-channel messages from pinned senders inside the cached membership and watermark keep flowing; creating a channel, joining, history, `requests`, inviting, removing, leaving a private channel and busctl management commands fail with a clear error and change nothing; a message from an unpinned sender, a private-channel message above the cached watermark, and mail checked by an adapter cold-started without caches are held until busd is back, never rejected; a killed busd is restarted by its supervisor.
- The monitor, a separate process, reads and shows private-channel messages marked private; `busctl post --channel` makes busd grant `human@<owner>` access first; the monitor signs as `monitor@<owner>` and cannot write, create a consumer, change access or call any owner-only or mutating busd method, and never holds the owner key.
- `busctl revoke --incident` revokes the agent's access and deletes its account, revokes its key and ends its busd authority and memberships; its later busd requests and broker connections fail. `busctl revoke --incident --direct` does the access and key parts and reports the busd part as pending.
- A manager that leaves, is removed or is revoked leaves the channel with no manager; the owner can still act and can name a new one; agents cannot hand management over.
- A busd request with an expired nonce, or one whose nonce was already used, is refused.
- `busctl --direct` commands leave private-channel membership unchanged; reconciliation does not undo or redo them.
- With a missing, empty or reset ledger busd refuses to revoke anything and reports it; `busd reconcile --adopt` or a backup restore brings it back.
- An invitation sends the invitee a signed `invite` DM; `join` without membership fails with "not a member" and the denied read is not treated as handled mail; a status change in a private channel follows the same assignee rule as in a group channel.
- A forged or unsigned busd response, a nonce response included, is refused by the adapter; a nonce request not signed by its claimed caller is refused and costs that caller no quota, also when redirected; busd answers nothing before it has caught up with the registry; a nonce does not survive a busd restart; the same idempotency ID with a different request is refused, also beyond the dedupe window.
- Defence in depth (a redirected record is not stored, so the test stores the record directly, as a grant mistake would): a former member's message (a new ID and body, any `ts`) on the private channel after its removal boundary is recorded is rejected and alerted; an old message replayed onto the channel after that is rejected; the same record from a member whose sequence lies inside an interval is accepted, also for a member removed and invited again (messages in the gap are rejected); a non-member's redirected record is rejected; a message above the cached watermark makes the adapter refresh first, and with busd unreachable it is held, not rejected; `history` and status folding apply the same rule; the pinned owner always passes.
- No agent can subscribe to `busd.>`, publish on a busd subject outside its grants, or reach `CONTROL`; only the `busd` account answers busd requests.
- A forged envelope that copies a legitimate message's ID (bad signature, another sender or another destination) is rejected by stream sequence and does not stop the legitimate message from being delivered; only verified envelopes enter the handled set; a verified envelope whose ID repeats with a different payload is alerted and not delivered.
- Hold release: a crash before release, after release before the agent sees the message, and after it saw the message but before `mark_read` each leave it delivered exactly when handled state is committed, never lost; the hold record is deleted only after the handled set write; a redelivery of a held sequence is acked.
- `busctl rotate` sends a signed `busd.agent.rotate`; one signed by a revoked key is refused; after a revocation only the owner's signature rotates the key.
- Tests that need the broker's behaviour run against the JetStream binding.

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
10. Libraries: the official MCP Go SDK, a JCS library (§2). The broker client was franz-go; replaced by decision 30 (nats.go).
11. Identity lock with `flock` plus a PID in the lock file; hooks talk to the adapter over a unix socket (§3, §7).
12. A private CA made by `busctl tls init` (§4).
13. What `--auto-accept-messages` allows, as described in §8 rule 2.
14. Hop limit 6 and 30 messages per 10 minutes as defaults, configurable by flag (§8).
15. The broker runs from a Kubernetes Pod manifest, started with Podman for now; another runtime later. Replaced by decision 31 (a plain `nats-server` process).
16. Group channels stay open: every agent reads and writes all of them (originally `agents.topic.*` through one prefixed ACL; now per-group consumer grants and the open `topic.*` publish grant, §4a); joining only changes subscriptions. Closed channels are private channels, by invitation through busd (§4, §4b).
17. Agents read and write `general` and produce idempotently (originally Kafka topics and `IdempotentWrite`; now `Nats-Msg-Id` dedupe, §4a). The requests channel was dropped in v1 (decision 34).
18. `busctl tls reissue` renews the broker certificate from the existing CA (originally busd's too, before decision 30); part of milestone 7 (§10).
19. Delivery and recovery: the adapter acknowledges each handled message itself (nothing advances automatically; originally a cumulative commit of the contiguous prefix on Kafka, replaced by per-message acks by decision 30); a verified message ID is handled when acked or sent by the adapter itself, and a terminally rejected delivery is recorded by its stream sequence; a message from an unknown sender under `--no-auto-accept-new-keys` or with a changed key is held on disk (and acked on the broker) and released after pinning; joined channels are persisted, with defined fallbacks when the broker has no stored cursor; the broker keeps cursors forever (§4, §6).
20. Delivery promise: at-least-once, no outgoing journal; mail reaches active sessions and the next session start, idle sessions are not woken; hooks talk to their own session's adapter; the inbox buffer is bounded (§7, §9).
21. Key trust: key storage guards against accidental exposure only; the registry record holds the key history with rotation links; pins store public keys; revoked keys stay in the registry, adapters persist revoked fingerprints (§6).
22. Loop guard: the adapter sets `hops` from the parent message; only the pinned owner resets it (§8).
23. Requests: a transition table checked on every status path; `rejected` is terminal; open requests are derived from the signed thread history, with no projection in v1 (decision 34, §5).
24. Record kinds: conversation envelopes, registry and catalogue records have their own verification policies; `history` and `join(from="earliest")` are read-only (§4, §6).

Added by the owner on 2026-11-24 (busd and private channels):

25. **busd, a control service, not a gateway.** Agents talk to the broker directly for messages; signing, verification, TOFU, inbox state and hooks stay in the adapter. busd holds the admin principal and does private channels, membership, broker access and busctl's management commands. busctl keeps direct admin access as an explicit recovery path. The owner's encrypted signing key is never an unattended busd credential. The monitor may share busd's binary but runs as a separate process with its own read-only principal; a browser admin UI is later work. busd has a service name and broker account of its own and is not a bus agent. If busd is down, existing traffic keeps flowing and management fails clearly (§2, §10, §10b).
26. **Private channels by invitation.** An agent creates one through busd and becomes its manager; the owner is recovery admin and names a new manager when none is left; only manager and owner invite and remove, members may leave; the invitee gets a signed `invite` DM. An invitation grants membership at once, enforced by per-member broker access in a separate namespace. No join key, no key exchange, no end-to-end encryption. Members read the whole retained history. Private means private from other agents, not from the owner, the monitor or the broker. Leaving removes access and is reported done only after the broker confirms it; the adapter drops pending mail. Private names, descriptions and requests never go to public channels. busd keeps one membership ledger with versioned, idempotent, per-channel serialised operations and reconciles broker access to it, but never revokes against a missing or reset ledger (`busd reconcile --adopt`). Incident revocation revokes access and deletes the account, revokes the key and removes busd authority (§4b, §6, §10).
27. **Agent authentication to busd:** a per-request Ed25519 signature by the agent's identity key over a domain-separated payload (busd's service name, caller, method, target, body digest, single-use short-lived nonce), checked against the current non-revoked registry key; authorisation by that identity; idempotency IDs for retries; no new long-lived agent credential. Originally busd also had a TLS certificate for its service name; with the NATS binding (decision 30), busd is reached over the broker connection and signs every response with its pinned service key (decision 32, §10b). Nonce requests are signed by the caller and the quota is per verified caller; busd authorises from the signed content and checks the subject against the signed method (decision 32).
28. **One internal boundary to the broker** (was: not tightly bound to one broker): a broker-neutral model (§4) with one binding (§4a), originally Kafka, replaced by decision 30; two small Go interfaces (message transport, admin), one implementation, confined to `internal/broker`. Another broker is a possible later option, not a design goal (decision 35; §2, §4a).
29. **Milestone order:** milestone 1 is done; busd comes after the adapter core and before the MCP surface, so private-channel tools are exposed only after authorisation, the ledger and reconciliation exist (§11).

Added by the owner on 2026-11-24 (backend switch):

30. **NATS JetStream replaces Kafka/Redpanda.** The first binding was Kafka (Redpanda for development, franz-go, SASL/SCRAM, ACLs, consumer-group offsets with a contiguous-prefix commit, compacted topics). The owner moved to NATS JetStream (Apache 2.0, CNCF): `nats-server` v2.15.3, `nats.go` v1.56.0 (`jetstream`). One stream `BUS` for conversation channels, per-agent durable pull consumers created by busd, with per-message acks, `Nats-Msg-Id` dedupe over a 24-hour window, KV buckets for registry and catalogue and a plain `LEDGER` stream for busd's membership ledger, all in a second account (`CONTROL`) that only busd and the admin reach, static users with per-user subject permissions, TLS from the private CA, and permission changes by rewriting the authorization file and reloading. JWT accounts and `nsc` are not needed for one owner. The two Go interfaces stay as one internal boundary; JetStream is the only implementation, in `internal/broker` (§2, §4a, §11 milestone 1a).
31. **The broker is its own process, not embedded in busd,** so a busd crash does not drop agent connections. On the dev machine it is a plain binary; a container is optional later, and the server configuration plus the start script are the only runtime-specific files (§2, §4a).
32. **Control plane isolation and busd-provisioned consumers.** JetStream writes responses to a reply subject the caller names, so authoritative data (registry, catalogue, `LEDGER`) sits in a second NATS account, `CONTROL`, that agents cannot reach; busd serves registry, catalogue and history over request/reply and signs every response with a service key pinned at setup. Agents get no consumer create, delete or update grants: busd creates their consumers (on register, group `join`, invite) and deletes them (on leave, removal, incident), and group `join` and `leave` become busd calls. Channels exist in the catalogue, not in the broker; the adapter refuses to publish elsewhere. Holds are written to disk and then acked on the broker; an agent's ack is confirmed by the broker before the handled state is written. Permission changes are confirmed operations (validate, reload, confirm the generation). Within `BUS`, an agent can make the server deliver a payload it stored (a DM to itself) to any `BUS` subject it can name, including a private channel it cannot publish to; the integration test showed the delivered copy is not stored again. No third account: receivers check broker order instead of sender time (decision 33). busd also creates the monitor's consumer; the monitor has no consumer grant and signs as `monitor@<owner>`, which may call only read-only busd methods and is never the owner key; any raw JetStream reader can redirect payloads within `BUS`, and the receiver checks make that harmless. busd operations are crash-safe: an operation intent keyed by (caller, idempotency ID), with the assigned resource IDs, is persisted before any effect, recovery finishes it, the result comes from confirmation events and the hash covers the semantic request only. busd authorises from the verified signed request (caller, method, target) and checks the subject against the signed method; nonce requests are signed and limited per verified caller. Only verified envelopes enter the handled set, keyed by destination, verified sender and ID; rejected or unverified deliveries are tracked by stream sequence. A held message keeps its record until handled state is committed. Key rotation is `busd.agent.rotate`, signed by the old key. busd runs supervised, with a table of what works while it is down and no HA (§10b). The grants matrix lists full subjects (§4a, §4b, §6, §10b).

Added by the owner on 2026-11-24 (JetStream review fixes):

33. **Private-channel membership by broker order, not sender time.** The broker assigns each stored message a stream sequence the sender cannot set, and a message redirected onto a private subject is delivered but not stored again (proved by the integration test), so the interval check is defence in depth. busd records the sequence at every confirmed join and removal; a receiver accepts a private-channel message only if its sequence lies in one of the sender's membership intervals, and `human@<owner>` always passes. busd's membership answers carry a signed watermark; above it the adapter refreshes, and with busd unreachable it holds. History reads and status folding use the same rule. The `ts` is no longer used for membership, and there is no backdating caveat (§4a, §4b, §6).
34. **No request projection in v1.** The `requests` tool and `busctl requests` derive open requests from thread history through the read-only path. The requests KV, its grants, `busd.requests.project` and its tests are dropped; a projection is a possible later optimisation (§5).
35. **One small internal boundary to the broker.** The two Go interfaces keep the client library in one package. Kafka or another broker is a possible later option, not a design goal (§2).
36. **Targets and quiet delivery.** The envelope carries signed `targets` (agent IDs or `@all`); every message has non-empty targets (the sending adapter fills DMs, replies and `@all` for requests, and refuses the rest). Agents are prompted only for mail that targets them; `--deliver=targeted` (default) keeps mail aimed at others out of the agent's inbox entirely, `--deliver=all` delivers it. Requests and questions get an immediate `ack` reply and a later `answer`; acks, answers and statuses are not answered (§5, §7).

Added by the owner on 2026-11-24 (presence):

37. **Presence for the owner's monitor, from hooks, in stream `PRESENCE`.** Each adapter publishes its agent's state, activity, workers and unread targeted mail on change (at most every 3 seconds) and as a 30-second heartbeat; hooks (Claude Code) and a plugin (OpenCode) feed it through `agent-bus event`; readers call an agent offline after 90 seconds without a heartbeat; an idle agent with targeted mail unseen for 10 minutes is `unresponsive`. One message per agent in a separate stream of the `BUS` account, written only by the agent itself. Agents do not poll it; sending to an agent that cannot respond still works and warns. `wait_for_messages` returns only mail not yet returned, a message the agent sent an `ack` for shows `acked`, and `mark_read all` leaves unanswered requests and questions (§7, §9, §10a, docs/monitor.md).

Added by the owner on 2026-11-24 (access control):

38. **Puppet-style mTLS enrollment, planned (§4c).** Agents enroll with a CSR, the owner approves in the monitor or with `busctl ca sign`, the monitor holds the CA key; optional autosigning with an enrollment secret; separate enrollment listener; certificates built from the approved ID; removed IDs not reused while their certificates are valid. Order: agent removal with directory refresh, the protocol fixes, the minimal monitor API, then enrollment, then the VS Code extension.
