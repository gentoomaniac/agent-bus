# Milestones

[Design index](README.md) | previous: [operations](operations.md) | next: [acceptance tests](acceptance-tests.md)

The build order, with what exists today. The MVP is milestones 1a, 2, 4 and 5 (subsets of 4 and
6); the monitor (8) and the OpenCode plugin were deferred by the MVP decision but were built
anyway, see the [index](README.md).

| Milestone | Status |
|---|---|
| 1 Broker up (Kafka, Redpanda) | done, then replaced by 1a |
| 1a Migrate to NATS JetStream | done |
| 2 Adapter core | done, except holds (see [delivery](delivery.md)) |
| 3 busd | **planned** |
| 4 MCP surface | partly: 8 tools ([MCP tools](mcp-tools.md)) |
| 5 Delivery hooks | done (Claude Code hooks, waiter, OpenCode plugin) |
| 6 Requests and safety | partly: loop guard, rate limit, secret scan, auto-accept flag; request lifecycle **planned** |
| 7 Ops | partly: `agent remove`, `directory export`, mTLS phase A; rotation, revocation, `human@` key, `post`/`approve`/`tail`/`alerts` **planned** |
| 8 Monitor (read-only) | done (with its own password and API, decision 39) |
| 9 Monitor sending | partly: sends as an agent identity; `human@<owner>` **planned** |

## The milestones

1. **Broker up (done, on Kafka):** a Redpanda Pod manifest started with `podman kube play`, TLS, SASL/SCRAM, auto-create off; agent-busctl `tls init`, `register` and `channel create`. Replaced by the NATS binding in 1a.
1a. **Migrate to NATS JetStream:** `internal/broker` becomes the JetStream implementation behind the message transport and admin interfaces of [architecture](architecture.md), with the channel-name mapping inside the binding (this absorbs the earlier refactor follow-up: agent-busctl no longer gets a raw broker admin client, broker channel names or access rules). agent-busctl `tls init`, `register` and `channel create`, the deploy (`nats-server.conf` with the `BUS` and `CONTROL` accounts, start script, generated `auth.conf`, [JetStream binding](jetstream-binding.md)) and `internal/itest` move to NATS: the `BUS` and `LEDGER` streams, the `registry` and `catalogue` buckets, the per-agent grants, and `agent-busctl --direct` creating the agents' consumers (busd takes that over in milestone 3). The integration test proves everything [JetStream binding](jetstream-binding.md) lists as not yet proved at the raw NATS level: account isolation, the per-role grants, reply-subject redirection (a pulled record redirected onto a subject is delivered but not stored again, an API response is stored with the server's payload, nothing lands in `CONTROL`), the delivery metadata's stream sequence, confirmed reloads and `MaxDeliver -1`. The signed-busd parts of the [acceptance tests](acceptance-tests.md) tests (nonces, method checks, idempotency) belong to milestone 3, and the adapter's hold and handled-state tests to milestone 2. `AgentRecord` gains the `keys` list ([bus model](bus-model.md)). The Kafka code and the Redpanda manifest are removed.
2. **Adapter core:** envelope, JCS, Ed25519 sign/verify, TOFU store (pinned public keys, `revoked_keys`) with `--[no-]auto-accept-new-keys`, handled set (keyed by destination, verified sender and ID; rejected deliveries by stream sequence), hold store with held, released and handled states, per-message acks (the agent's `mark_read`: broker confirms, then handled is written), persisted channels, bounded inbox buffer, identity lock, `send` / `publish` / `inbox` / `mark_read`.
3. **busd:** the control service ([busd](busd.md)): signed-request authentication with nonces, private channels ([private channels](private-channels.md)), the membership ledger with stream-sequence boundaries and watermarked membership answers, reconciliation, the private catalogue, ledger backup and restore, `busd reconcile --adopt` ([private channels](private-channels.md)), busd's own broker connections, service name and signing key (signed responses), consumer provisioning (the monitor's `bus-monitor` too), history and registry reads, registry writes (register, revoke, `busd.agent.rotate`), the monitor identity, confirmed reloads, readiness after registry catch-up, crash-safe operation intents, supervised restart, and agent-busctl as a busd client (`--direct` stays for recovery).
4. **MCP surface:** all tools in [MCP tools](mcp-tools.md), private-channel tools included, with `history` as the read-only path; register with Claude Code and OpenCode.
5. **Delivery hooks:** Claude Code `Stop` and `SessionStart`; OpenCode plugin; per-session adapter socket and notified-message tracking ([delivery](delivery.md)).
6. **Requests and safety:** request lifecycle with its transition table, open requests derived from thread history (`requests` tool, `agent-busctl requests`; no projection, [protocol](protocol.md)), `--[no-]auto-accept-messages`, loop guard (adapter-set `hops`), rate limits, secret scan.
7. **Ops:** password-protected `human@<owner>` key, agent-busctl `tail` / `alerts` / `post` / `approve`, adapters honouring a pinned owner's `status: accepted` ([safety](safety.md) rule 2), key rotation (`agent-busctl rotate` through `busd.agent.rotate`, key chain) and revocation, incident revocation: access revoked and account deleted, key revoked, busd authority removed ([admin CLI](admin-cli.md)), `agent-busctl tls reissue`, setup script per machine (which also pins the `human@<owner>` key).
8. **Monitor (read-only):** live view on localhost, browsing, verification status, `monitor@<owner>` identity ([monitor](monitor-and-api.md) v1); may be hosted alongside busd ([busd](busd.md)).
9. **Monitor sending:** login with the master password, posting and approving as `human@<owner>`, HTTPS for non-localhost `--listen`, adapters delivering the owner's messages as user commands ([monitor](monitor-and-api.md) later). The key stays unlocked by the owner, never by busd on its own.

Added after the original plan: presence (decision 37), the monitor API (39), agent removal and mTLS phase A (38, [access control](access-control.md)).
