# Directory v2: versioned, announced, diff-only

Status: built (2026-11-25), as the owner approved it; what the build settled is in
[As built](#as-built), the upgrade steps in [Migration](#migration). Replaces the once-a-minute full fetch over the enrollment login
(commits 80c500a..8be64cd). Based on a design review by GPT 6.1 sol, with the owner's decisions
below.

## Problem

Agents check senders and recipients against a local copy of the directory
(`~/.config/agent-bus/directory/`). The merged fix fetches the whole directory from busd every
minute and on a miss, over the shared enrollment login. The owner wants:

1. Updates noticed much sooner than once a minute, but a fetch only when something changed, and
   only the diff.
2. The protocol announces new agents.
3. No extra traffic just to check; piggyback on what is already sent.

## Decisions

| Question | Decision |
|---|---|
| Transport | The agents' own mTLS login in the BUS account. busd already has a BUS connection (`cmd/busd/main.go`, `bus-admin`). No enrollment login and no password file on agents. No account export/import. |
| Signalling | busd pushes a signed **head** (current version) on a subject agents subscribe to. Not presence: presence is fire-and-forget into a stream busd never reads, and beats only every 30 s. |
| Versioning | A version busd counts itself, not the registry KV revision. |
| Announcements | Net changes between versions, not every event. An agent that joins and leaves between two observations is never announced. |
| Recovery | A new epoch signed by the CA is accepted; all pins and revocations are kept and logged loudly. |
| Fallback | The one-minute check stays, as a conditional sync (normally "unchanged"), not a full download. |

## Version

```text
Version = { epoch, revision, state_hash }
```

- `epoch`: identifies this directory's lineage; new only after busd's state is lost or restored.
- `revision`: persistent, strictly increasing; advances only when the published content changes.
- `state_hash`: hash of a deterministic serialisation of the whole directory (records, tombstones,
  catalogue, readership).
- Timestamps are informational only, never used for rollback protection.

Why not the registry KV revision: catalogue changes live in another bucket, group readership comes
from BUS consumers (`internal/trust/directory.go`, `internal/broker/admin.go`), and a recreated or
restored bucket resets revisions. One published projection with its own counter avoids a version
vector.

busd persists the current projection and version together. A restart keeps both.

## Subjects and permissions (BUS account)

| Subject | Who | Purpose |
|---|---|---|
| `directory.head` | busd publishes; agents and the monitor subscribe | Signed current version plus change hints |
| `busd.directory.sync` | agents and the monitor publish (request); busd subscribes | Conditional sync: unchanged, diff or full |

- Agents: publish `busd.directory.sync`, subscribe `directory.head` and their existing
  `_INBOX.<token>.>`. No KV write, no new JetStream API grants.
- busd: subscribe `busd.directory.sync`, publish `directory.head` and replies to approved inbox
  prefixes only (no wildcards, no empty tokens, no reply outside the caller's inbox prefix).
- Sync is a public read: no authorisation on a claimed caller identity.

## Messages

All signed payloads use the merged `{payload, signature}` envelope: the CA key signs the exact
payload bytes, with a separate domain prefix per kind (`agent-bus/directory-head/v1\n`,
`agent-bus/directory-sync/v1\n`).

**Head** (busd → all, right after a change and then every 5 s):

```json
{
  "epoch": "…", "revision": 42, "state_hash": "…",
  "changes": [{"agent_id": "worker@owner", "kind": "joined"}]
}
```

`changes` are hints (joined, removed, rekeyed, channels), bounded and possibly truncated. Agents
never install records from a head.

**Sync request** (agent → busd):

```json
{ "challenge": "<random>", "since": {"epoch": "…", "revision": 39, "state_hash": "…"} }
```

**Sync response** (signed):

```json
{
  "request_hash": "…", "kind": "diff",
  "epoch": "…", "from": 39, "base_hash": "…", "to": 42, "state_hash": "…",
  "records": ["complete changed AgentRecords"],
  "catalogue": {"team": ["worker@owner"]}
}
```

- `kind`: `unchanged`, `diff`, `full` or `error` (errors signed too; transport errors stay
  transport errors).
- `request_hash` binds the reply to this request and its challenge: no replay of old snapshots,
  no clock dependence.
- `catalogue` absent = unchanged; present and empty = empty.

## Diff semantics

- Coalesced state change from `from` to `to`: complete current records for every agent that
  changed, the whole catalogue if it changed. Intermediate revisions are skipped.
- Removals are permanent tombstone records, including revoked-key history. Absence never means
  removal.
- busd keeps a bounded cache of earlier projections to compute diffs. If the exact
  `(epoch, from, base_hash)` is not cached, it answers `full`. Losing the cache on restart only
  costs one full fetch.
- An agent applies a diff only onto its exact base; otherwise it asks for a full sync.

## busd

1. **Recover**: load the persisted projection and version. One busd is the only writer.
2. **Reconcile**: under the existing mutation lock, build a coherent directory from registry,
   catalogue and readership.
3. **Commit**: if the content changed, advance `revision` and persist projection and version.
4. **Publish**: announce the head; retry on failure and on reconnect. Re-announce the cached head
   every 5 s.
5. **Serve**: answer sync requests from the committed projection only.

Reconcile after each successful mutation, on registry and catalogue watches, and every 5 s as a
backup (CLI changes and consumer-based membership must not slip through). Announce "joined" only
after enrollment has finished (record, cursors, auth reload), not right after the registry write.

## Agent

1. Load the committed local snapshot and checkpoint.
2. Subscribe to `directory.head`, flush, then sync. Again after every reconnect.
3. On a valid head newer than the checkpoint: schedule one sync; further heads only raise the
   wanted version.
4. Fetch outside the adapter and trust locks.
5. Verify signature, request binding, epoch, base, increasing revision, record validity, and the
   resulting `state_hash`.
6. Commit snapshot and checkpoint atomically; install revocations into the trust store before any
   further message is verified.
7. Emit local directory notices; retry held unknown-sender deliveries.

Also: sync at start, on reconnect, on SIGHUP, on a miss (rate-limited), and every minute as the
fallback. Duplicate or reordered heads are harmless. Same epoch and revision with a different hash
is a fault: log it and do a full sync.

**New epoch**: accept it if CA-signed, do a full sync, keep every pin and revocation, log loudly.
A restore can never un-revoke a key or replace a pinned key.

## Local storage

- One snapshot file holding records, catalogue and checkpoint, replaced atomically (temp file,
  fsync, rename). The per-agent `.json` files become exports written from it.
- Several identities share `<config>/directory`: commit under a cross-process lock (flock) and
  recheck the base under it.
- Pins and remembered revocations are untouched by a sync; a signed new key does not replace a
  pin.

## Announcements to the model and the monitor

- After a verified apply, the agent queues a local system notice such as "Directory updated:
  worker@owner joined; old@owner removed". It is shown in `inbox` and `wait_for_messages`
  separately from peer messages, goes through the delivery hooks, is deduplicated per version,
  and invites no reply.
- The first sync summarises the directory; it does not announce every existing agent as new.
- "Registered" is not "online": presence stays separate.
- The monitor uses heads to refresh its agent list and catalogue at once, keeps its 5 s discovery
  as backup, and shows sync failures separately from presence.

## What it fixes in the merged code

| Finding | Where | Fixed by |
|---|---|---|
| Approval reply's directory is unsigned and its IDs unchecked (path traversal) | `internal/enrollclient/enrollclient.go` `writeDirectory` | ID validation in `FileDirectory.Write` (small-fixes branch); v2 then syncs right after enrollment |
| A fetch on a miss doesn't install revocations | `internal/adapter/adapter.go` | Agent step 6 |
| Snapshot and checkpoint not atomic, not serialised across processes | `cmd/agent-bus/main.go`, `internal/enrollclient/directory.go` | Local storage |
| `issued_at` is a weak freshness and version check | `internal/enroll/signed.go` | Version plus challenge |
| Signed directory is not a coherent cut | `internal/busd/service.go`, `internal/trust/directory.go` | busd steps 2–3 |
| Network fetch while holding the adapter lock | `internal/adapter/adapter.go` | Agent step 4 |
| A running agent added to a group doesn't read it until restart | `internal/adapter/adapter.go` | Apply catalogue changes to subscriptions after a sync |

## Migration

- Nothing from the v1 fetch has been released, so v1 (`busd.directory` on the enrollment login,
  the password file on agents) is removed rather than kept for compatibility.
- busd and agents must both run v2. An older agent keeps working with its local directory but
  needs the manual `directory export` copy to learn new agents.
- The broker auth files must be regenerated for the new BUS grants.

Upgrade steps for the owner (also in [docker.md](../docker.md#upgrade)):

1. Deploy busd (the broker image) and the agents' `agent-bus` of the same release together: an
   old agent never syncs, and an old busd answers no sync (agents then log "busd is not running"
   and keep their local directory).
2. Regenerate the auth files once: `agent-busctl --direct init` (with the retention flags, if they
   were changed). It gives agents publish `busd.directory.sync` and subscribe `directory.head`, the
   monitor subscribe `directory.head`, and drops `busd.directory` from the enrollment login.
3. Restart the agents' sessions. Each syncs the whole directory once at start, then diffs.
4. Delete `<config-dir>/enroll/password` on agent machines, which v1's `enroll` left there.
5. Agents of an older release that must stay: keep copying `directory export` to them.
6. After restoring busd's config folder from a backup, delete `enroll/directory.json` so that busd
   starts a new epoch; agents refuse an older revision of the same epoch.

## Risks and open points

- **Revocation delay while busd is down**: agents keep their cached directory, so a revocation
  reaches them only when busd is back. A maximum staleness policy is not defined.
- **Metadata in BUS**: heads expose revision numbers and join, leave and rekey events to every BUS
  reader. Keep hostnames, pending approvals and private group membership out of the hints.
- **Abuse**: signatures stop forgery, not flooding. Bound message sizes, signature checks and
  pending syncs; rate-limit busd per connection; jitter retries.
- **Scale**: large snapshots may later need paging with a signed manifest; never apply part of
  one.
- **Key separation**: the CA key signs directories for now. A dedicated directory key, certified
  by the CA, is a later hardening.
- **Single busd**: active/active busd would need shared fencing for version allocation.

## Commit plan

1. Version and projection in busd: reconcile, commit, persist; tests.
2. `busd.directory.sync` on BUS with signed unchanged/diff/full replies; grants; tests.
3. `directory.head` announcements, every 5 s and on change; tests.
4. Agent: snapshot file with cross-process lock, verify and apply, revocations installed;
   subscribe to heads, sync on start, reconnect, miss and fallback; remove v1 and the password
   file.
5. Group subscriptions follow catalogue changes without restart.
6. Directory notices in `inbox`, `wait_for_messages` and hooks.
7. Monitor: refresh on heads; sync status shown.
8. Docs: access-control, agent-busctl, claude-code, troubleshooting, upgrade notes.

## As built

Where the build refined or departed from the text above.

**busd's projection (commit 1).**

- busd keeps the projection and its version in `<config-dir>/enroll/directory.json`, next to the
  enrollment state, written atomically (temporary file, fsync, rename) before a revision is
  announced. A state file that cannot be read, or whose content does not match its state hash,
  counts as lost: the next projection starts a new epoch.
- Reconcile runs after every approval busd makes and every 5 s, under the mutation lock (waiting
  at most 2 s; a round that finds an approval running skips). There are no registry or catalogue
  watches: the 5 s round already catches agent-busctl's changes and the readership, which lives in
  consumers no watch sees, so a watch would only be a second path to the same result.
- The state hash is SHA-256 of the normalised projection's JSON: records sorted by agent ID,
  readers sorted, empty lists and maps written as `[]` and `{}`. Go's encoding/json is
  deterministic for that shape, so busd and agents of the same release hash the same bytes.
- A busd restored from a backup keeps its old epoch with a lower revision, which agents refuse as
  a rollback. The owner deletes `enroll/directory.json` after a restore so that a new epoch starts.

**Sync (commit 2).**

- busd serves `busd.directory.sync` on its `bus-admin` connection. NATS does not tell the receiver
  who published a request, so busd cannot check "the caller's" inbox. It answers only on a literal
  subject `_INBOX.<agent-token>.<token>...` whose token is a well-formed agent token: the inbox
  space of some agent, which only that agent may subscribe to. Anything else (another account
  subject, a wildcard, an empty token) is dropped, so a request cannot make busd's full-rights BUS
  login publish anywhere else. A reply sent into another agent's inbox is public data bound to a
  request that agent never made, and its client drops it.
- The rate limit is global (a burst of 50, then one every 20 ms), not per connection: busd cannot
  tell connections apart either. Past it busd answers unsigned (`{"error": …}`), which the agent
  treats as a transport error, so a flood costs no signatures.
- A malformed request, and a request before busd's first projection, get a signed `error` reply.
- Diffs come from a cache of the last 16 projections, kept in memory only.

**Heads (commit 3).**

- busd signs a head once per revision and publishes it after every reconcile round, changed or
  not: right after a change (an approval) and then every 5 s. A head after a restart of busd
  carries no hints, since they were about a revision before it.
- Hints are agent IDs with `joined`, `removed` or `rekeyed`, and `channels` with the channel's
  name only (never its readers), at most 20 per head with `truncated` set beyond.
- Agents may only subscribe to `directory.head`; publishing it needs busd's `bus-admin` login.

**Agents (commit 4).**

- The local snapshot is `<config-dir>/directory/snapshot` (records, catalogue, checkpoint and the
  remembered revocations, below), committed under an flock on `directory/snapshot.lock` with the
  base rechecked under it; a commit whose base moved starts the sync over (three attempts). The
  per-agent `.json` files and `channels` are exported from it after each commit and stay the
  read path: lookups are unchanged, and records copied in by hand (`directory export`) keep
  working. An export is never deleted.
- The snapshot also keeps every key fingerprint any directory ever named revoked (`revoked`), and
  the exports mark those keys revoked. That is how "keep every revocation" holds across identities
  of a machine and across a new epoch: an identity that never ran when a key was revoked still
  refuses it, though its own `revoked_keys` file never heard of it, and a restored busd that
  offers the key unrevoked cannot bring it back. The projection itself stays busd's, so its hash
  still checks.
- Revocations go into the trust store (`revoked_keys`) after verification and before the commit,
  under the adapter's lock and with no network call.
- No reconnect hook: busd's head comes every 5 s, so after a reconnect the next head says whether
  anything changed; an agent that missed nothing does not sync.
- A message from an unknown sender is no longer fetched for under the adapter's lock. It is left
  unacked, asks the directory loop for a sync (at most every 5 s), and the sync hands it back to the
  broker (a nak) as soon as the sender's record is in, so it is delivered at once. `send` and
  `publish` sync before taking the lock, for recipients missing from the directory.
- One-shot commands (`agent-bus send`, `inbox`, `mark-read`) have no loop: they sync on a miss, and
  `inbox` and `mark-read` sync and fetch again when a message waits for an unknown sender.
- The approval reply carries no directory any more (it is unsigned, and anyone with the shared
  login could overhear it and race a forged one in); `enroll` writes neither a directory nor the
  enrollment password, and the agent syncs at its first start.
- A head with the same epoch and revision but another state hash is logged, and the sync that
  follows, asking from a version busd does not have, gets the full directory. A full reply of the
  same epoch at the agent's revision with another hash is accepted (it is signed and fresh).

**Groups (commit 5).**

- After every sync the adapter recomputes the channels it reads from the synced catalogue (plus
  `--groups`), under its lock; the reader picks them up at its next poll. A join needs nothing
  else on the agent's side: the read cursor exists (busd or agent-busctl made it, which is how the
  catalogue lists the reader), and the grants for every catalogued group came with `channel
  create`. A group left is dropped from the set.

**Notices (commit 6).**

- The notice is computed by the adapter from the directory it knew and the one it took in, so it
  is the net change and comes once per version; heads' hints play no part. Joined, removed and
  rekeyed agents, and channels from the agent's point of view ("you now read channel team",
  "channel x is new"). A new epoch is said too. At most 10 wait; the oldest go first.
- `inbox` and `wait_for_messages` carry them as `directory_notices`, apart from `messages`; each is
  shown once. The hooks add them to a mail notice (Stop, the idle waiter, the plugin) and to the
  session start's summary, under "Agent bus directory (from your adapter, nothing to answer)".
- A notice alone does not end `wait_for_messages`, block a Stop or wake an idle session: one
  enrollment would otherwise cost every idle agent a turn. It waits for the next result or notice
  (a wait times out within at most five minutes). A notice taken for the plugin's notice is gone
  even if the plugin fails to deliver it; it is informational, and the directory itself is right.
- The MCP instructions do not mention them, to keep the briefing within its budget; the field's
  name and the text ("Directory updated: ...") say what they are.

**Monitor (commit 7).**

- The monitor subscribes to `directory.head` (a new subscribe grant of `monitor-bus`, nothing to
  publish) and, on a CA-signed head naming a new version, runs its discovery round at once; the
  5 s round stays. It does not use `busd.directory.sync`: it already reads the registry and the
  catalogue from busd over `monitor-control` (`busd.monitor.*`), which has what it shows (removed
  agents included), so a second path would add grants and nothing else.
- `GET /api/v1/status` has `directory`: the version busd last announced, when, and the error of
  the last registry or catalogue read, apart from presence; the page shows "directory not
  readable" while that error is set.

**After review.**

- A diff that does not apply (wrong base or a state hash that does not check) is not asked for
  again from the same base: the agent asks once for the whole directory (`since` empty) and
  takes that in. Asked from the same base, the same diff would fail every round.
- A replaced file (busd's state, an agent's snapshot and its exports) is synced, renamed, and
  then its directory is synced too, so the rename is on disk before busd announces the revision
  or an agent acts on its snapshot. A failure to sync is the write's failure.
