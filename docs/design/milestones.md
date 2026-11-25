# Milestones

[Design index](README.md) | previous: [operations](operations.md) | next: [acceptance tests](acceptance-tests.md)

The build order and what is left. The MVP was milestones 1a, 2, 4 and 5 (subsets of 4 and 6). The
monitor (8) and the OpenCode plugin were deferred by the MVP decision but were built anyway, see
the [index](README.md). Feature specifications live in the linked documents; this page does not
repeat them.

## Status

| Milestone | Status | Remaining |
|---|---|---|
| 1 Broker up (Kafka, Redpanda) | done, replaced by 1a | none (historical) |
| 1a Migrate to NATS JetStream | done | none |
| 2 Adapter core | done, except holds | hold store, `--[no-]auto-accept-new-keys` flags |
| 3 busd | minimal busd built | signed API, private channels, ledger and the rest, see below |
| 4 MCP surface | partly: 8 tools | `history`, `requests`, `set_request_status`, private-channel tools |
| 5 Delivery hooks | done | none |
| 6 Requests and safety | partly | request lifecycle; limits as command-line flags |
| 7 Ops | partly | rotation, revocation by key, `human@` key, `post`/`approve`/`tail`/`alerts` |
| 8 Monitor (read-only) | done | `monitor@<owner>` identity |
| 9 Monitor sending | partly: sends as an agent identity | `human@<owner>` |

Added after the original plan, with their decisions: presence (37), the monitor API (39), agent
removal and mTLS phase A (38), mTLS phase B with the minimal busd (41), the monitor's own logins
(42), shared preferences (43), the VS Code extension (44), the OpenCode plugin and waking idle
sessions (20), [directory v2](directory-v2.md) and public
[channels requested by agents](agent-channels.md) (both built, 2026-11-25).

## Milestones in detail

### 1 Broker up (done, on Kafka)

- **Delivered:** a Redpanda Pod manifest started with `podman kube play`, TLS, SASL/SCRAM,
  auto-create off; agent-busctl `tls init`, `register` and `channel create`.
- **Replaced** by the NATS binding in 1a.

### 1a Migrate to NATS JetStream (done)

- **Delivered:** `internal/broker` is the JetStream implementation behind the message transport
  and admin interfaces of [architecture](architecture.md), with the channel-name mapping inside
  the binding. agent-busctl no longer gets a raw broker admin client, broker channel names or
  access rules.
- **Moved to NATS:** agent-busctl `tls init`, `register` and `channel create`; the deploy
  (`nats-server.conf` with the `BUS` and `CONTROL` accounts, start script, generated `auth.conf`,
  [JetStream binding](jetstream-binding.md)); and `internal/itest`.
- **In place:** the `BUS` and `LEDGER` streams, the `registry` and `catalogue` buckets, the
  per-agent grants, and `agent-busctl --direct` creating the agents' consumers. `AgentRecord`
  gained the `keys` list ([bus model](bus-model.md)).
