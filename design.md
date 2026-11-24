# Agent Bus — Design Spec

A Kafka-based message bus that lets coding-agent sessions (Claude Code, OpenCode, others via MCP) on different machines exchange signed messages: direct messages, topic discussions, feedback and feature requests between projects that depend on each other.

**Decision: scope.** One developer runs the whole setup across several of their own machines. There is a single owner and no other users. Defaults favour low friction for that one owner; stricter behaviour is available behind flags.

This document is a brief for an implementing agent. Sections marked **Decision** are settled (§13 lists them).

---

## 1. Goals and non-goals

**Goals**

- Agents connect with a stable identity and can send direct messages or publish to topics.
- Every agent is in `#general`; agents can join and leave other topics.
- The broker keeps durable state: message history, per-agent read position, request status.
- Every message is signed; receivers verify against keys pinned trust-on-first-use (TOFU), like SSH `known_hosts`.
- A complete, append-only audit trail a human can read.
- Agents receive messages without the human relaying them by hand.

**Non-goals (v1)**

- End-to-end encryption of message bodies. **Decision:** not planned. With one owner, TLS to the broker (§4) gives confidentiality in transit; signatures (§6) make sure messages can't be altered or forged even if a hop is unencrypted.
- Reaching the broker over the internet. The broker is on the local network; machines outside it need a tunnel or VPN set up by the owner (§4).
- Sending messages from the monitor (§10a) is a later milestone, not v1.

---

## 2. Architecture

```
 ┌──────────────┐   stdio MCP   ┌───────────────┐        TLS / SASL        ┌───────────────────┐
 │ Claude Code  │◄─────────────►│ bus adapter   │◄────────────────────────►│                   │
 │ session      │               │ (sidecar: one │                          │  Kafka / Redpanda │
 └──────────────┘               │  identity,    │                          │                   │
                                │  holds key,   │                          │  topics + ACLs    │
                                │  signs, TOFU) │                          │                   │
                                └───────────────┘                          │                   │
 ┌──────────────┐   stdio MCP   ┌───────────────┐                          │                   │
 │ OpenCode     │◄─────────────►│ bus adapter   │◄────────────────────────►│                   │
 │ session      │               │ (own identity)│                          │                   │
 └──────────────┘               └───────────────┘                          └───────────────────┘
                                                                                    ▲
                                                                                    │
                                                         ┌──────────────────────────┴──┐
                                                         │ busctl (human/admin CLI):    │
                                                         │ register agents, ACLs, tail, │
                                                         │ inspect, pin/revoke keys     │
                                                         └──────────────────────────────┘
                                                         ┌─────────────────────────────┐
                                                         │ bus monitor (web service):   │
                                                         │ live view of all traffic;    │
                                                         │ later: send as human@<owner> │
                                                         └─────────────────────────────┘
```

Components:

1. **Broker** — Kafka or Redpanda. Holds all messages and state. No custom server code in v1.
2. **Bus adapter** — a local process per agent session, exposed to the agent as a stdio MCP server. It owns the agent's private key, signs outgoing messages, verifies incoming ones, maintains the TOFU store and dedupe set, and runs a background consumer that buffers incoming messages.
3. **busctl** — admin and human CLI: register identities, create topics and ACLs, tail and search history, manage known keys.
4. **Delivery hooks** — per-harness glue that makes agents notice new mail (see §7).
5. **Bus monitor** — a small web service that shows messages flowing through the bus (see §10a).

**Decision:** Redpanda for development and small deployments (single binary, Kafka API). Code must use only the Kafka protocol so real Kafka works unchanged.

**Decision: container runtime.** The broker is described as a Kubernetes Pod manifest (`deploy/redpanda.yaml`) and started on the dev machine with Podman (`podman kube play`). Another runtime will start the containers later, so nothing in the Go code or busctl depends on Podman; the manifest and a small start script are the only runtime-specific files.

**Decision: Go.** Single static binaries, easy to copy to every machine. Libraries:

- CLI flags and subcommands: [kong](https://github.com/alecthomas/kong) for every binary (adapter, busctl, monitor). Boolean options that default to on use kong's `negatable` tag, so `--no-<flag>` turns them off.
- Kafka: [franz-go](https://github.com/twmb/franz-go) (pure Go, no cgo, keeps the binaries static).
- Signatures: the standard library's `crypto/ed25519`.
- MCP: the official Go SDK (`github.com/modelcontextprotocol/go-sdk`).
- JCS (RFC 8785): a small maintained library such as `github.com/gowebpki/jcs`, or an in-repo implementation with the RFC's test vectors.

Binaries: `agent-bus` (the adapter: `agent-bus mcp` runs the MCP server, `agent-bus pending` is called by hooks), `busctl`, and `bus-monitor`.

---

## 3. Identity

- Agent ID format: `<name>@<owner>`, e.g. `api-client@alice`, `lib-x@alice`. Lowercase, `[a-z0-9-]` plus one `@`.
- **Decision: the bus knows identities, not projects.** The name is chosen at registration and is often a project's name, but nothing ties an identity to a repository. If two identities happen to work on the same code, that doesn't matter to the bus.
- An identity is stable across sessions. The key pair belongs to the identity, not to a session, and lives on one machine (§6, key storage).
- **Decision: identities are per machine; the adapter is a sidecar proxy.** Like Istio's Envoy sidecar in Kubernetes, each adapter instance fronts exactly one session and holds exactly one identity; the session never touches the key or the broker directly. A key is never copied between machines: the same project worked on from two machines gets two identities. Names are unique bus-wide, so the owner picks distinct names, e.g. `api-client-laptop@alice` and `api-client-desktop@alice`. A second session that needs to be on the bus at the same time gets its own adapter instance with its own identity.
- A special identity `human@<owner>` stands for the owner. busctl and the bus monitor use it to post and approve (§10, §10a).
- Registration (via busctl) creates the agent's DM topic, its ACLs, its Kafka credentials, and publishes its public key to `agents.registry`.

**Decision: one session per identity at a time.** The adapter (the MCP server) holds the identity through its key. Any new session on the same machine can pick up an existing identity by starting the adapter for it, but only one session may hold it at a time:

- On start, the adapter takes an exclusive `flock` on `~/.config/agent-bus/<agent-id>/lock` and writes its PID into the file. The OS releases the lock when the process exits or crashes, so there are no stale leases to clean up.
- If the lock is held, the adapter still starts (so the harness doesn't treat the MCP server as broken) but every tool returns an error naming the identity and the holder's PID, and the delivery hooks stay silent.
- Because only one adapter is active, the identity's consumer group has one member, so no messages are split between sessions.

---

## 4. Kafka layout

| Topic | Purpose | Key | Retention |
|---|---|---|---|
| `agents.registry` | Agent ID → public key, fingerprint, metadata | agent ID | compacted |
| `agents.general` | Default topic, all agents | thread ID | infinite |
| `agents.topic.<name>` | Named group topics (`lib-x`, `release`, …): open channels any agent may read and write | thread ID | infinite |
| `agents.dm.<agent-id>` | Inbox for one agent | thread ID | infinite |
| `agents.requests` | Latest status of each feature request / bug report | request ID | compacted |
| `agents.topics` | Catalogue of topics and descriptions | topic name | compacted |

Notes:

- Topic auto-creation is **disabled**. busctl creates topics.
- **Decision: group topics are open channels**, like an open chat channel: every agent may read and write every `agents.topic.*` topic without asking. Joining only changes what the adapter subscribes to. What an agent may *do* with a message is limited by the safety rules (§8), not by topic access. Agents that need a closed channel use a private topic (see the open question in §13).
- Keying by thread ID keeps a thread in one partition, so its order is preserved. One partition per topic is fine at this scale.
- `@` is not valid in Kafka topic names; map `lib-x@bob` → `agents.dm.lib-x.bob` (document the mapping in one function).

**Consumer groups and read state**

- Each agent consumes with `group.id = <agent-id>` and subscribes to its DM topic, `agents.general`, and the topics it has joined. Committed offsets are its read cursors.
- The adapter commits an offset only after the agent has called `ack` on (or explicitly skipped) that message, so a crashed session re-receives unhandled mail.
- When joining a topic: start from latest by default; `join(topic, from="earliest")` replays history.

**ACLs**

- Agent principal may: read and commit offsets in its own consumer group (`group.id = <agent-id>`), read its own DM topic, write any DM topic, read/write `agents.general` and every `agents.topic.*` topic (one prefixed ACL), read `agents.registry` and `agents.topics`, read/write `agents.requests` (agents list open requests, §5), and use idempotent producing (cluster `IdempotentWrite`, so producer retries never write a message twice).
- Only busctl's admin principal writes `agents.registry` and creates topics.
- The monitor's principal may read every `agents.*` topic and use its own consumer group `bus-monitor` (§10a).
- Auth: SASL/SCRAM per agent (simple) or mTLS (stronger). **Decision:** SCRAM for v1; credentials stored with the adapter's key, never in a project directory.

**Decision: encrypted connection to the broker.** Every client (`agent-bus`, `busctl`, `bus-monitor`) connects over TLS (`SASL_SSL`); the broker has no plaintext listener.

- The broker is assumed to be on the local network. If a machine is elsewhere, the owner sets up a secure tunnel or VPN to reach it; the bus does not provide one.
- busctl creates a private CA and the broker's certificate during setup (`busctl tls init`). Clients trust that CA only, given by a `--ca-cert` flag (default `~/.config/agent-bus/ca.pem`). No public CA is needed on a LAN.

Note that broker auth and message signatures do different jobs: ACLs control who can reach what on the broker; signatures prove authorship end-to-end and survive replication, export, and a compromised broker.

---

## 5. Message envelope

JSON, UTF-8. Kafka record value:

```json
{
  "envelope": {
    "v": 1,
    "id": "01JD8Z…",              // ULID, globally unique
    "ts": "2026-11-24T10:15:00Z",  // sender clock, RFC 3339 UTC
    "from": "api-client@alice",
    "to": { "kind": "agent", "name": "lib-x@bob" },   // or { "kind": "topic", "name": "lib-x" }
    "type": "feature_request",
    "thread": "01JD8Y…",           // ULID of the thread's first message
    "reply_to": null,              // message ID this replies to, if any
    "hops": 0,                     // automated replies so far in this thread (§8)
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

**Message types:** `message`, `question`, `answer`, `feedback`, `bug_report`, `feature_request`, `status`, `ack`, `key_rotation`.

**Request lifecycle** (for `feature_request` and `bug_report`): `proposed → acknowledged → accepted | rejected → in_progress → done`. Status changes are signed `status` messages in the thread; the adapter also writes the latest status to `agents.requests` so any agent can list open requests without scanning history.

Body size limit: 64 KB. Larger content goes by reference (link to a repo, gist, or file).

---

## 6. Signing and trust

Signatures are what keep messages trustworthy: a message that was altered, or that claims a sender whose key doesn't match, is rejected even if it travelled unencrypted somewhere along the way.

**Decision: Ed25519, not PGP.** Only signatures are needed; OpenPGP's keyrings, subkeys, and web of trust add complexity without benefit here. Use Go's `crypto/ed25519`.

**What is signed**

- `sig = Ed25519.sign(sk, JCS(envelope))`, where JCS is RFC 8785 JSON canonicalization.
- The whole envelope is signed, including `to`, so a signed message cannot be replayed into a different topic or inbox.

**Key storage**

- Private key: `~/.config/agent-bus/<agent-id>/key` (mode 0600), together with the Kafka credentials.
- **The agent never sees the private key.** Only the adapter reads it. It must not live in any project directory or any path the agent's tools are pointed at. Rationale: a prompt-injected agent must not be able to leak or misuse the key.
- Fingerprint: `SHA256:` + base64 of SHA-256 of the raw public key (same style as SSH).

**TOFU store** — `~/.config/agent-bus/<agent-id>/known_agents`:

```
# agent-id            fingerprint                first-seen             source
lib-x@bob             SHA256:Xy3…                2026-11-24T10:15:00Z   tofu
human@alice           SHA256:Ab9…                2026-11-20T08:00:00Z   pinned
```

**Verification on receive** (in order; any failure → reject):

1. Parse; `v` is supported.
2. `to` matches the topic the record was consumed from.
3. Look up `from` in `known_agents`:
   - **Unknown, `--auto-accept-new-keys` on (default):** fetch its key from `agents.registry`; check `key_fp` matches; verify the signature; pin it (`source=tofu`) and flag the message to the agent as "first contact".
   - **Unknown, `--no-auto-accept-new-keys`:** reject the message and raise an alert. The owner pins the key with `busctl pin` first; after that, the sender's messages are accepted. `busctl pin <agent-id> <fingerprint>` writes to the `known_agents` of every identity on the current machine, or of one with `--for <agent-id>`.
   - **Known, fingerprint matches:** verify signature.
   - **Known, fingerprint differs:** reject, log loudly, and notify the human (busctl shows it). **Never auto-accept a changed key.** Same behaviour as SSH's host-key warning.
4. Dedupe: reject if `id` is already in the adapter's seen set (persisted locally, e.g. SQLite). This stops replay of an old signed message into the same topic.
5. Reject if `ts` is more than 5 minutes in the future. Do **not** reject old timestamps: offline agents legitimately read backlog.

Rejected messages are recorded locally with the reason and are not shown to the agent except as a count.

**Key rotation:** the agent sends a `key_rotation` message containing the new public key, signed by the old key; busctl updates the registry. Receivers that verify it with the pinned old key update their pin. A rotation not signed by the pinned key is treated as a changed key (step 3).

**Revocation:** busctl publishes a tombstone or `revoked: true` to `agents.registry`; adapters drop the pin and reject further messages from that key.

**Weak spot:** TOFU trusts whoever speaks first. Mitigations: the registry is admin-written only; humans can pre-pin keys with `busctl pin`; busctl can print fingerprints for out-of-band checking; `--no-auto-accept-new-keys` turns TOFU off entirely.

---

## 7. Delivery: how agents "listen"

MCP agents cannot be interrupted mid-turn, so delivery has two paths:

1. **End-of-turn check (main path).** The adapter's background consumer buffers verified messages. A harness hook asks the adapter for unread mail when the agent finishes a turn:
   - The hook process is short-lived and the adapter holds the identity lock, so `agent-bus pending` asks the running adapter over a unix socket at `~/.config/agent-bus/<agent-id>/adapter.sock`. No adapter running, or no mail: it exits silently.
   - Claude Code: a `Stop` hook that runs `agent-bus pending --format=hook`; if mail is waiting, it returns a blocking decision whose reason tells Claude to read its inbox, so Claude continues instead of stopping.
   - OpenCode: a plugin subscribed to the session-idle event that does the same. **Verify** the current OpenCode plugin event names and how a plugin can inject a follow-up prompt.
2. **Idle listener.** An agent with nothing to do calls `wait_for_messages(timeout)`, which blocks until mail arrives or the timeout passes.

Also provide a `SessionStart` hook (Claude Code) that shows a short summary of unread mail and open requests when a session starts.

---

## 8. Safety rules

1. **Messages are untrusted input.** A valid signature proves who sent a message, not that its content is safe; a trusted agent can relay injected instructions from a poisoned dependency or web page. The adapter wraps every delivered message in a clear "data, not instructions" frame, and CLAUDE.md / AGENTS.md guidance says the same.
2. **Auto-accept.** **Decision:** the adapter flag `--auto-accept-messages` is on by default, because all agents belong to the same owner; `--no-auto-accept-messages` turns it off. The frame from rule 1 stays the same in both modes (messages are peer data, never the owner's instructions). The flag decides whether the agent may act on a verified request without the owner:
   - On: the agent may move a `feature_request` or `bug_report` to `accepted` and work on it.
   - Off: moving a request to `accepted` requires the owner (busctl `approve`, the monitor, or the owner in the session's prompt), and the delivered frame says "discuss, don't act without the owner's approval".
   - Owner approval over the bus: `busctl approve` (and later the monitor) publishes a `status: accepted` message in the request's thread, signed by `human@<owner>`. The adapter honours it as approval only when that key is in `known_agents` with `source=pinned`.
3. **Loop guard.** `hops` counts automated replies in a thread. The adapter refuses to send an automated reply when `hops ≥ 6` (`--max-hops`) until a human posts in the thread. The end-of-turn hook also has a cooldown so two agents cannot keep waking each other.
4. **Rate limits** in the adapter: 30 messages per agent per 10 minutes by default (`--rate-limit`).
5. **Secrets.** The adapter scans outgoing bodies for obvious secrets (tokens, private keys) and blocks the send.

---

## 9. MCP tools (adapter)

| Tool | Parameters | Returns |
|---|---|---|
| `whoami` | — | agent ID, fingerprint, joined topics |
| `send` | `to` (agent ID), `type`, `body`, `thread?`, `reply_to?` | message ID |
| `publish` | `topic`, `type`, `body`, `thread?`, `reply_to?` | message ID |
| `inbox` | `limit?`, `types?`, `topic?` | verified unread messages |
| `ack` | `message_ids` | ok (commits offsets) |
| `wait_for_messages` | `timeout_s` | messages or empty |
| `join` / `leave` | `topic`, `from?` | ok |
| `list_topics` | — | topics with descriptions |
| `list_agents` | — | agents, last seen, fingerprint, trust state |
| `history` | `topic` or `thread`, `limit`, `before?` | messages |
| `requests` | `agent?`, `status?` | open feature requests / bug reports |
| `set_request_status` | `request_id`, `status`, `note` | ok (`accepted` needs `--auto-accept-messages`, §8 rule 2) |

The adapter fills in `id`, `ts`, `from`, `hops`, `key_fp`, and the signature; agents never construct envelopes.

---

## 10. busctl (human/admin CLI)

`register <agent-id>`, `revoke <agent-id>`, `topic create|list`, `tls init`, `tls reissue` (new broker certificate from the existing CA, for an expired certificate or a moved broker; `tls init --force` replaces the CA too), `tail [topic|--all]`, `thread <id>`, `requests`, `pin <agent-id> <fingerprint>`, `known`, `alerts` (key mismatches, rejected messages, unknown senders), `post` (as `human@<owner>`), `approve <request-id>`.

---

## 10a. Bus monitor

A small, separate Go service that connects to the broker and shows the messages flowing around, in a browser.

**v1: read-only.**

- Consumes every `agents.*` topic with its own read-only principal and its own consumer group (its reading never moves any agent's read position).
- Verifies each message's signature and `to` field and shows the result: sender, recipient or topic, type, thread, verified / rejected (with reason). It does no dedupe or timestamp checks; a duplicate ID is shown as such.
- Live view (server-sent events) plus browsing by topic, thread and agent, and the open requests from `agents.requests`.
- Verification uses the key in `agents.registry` only. The monitor keeps no `known_agents` store and pins nothing; a sender missing from the registry, or a signature that doesn't match its registry key, is shown as rejected with the reason.
- Listens on `127.0.0.1` only, over plain HTTP. To view it from another machine, use an SSH tunnel.

**Later: sending.**

- The monitor posts as `human@<owner>`, using the password-protected key described below. The owner logs in with the master password, and the key is decrypted only in memory.
- `--listen` on other addresses becomes allowed, served over HTTPS with a certificate from the bus CA (§4), and every page needs the master password.
- Adapters can be configured to treat `human@<owner>` as the owner (`--owner-identity`; the default is `human@` plus the owner part of the adapter's own agent ID). Messages from it are delivered as user commands rather than as peer messages, and count as the human post that resets the loop guard (§8 rule 3). Approval of requests already works from milestone 6 through the `status: accepted` path (§8 rule 2).
- This only applies when that key is in `known_agents` with `source=pinned`; a TOFU-pinned owner key is treated like any other agent. Machine setup pins it with `busctl pin`.

**The `human@<owner>` key.** It speaks with the owner's authority, so it is the most valuable secret in the system. `busctl register human@<owner>` creates it and asks for the master password. The private key is stored encrypted with a key derived from that password (Argon2id) and never unencrypted. busctl `post` / `approve` and the monitor all use this key.

---

## 11. Milestones

1. **Broker up:** Redpanda Pod manifest started with `podman kube play`, TLS, SASL/SCRAM, auto-create off; busctl `tls init`, `register` and `topic create`.
2. **Adapter core:** envelope, JCS, Ed25519 sign/verify, TOFU store with `--[no-]auto-accept-new-keys`, dedupe store, identity lock, `send` / `publish` / `inbox` / `ack`.
3. **MCP surface:** all tools in §9; register with Claude Code and OpenCode.
4. **Delivery hooks:** Claude Code `Stop` and `SessionStart`; OpenCode plugin.
5. **Requests and safety:** request lifecycle, `agents.requests`, `--[no-]auto-accept-messages`, loop guard, rate limits, secret scan.
6. **Ops:** password-protected `human@<owner>` key, busctl `tail` / `alerts` / `post` / `approve`, adapters honouring a pinned owner's `status: accepted` (§8 rule 2), key rotation and revocation, `busctl tls reissue`, setup script per machine (which also pins the `human@<owner>` key).
7. **Monitor (read-only):** live view on localhost, browsing, verification status (§10a v1).
8. **Monitor sending:** login with the master password, posting and approving as `human@<owner>`, HTTPS for non-localhost `--listen`, adapters delivering the owner's messages as user commands (§10a later).

---

## 12. Acceptance tests

- Two agents on different machines exchange a DM; both verify and pin each other on first contact.
- A message re-produced to the same topic with the same ID is rejected (dedupe).
- A message copied into a different topic is rejected (`to` mismatch).
- A message signed with a new key for a known agent is rejected and raises an alert; a properly signed `key_rotation` updates the pin.
- With `--no-auto-accept-new-keys`, a message from an unknown sender is rejected until the owner pins its key.
- A message altered in transit (any envelope field) fails verification.
- A second session that starts the adapter for an identity already in use gets an error from every tool; once the first session ends, a new one picks the identity up.
- With `--auto-accept-messages` (default), an agent can move a request to `accepted`; with `--no-auto-accept-messages`, it cannot until `busctl approve` sends the pinned owner's approval.
- A client cannot connect to the broker without TLS.
- The monitor shows a DM between two agents live, without changing either agent's read position, and shows a tampered message as rejected with its reason.
- (Milestone 8) A message from the pinned `human@<owner>` is delivered as a user command and resets the loop guard; the same identity pinned by TOFU does not.
- An agent offline for a day reads its full backlog in order on reconnect; nothing is lost if the session crashes before `ack`.
- Two agents set to auto-reply stop at the hop limit.
- An agent cannot read another agent's DM topic (ACL denied).
- The private key file is not readable through any path the agent's tools operate in.
- `busctl tail --all` reproduces the full conversation from the log.

---

## 13. Decisions

Settled by the owner on 2026-11-24:

1. Language: Go, with kong for CLI parameters (§2).
2. One session per identity at a time; a new session on the same machine can pick it up (§3).
3. Single-user setup; `--auto-accept-messages` on by default, `--no-auto-accept-messages` to turn it off (§8).
4. TLS to the broker; broker on the local network, tunnels or VPN for anything outside it (§4).
5. No end-to-end encryption; signatures guard integrity; `--auto-accept-new-keys` on by default (§6).
6. A separate bus monitor service, read-only first, sending with a master password later (§10a).
7. The bus deals in identities, not projects; which code an identity works on doesn't matter to it (§3).
8. Identities are per machine and keys are never copied; each adapter is a sidecar proxy holding one identity for one session, and another concurrent session gets another adapter and identity (§3).
9. Monitor: localhost-only plain HTTP in v1; Argon2id-encrypted `human@<owner>` key; owner identity only honoured when pinned (§10a).

10. Libraries: franz-go, the official MCP Go SDK, a JCS library (§2).
11. Identity lock with `flock` plus a PID in the lock file; hooks talk to the adapter over a unix socket (§3, §7).
12. A private CA made by `busctl tls init` (§4).
13. What `--auto-accept-messages` allows, as described in §8 rule 2.
14. Hop limit 6 and 30 messages per 10 minutes as defaults, configurable by flag (§8).
15. The broker runs from a Kubernetes Pod manifest, started with Podman for now; another runtime later (§2).
16. Group topics are open channels: every agent reads and writes all `agents.topic.*` through one prefixed ACL; joining only changes subscriptions (§4).
17. Agents read and write `agents.requests` and `agents.general`, and get `IdempotentWrite` so the adapter keeps Kafka's idempotent producer (§4).
18. `busctl tls reissue` renews only the broker certificate from the existing CA; part of milestone 6 (§10).

**Open question: private topics.** Agents that need a closed channel join a private topic, which needs a join key; the key is handed over in a message that is end-to-end encrypted with the agents' existing keys, sent through the adapter. To settle: whether the join key encrypts the private topic's messages (so the broker and other agents see only ciphertext) or the broker enforces access through ACLs granted on presentation of the key, and how the end-to-end key exchange derives its encryption keys from the identity keys.
