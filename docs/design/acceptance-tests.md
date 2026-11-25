# Acceptance tests

[Design index](README.md) | previous: [milestones](milestones.md) | next: [decisions](decisions.md)

What the finished system must pass. Each requirement has a stable ID (`<area>-<number>`), is
grouped by subsystem, and says whether the feature it tests exists. IDs are never reused: a
requirement that is dropped keeps its row marked **dropped**.

The list was kept whole from the original specification. Many requirements concern planned
features (private channels, holds, rotation, the request lifecycle, signed busd) and cannot pass
yet; they are marked **planned** and are future acceptance requirements, not tests that exist.

## Status words

| Status | Meaning |
|---|---|
| **built** | The feature exists. Tests for it live in the packages and integration tests named under [Evidence](#evidence); individual requirements are not mapped to one test function. |
| **partly** | Part of the requirement exists; the note says which part. |
| **planned** | The feature is specified, not built. The requirement cannot pass today. |

## Evidence

- Unit tests run with `make check`; integration tests run with `make itest` against a real
  `nats-server` (`internal/itest`). [docs/development.md](../development.md#tests) lists the cases.
- Integration tests by area: `adapter_test.go` (adapter core), `mcp_test.go` (tools), `hook_test.go`
  and `wake_test.go` (delivery hooks, waiter), `presence_test.go` and `presence_e2e_test.go`
  (presence), `targets_test.go`, `retention_test.go`, `restart_test.go`, `disconnect_test.go` and
  `outage_test.go` (broker restarts and outages), `directory_test.go` (directory v2),
  `channels_test.go` (channels through busd), `mtls_test.go` and `enroll_test.go` (certificate
  login and enrollment), `remove_test.go` (agent removal), `monitor_test.go` and
  `monitor_proxy_test.go` (monitor), `itest_test.go` (JetStream binding, `TestMilestone1a`).

## Identity and trust (IT)

| ID | Requirement | Status |
|---|---|---|
| IT-01 | Two agents on different machines exchange a DM; both verify and pin each other on first contact. | built |
| IT-02 | A message altered in transit (any envelope field) fails verification. | built |
| IT-03 | A forged envelope that copies a legitimate message's ID (bad signature, another sender or another destination) is rejected by stream sequence and does not stop the legitimate message from being delivered. | built |
| IT-04 | Only verified envelopes enter the handled set; a verified envelope whose ID repeats with a different payload is alerted and not delivered. | partly: the handled set is built; the alert for a repeated ID with another payload is not |
| IT-05 | A second session that starts `agent-bus` for an identity already in use exits with an error naming the identity and the holder's PID. | built |
| IT-06 | Once the first session ends, a new one picks the identity up. | built |
| IT-07 | No adapter tool returns or reads key material, and keys are never inside a project directory. | built |
| IT-08 | An online adapter rejects a revoked key and never treats it as a fresh TOFU candidate. | built for agents removed through `agent-busctl` and the directory; revocation by key is planned |
| IT-09 | An adapter that was offline for a long time rejects a revoked key too, also across restarts. | built, as IT-08 (the snapshot keeps every revocation) |
| IT-10 | A message signed with a new key for a known agent is held and raises an alert; it is delivered after `agent-busctl pin`. | planned: today a changed key is rejected terminally, with an alert, and acked |
| IT-11 | A key reached through a valid rotation chain in the registry updates the pin. | planned |
| IT-12 | A receiver that was offline through two rotations verifies its backlog and the new messages. | planned |
| IT-13 | The monitor verifies pre-rotation history. | planned |
| IT-14 | After `agent-busctl revoke` and `agent-busctl rotate`, receivers that know the agent hold its messages until `agent-busctl pin` and then deliver them, even with `--auto-accept-new-keys`. | planned |
| IT-15 | After revoke and rotate, a receiver that never met the agent pins the new key by TOFU as usual. | planned |
| IT-16 | With `--no-auto-accept-new-keys`, a message from an unknown sender is held and alerted. | planned: the flag is not exposed on the command line |
| IT-17 | Once the owner pins the sender (IT-16), the message is delivered. | planned |
| IT-18 | `agent-busctl rotate` sends a signed `busd.agent.rotate`. | planned |
| IT-19 | A `busd.agent.rotate` signed by a revoked key is refused; after a revocation only the owner's signature rotates the key. | planned |
| IT-20 | The monitor reads milestone-1 `AgentRecord` and `TopicRecord` records without false signature alerts. | built |
| IT-21 | `agent-busctl revoke --incident` revokes the agent's access, deletes its account, revokes its key and ends its busd authority and memberships; its later busd requests and broker connections fail. | planned |
| IT-22 | `agent-busctl revoke --incident --direct` does the access and key parts and reports the busd part as pending. | planned |

## Message integrity and dedupe (MSG)

| ID | Requirement | Status |
|---|---|---|
| MSG-01 | A message re-produced to the same channel with the same ID is skipped (dedupe). | built |
| MSG-02 | A message copied into a different channel is rejected (`to` mismatch). | built |
| MSG-03 | A message published twice with the same `Nats-Msg-Id` inside the dedupe window is stored once. | built |
| MSG-04 | The same envelope ID on another subject is stored. | built |
| MSG-05 | A retry after the dedupe window is stored again and the receiver's handled set skips it. | built |
| MSG-06 | A record over 96 KB is rejected and acked. | built |
| MSG-07 | An agent's own posts to `general` and a joined channel are never delivered back to it as unhandled mail. | built |

## Delivery and recovery (DLV)

| ID | Requirement | Status |
|---|---|---|
| DLV-01 | An agent offline for a day reads its full backlog in order on reconnect. | built |
| DLV-02 | Nothing is lost if the session crashes before `mark_read`. | built |
| DLV-03 | Ack B before A, then crash: A is redelivered. | built |
| DLV-04 | A crash before the ack is persisted: the message is redelivered or skipped, never lost. | built |
| DLV-05 | A crash after the ack is persisted: the message is redelivered or skipped, never lost. | built |
| DLV-06 | A message sent before the adapter's first start is delivered. | built |
| DLV-07 | After joining a channel and restarting, the adapter resumes where it was. | built |
| DLV-08 | With a consumer lost on the broker, busd recreates it from the ledger's join boundary (group) or from the beginning (others); unread group mail is recovered and the handled set skips replayed mail. | planned: recreation needs the ledger |
| DLV-09 | A backlog larger than the inbox buffer replays without loss or crash. | built |
| DLV-10 | `history` on acknowledged mail leaves the unread state unchanged. | planned: there is no `history` tool |
| DLV-11 | `join(from="earliest")` leaves the unread state unchanged. | planned: the read-only history path is not built |
| DLV-12 | Mail arriving at an idle session wakes it through the waiter. | built |
| DLV-13 | With no waiter (it ran out, or no hook is set up), mail waits for the next hook or session. | built |
| DLV-14 | Mail during the waiter's cooldown is notified when it is over (the waiter) or at the next hook. | built |
| DLV-15 | Mail arriving while earlier mail is still unacked prompts only for the new message. | built |
| DLV-16 | With two sessions on one identity, only the lock holder gets delivery prompts. | built |
| DLV-17 | A JetStream call the server denies is reported with its reason (permissions violation, "not a member") within a short timeout, not as a hang or a generic timeout. | partly: permission violations are reported; "not a member" needs private channels |
| DLV-18 | A held message is on disk and acked on the broker before it is released, so it survives an adapter restart and a `nats-server` restart and never uses up `MaxAckPending`. | planned: there is no hold store |
| DLV-19 | Unacked mail stays pending across a restart and is never dropped by the delivery limit. | built |
| DLV-20 | A redelivered handled or held ID is acked, and a buffered one gets `InProgress`. | built for handled IDs; held IDs are planned |
| DLV-21 | A hold-release crash before release, after release before the agent sees the message, and after it saw the message but before `mark_read` each leave it delivered exactly when handled state is committed, never lost. | planned |
| DLV-22 | The hold record is deleted only after the handled set write, and a redelivery of a held sequence is acked. | planned |
| DLV-23 | A status change whose thread history is missing is held, then accepted once the history is available. | planned: no hold store |
| DLV-24 | With busd stopped, DMs, `general`, groups and (planned) private-channel messages from pinned senders keep flowing. | partly: DMs, `general` and groups built; private channels planned |
| DLV-25 | With busd stopped, creating a channel, joining, `requests`, inviting, removing and agent-busctl management commands fail with a clear error and change nothing. | partly: channel requests fail clearly and change nothing; the rest follows the planned busd |
| DLV-26 | With busd stopped, a message from an unpinned sender is held until busd is back, never rejected. | built in the form "left unacked and redelivered" for an unknown sender; holds are planned |

## Requests, safety and targets (REQ)

| ID | Requirement | Status |
|---|---|---|
| REQ-01 | With `--auto-accept-messages` (default), an agent can move a request to `accepted`. | built |
| REQ-02 | With `--no-auto-accept-messages`, it cannot until `agent-busctl approve` sends the pinned owner's approval. | planned: `approve` is not built |
| REQ-03 | (Milestone 9) A message from the pinned `human@<owner>` is delivered as a user command and resets the loop guard. | planned |
| REQ-04 | The same identity pinned by TOFU does not reset the loop guard. | planned with REQ-03 |
| REQ-05 | Open requests are derived from thread history: `requests` and `agent-busctl requests` list the same open requests an agent folds from the thread, in canonical order, and write no record. | planned |
| REQ-06 | Requests in a private channel are derived from its own history. | planned |
| REQ-07 | Status changes through generic `send` / `publish` with `type=status` are checked like `set_request_status`. | planned |
| REQ-08 | An unrelated agent cannot change a request's status; `rejected` is terminal. | planned |
| REQ-09 | A group request gets its assignee from the first non-requester status change. | planned |
| REQ-10 | With `--no-auto-accept-messages`, the pinned owner's approval is followed by legitimate progress. | planned |
| REQ-11 | A send without `reply_to` or `thread` inherits from a message delivered four minutes ago, but not from one delivered or acked six minutes ago (the window is five minutes). | built |
| REQ-12 | A send naming a `reply_to` that is not found after the history lookup is refused. | partly: refused when the agent has neither read nor sent it; there is no history lookup |
| REQ-13 | Two agents set to auto-reply stop at the hop limit when they alternate `send` and `publish`. | built |
| REQ-14 | They also stop when they omit `reply_to` or `thread`, branch replies, or restart their adapters. | built |
| REQ-15 | A TOFU-pinned owner cannot reset the hop count. | built (nothing resets it yet; see REQ-03) |
| REQ-16 | Mail aimed at other agents stays out of an agent's inbox with `--deliver=targeted`. | built |

## Access control and broker isolation (ACC)

| ID | Requirement | Status |
|---|---|---|
| ACC-01 | A client cannot connect to the broker without TLS. | built |
| ACC-02 | An agent cannot read another agent's DM channel: bob has no consumer create, delete or update grant, cannot pull from or ack alice's consumer, and cannot direct-get a message of stream `BUS`. The only direct get an agent holds is on `presence.*` of stream `PRESENCE`. | built |
| ACC-03 | After a restart that loses local context, either participant of a DM request rebuilds the full thread (both DM channels, in stream order) through `history`; a third agent gets only its own messages. | planned: `history` and the request fold |
| ACC-04 | Redirection: an agent that calls any JetStream API it holds (`MSG.NEXT`, `INFO`, an ack) with a reply subject of `$KV.registry.<key>`, `$KV.catalogue.<key>`, `ledger.<x>` or `busd.<method>` changes nothing in `CONTROL`. | built |
| ACC-05 | A record pulled onto a private subject through a reply subject is delivered to a listener there, but `BUS`'s message count and last sequence are unchanged and no record exists on that subject. | built |
| ACC-06 | A JetStream API response sent to such a subject is stored, with the server's payload. | built |
| ACC-07 | A signed busd request stored in a DM and pulled onto `busd.<other method>` is refused (method mismatch). | planned: needs the signed busd protocol |
| ACC-08 | An unsigned or unauthorised payload pulled onto any busd subject changes nothing on the `BUS` side (no channel, member, consumer or grant) and uses up no other caller's nonce quota. | partly: nothing changes on the `BUS` side; nonce quotas are planned |
| ACC-09 | Unauthorised, mismatched or replayed requests have no effect at all; a valid signed request that arrives by redirection executes exactly the signed operation. | planned |
| ACC-10 | An agent cannot create, update or delete a consumer; busd creates them on register and deletes them on removal. | built; join, invite, leave and incident are planned |
| ACC-11 | The monitor cannot create, update or delete a consumer or touch an agent consumer's name. | built |
| ACC-12 | busd creates the monitor's consumer `bus-monitor`. | planned: the monitor uses ordered consumers of its own |
| ACC-13 | `join` of an uncatalogued channel is refused, and the adapter refuses to publish to one. | built |
| ACC-14 | An agent that appends to an uncatalogued subject is read by nobody and `history` refuses it. | partly: read by nobody; `history` is planned |
| ACC-15 | After `busd` rewrites `auth.conf` and reloads the server, a new grant works and a revoked one stops working on live connections at once. | built |
| ACC-16 | A removed user is disconnected. | built |
| ACC-17 | A busd crash drops no agent connection. | built |
| ACC-18 | An invalid candidate config is refused before it replaces the file. | built |
| ACC-19 | A reload that did not take is detected and finished when busd restarts. | partly: detected and kept out of the directory; finishing on restart is planned |
| ACC-20 | No agent can subscribe to `busd.>`, publish on a busd subject outside its grants, or reach `CONTROL`; only the `busd` account answers busd requests. | built |
| ACC-21 | An agent with a client certificate logs in on the certificate listener; after revocation by certID the same certificate no longer logs in. | built (`mtls_test.go`) |
| ACC-22 | Enrollment: a request with a CSR is approved and the new machine talks to an existing agent. | built (`enroll_test.go`) |
| ACC-23 | A rejected enrollment request fails with the reason and creates nothing. | built |
| ACC-24 | A request carrying the autosign secret is approved without the owner; one without it is not. | built |
| ACC-25 | The enrollment login can only ask. Limits and expiry: [access control](access-control.md#phase-b-implemented-the-enrollment-flow). | built |
| ACC-26 | The monitor's read-only logins cannot publish to any message subject, cannot approve or reject an enrollment, and can reach only their own reply inbox space. | built (`internal/broker/principals_test.go`) |
| ACC-27 | `agent-busctl tail --all` reproduces the full conversation from the log. | planned: `tail` is not built |
| ACC-28 | Tests that need the broker's behaviour run against the JetStream binding. | built |

## Directory and channels (DIR)

These were added after the original list, for [directory v2](directory-v2.md) and
[channels requested by agents](agent-channels.md).

| ID | Requirement | Status |
|---|---|---|
| DIR-01 | A running agent learns of a new agent from the head, without a restart. | built (`directory_test.go`) |
| DIR-02 | The directory grants and replies stay in their place: agents cannot publish `directory.head` or reach another agent's reply subject. | built |
| DIR-03 | A freshly enrolled machine knows the directory at its first start. | built |
| DIR-04 | Agents create, join and manage channels through busd. | built (`channels_test.go`) |

## busd (BUSD)

| ID | Requirement | Status |
|---|---|---|
| BUSD-01 | A replayed busd request (same nonce) is refused. | planned |
| BUSD-02 | A request with a changed body, method or target is refused. | planned |
| BUSD-03 | A request signed by a revoked or unknown key is refused. | planned |
| BUSD-04 | A request with a caller-supplied key is refused. | planned |
| BUSD-05 | A request whose subject does not match its signed method is refused. | planned |
| BUSD-06 | A retry with the same idempotency ID returns the first result and changes nothing twice. | planned |
| BUSD-07 | A busd request with an expired nonce, or one whose nonce was already used, is refused. | planned |
| BUSD-08 | A forged or unsigned busd response, a nonce response included, is refused by the adapter. | planned: only the enrollment and channel replies are signed |
| BUSD-09 | A nonce request not signed by its claimed caller is refused and costs that caller no quota, also when redirected. | planned |
| BUSD-10 | busd answers nothing before it has caught up with the registry. | partly: sync requests before the first projection get an `error` reply |
| BUSD-11 | A nonce does not survive a busd restart. | planned |
| BUSD-12 | The same idempotency ID with a different request is refused, also beyond the dedupe window. | planned |
| BUSD-13 | Crash recovery: a kill after the intent is stored but before any effect ends, on retry with the same idempotency ID, in the same single operation. | planned |
| BUSD-14 | A kill between effects ends, on retry, in the same single operation. | planned |
| BUSD-15 | A kill after the effects but before the result ends, on retry, in the same single operation. | planned |
| BUSD-16 | After any such kill, a new private channel keeps its one assigned ID and no second channel appears. | planned |
| BUSD-17 | A retry with a fresh nonce and signature hashes the same; the same ID with a different request is refused. | planned |
| BUSD-18 | A killed busd is restarted by its supervisor. | planned |
| BUSD-19 | `agent-busctl --direct` commands leave private-channel membership unchanged; reconciliation does not undo or redo them. | planned |
| BUSD-20 | With a missing, empty or reset ledger busd refuses to revoke anything and reports it. | planned |
| BUSD-21 | `busd reconcile --adopt` or a backup restore brings the ledger back. | planned |

## Private channels (PRIV)

All planned: private channels are not built ([private channels](private-channels.md)).

| ID | Requirement | Status |
|---|---|---|
| PRIV-01 | A non-member that connects to the broker directly cannot read or write a private channel (access denied) and cannot see its name; a removed agent cannot either. | planned |
| PRIV-02 | An agent that is not the manager or the owner cannot invite or remove members through busd. | planned |
| PRIV-03 | A member cannot remove another member; a member can leave itself. | planned |
| PRIV-04 | An invitation of an unregistered or revoked identity is refused. | planned |
| PRIV-05 | Creating, inviting and removing in a private channel leaves no name, description, member or request in the public catalogue. | planned |
| PRIV-06 | The private catalogue is served only to members and the owner. | planned |
| PRIV-07 | After removal, busd has deleted the member's consumer (waiting pulls end). | planned |
| PRIV-08 | After removal, the adapter drops the channel's pending mail, stops fetching, and does not treat the access-denied error as a handled message or loop on reconnect. | planned |
| PRIV-09 | After a new invitation the member reads the whole retained history, including what was sent while it was out. | planned |
| PRIV-10 | `leave` on a private channel reports done only after the broker confirmed the access change. | planned |
| PRIV-11 | `leave` on a group channel deletes its read cursor through busd. | built for channels through busd (`channels_test.go`); `agent-busctl` removes cursors directly |
| PRIV-12 | `unsubscribe` on a private channel keeps access. | planned |
| PRIV-13 | An invitation sends the invitee a signed `invite` DM; `join` without membership fails with "not a member" and the denied read is not treated as handled mail. | planned |
| PRIV-14 | A status change in a private channel follows the same assignee rule as in a group channel. | planned |
| PRIV-15 | A manager that leaves, is removed or is revoked leaves the channel with no manager; the owner can still act and can name a new one; agents cannot hand management over. | planned |
| PRIV-16 | Boundary gap: a former member's record redirected into the channel in the gap before the removal boundary is recorded is handled as [private channels](private-channels.md) documents (accepted until the boundary is recorded). | planned |
| PRIV-17 | Boundary gap: a record stored before a new member's grant takes effect is counted as the member's own. | planned |
| PRIV-18 | A crash before the removal boundary is recorded keeps the interval open until recovery records it, and the boundary is not changed afterwards. | planned |
| PRIV-19 | A watermark snapshot never covers a sequence past a pending change. | planned |
| PRIV-20 | A crash between the ledger write and the broker change is finished on busd's restart. | planned |
| PRIV-21 | Concurrent invite and remove on one channel end in one consistent state. | planned |
| PRIV-22 | An old retry cannot bring back a removed member. | planned |
| PRIV-23 | Reconciliation repairs access changed behind busd's back. | planned |
| PRIV-24 | With busd stopped, a private-channel message above the cached watermark, and mail checked by an adapter cold-started without caches, are held until busd is back, never rejected. | planned |
| PRIV-25 | Defence in depth (the test stores the record directly, as a grant mistake would): a former member's message (a new ID and body, any `ts`) after its removal boundary is rejected and alerted. | planned |
| PRIV-26 | An old message replayed onto the channel after the removal boundary is rejected. | planned |
| PRIV-27 | The same record from a member whose sequence lies inside an interval is accepted, also for a member removed and invited again (messages in the gap are rejected). | planned |
| PRIV-28 | A non-member's redirected record is rejected. | planned |
| PRIV-29 | A message above the cached watermark makes the adapter refresh first; with busd unreachable it is held, not rejected. | planned |
| PRIV-30 | `history` and status folding apply the same membership rule; the pinned owner always passes. | planned |

## Monitor (MON)

| ID | Requirement | Status |
|---|---|---|
| MON-01 | The monitor shows a DM between two agents live, without changing either agent's read position. | built |
| MON-02 | The monitor shows a tampered message as rejected with its reason. | built |
| MON-03 | Shared preferences: read markers merge by maximum, favourites and folded sections are replaced, a `prefs` event reaches connected clients, and the file survives a restart of the monitor. | built (`internal/monitor/prefs_test.go`) |
| MON-04 | The monitor, a separate process, reads and shows private-channel messages marked private. | planned |
| MON-05 | `agent-busctl post --channel` makes busd grant `human@<owner>` access first. | planned |
| MON-06 | The monitor signs as `monitor@<owner>` and cannot write, create a consumer, change access or call any owner-only or mutating busd method, and never holds the owner key. | planned: the monitor signs as an ordinary agent identity today and has read-only logins |