- **Proved at the raw NATS level:** account isolation, the per-role grants, reply-subject
  redirection (a pulled record redirected onto a subject is delivered but not stored again, an API
  response is stored with the server's payload, nothing lands in `CONTROL`), the delivery
  metadata's stream sequence, confirmed reloads and `MaxDeliver -1`.
- **Removed:** the Kafka code and the Redpanda manifest.
- **Left to later milestones:** the signed-busd tests of the [acceptance tests](acceptance-tests.md)
  (nonces, method checks, idempotency) to milestone 3; the adapter's hold and handled-state tests
  to milestone 2.

### 2 Adapter core (done, except holds)

- **Delivered:** envelope, JCS, Ed25519 sign and verify, TOFU store (pinned public keys,
  `revoked_keys`), handled set (keyed by destination, verified sender and ID; rejected deliveries
  by stream sequence), per-message acks (the agent's `mark_read`: the broker confirms, then
  handled is written), persisted channels, bounded inbox buffer, identity lock, `send` / `publish`
  / `inbox` / `mark_read`.
- **Remaining:** the hold store with held, released and handled states, and
  `--[no-]auto-accept-new-keys` ([delivery](delivery.md)).

### 3 busd (minimal busd built)

The control service ([busd](busd.md)).

- **Built:** the minimal busd with its own login, the enrollment and approval API in `CONTROL`,
  local confirmed reloads, `busd.monitor.*`, the directory projection, head and sync
  ([directory v2](directory-v2.md)), and channel requests ([agent channels](agent-channels.md)).
- **Planned:** signed-request authentication with nonces; private channels
  ([private channels](private-channels.md)) with the membership ledger (stream-sequence boundaries,
  watermarked membership answers), reconciliation, the private catalogue, ledger backup and
  restore, `busd reconcile --adopt`; busd's own service name and signing key (signed responses);
  consumer provisioning (the monitor's `bus-monitor` too); history and registry reads; registry
  writes (register, revoke, `busd.agent.rotate`); the monitor identity; readiness after registry
  catch-up; crash-safe operation intents; supervised restart; agent-busctl as a busd client
  (`--direct` stays for recovery).

### 4 MCP surface (partly)

- **Built:** 8 tools ([MCP tools](mcp-tools.md)); registered with Claude Code and OpenCode.
- **Remaining:** the rest of the tools of the MCP document, private-channel tools included, with
  `history` as the read-only path.

### 5 Delivery hooks (done)

- **Delivered:** Claude Code `Stop` and `SessionStart`; the OpenCode plugin; per-session adapter
  socket and notified-message tracking ([delivery](delivery.md)).

### 6 Requests and safety (partly)

- **Built:** `--[no-]auto-accept-messages`, loop guard (adapter-set `hops`), rate limits, secret
  scan ([safety](safety.md)). The limits are configuration fields, not command-line flags.
- **Planned:** the request lifecycle with its transition table, open requests derived from thread
  history (`requests` tool, `agent-busctl requests`; no projection, [protocol](protocol.md)).

### 7 Ops (partly)

- **Built:** `agent remove`, `directory export`, `tls broker` (reissue the broker certificate),
  mTLS phase A and B (`cert`, enrollment, `ca`, autosign), revocation by certID, `service init`.
- **Planned:** password-protected `human@<owner>` key; `tail` / `alerts` / `post` / `approve`;
  adapters honouring a pinned owner's `status: accepted` ([safety](safety.md) rule 2); key
  rotation (`agent-busctl rotate` through `busd.agent.rotate`, key chain) and revocation by key;
  incident revocation ([admin CLI](admin-cli.md)); a setup script per machine.

### 8 Monitor, read-only (done)

- **Delivered:** live view on localhost, browsing, verification status, its own password and API
  (decision 39), its own read-only logins and config directory (decision 42), shared prefs
  (decision 43), search, and the VS Code extension (decision 44). It is a separate binary and
  container, not hosted in busd ([monitor](monitor-and-api.md)).
- **Remaining:** the `monitor@<owner>` identity.

### 9 Monitor sending (partly)

- **Built:** the monitor sends as one registered agent identity.
- **Planned:** login with the master password, posting and approving as `human@<owner>`, adapters
  delivering the owner's messages as user commands. The key stays unlocked by the owner, never by
  busd on its own ([monitor](monitor-and-api.md)).

## Dependencies

- Private channels, the ledger and the private-channel tools wait for the signed busd (milestone 3).
- The request lifecycle (6) and `history` (4) wait for busd's history reads.
- Holds (2) and key rotation (7) wait for `human@<owner>` and `agent-busctl pin`.
- Cleanup of abandoned channels needs nothing new; it follows [agent channels](agent-channels.md)
  step 3.

## History

Decision 29 set the order: milestone 1 done; busd after the adapter core and before the MCP
surface, so private-channel tools are exposed only after authorisation, the ledger and
reconciliation exist. In practice a minimal busd came first, for enrollment (decision 41), and the
MVP shipped the MCP surface and hooks without the rest of busd.
