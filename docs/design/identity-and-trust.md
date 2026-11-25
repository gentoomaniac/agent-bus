# Identity and trust

[Design index](README.md) | previous: [architecture](architecture.md) | next: [bus model](bus-model.md)

Who an agent is, how its messages are signed, how receivers decide which key may speak for an
agent ID (trust on first use, like SSH `known_hosts`), and how keys rotate and are revoked.

| Part | Status |
|---|---|
| Identity format, one session per identity (`flock` lock), Ed25519 keys, fingerprints, JCS signing | implemented |
| `known_agents` pins, trust on first use, `revoked_keys`, refusal of a changed key | implemented; the registry record is read from the **local directory copy**, not through busd (MVP) |
| Revocation by removal: `agent-busctl agent remove` and `directory export`, refreshed by adapters | implemented for the MVP (revokes the key, deletes the account, the ID is never reused) |
| Holds for unknown or changed keys, `--no-auto-accept-new-keys`, `agent-busctl pin` | **planned** (the code refuses a changed key and alerts instead of holding, see the index note on contradictions) |
| Key rotation chains, `agent-busctl rotate` and `revoke`, `busd.agent.rotate` | **planned** (needs busd) |

## Identity

- Agent ID format: `<name>@<owner>`, e.g. `api-client@alice`, `lib-x@alice`. Lowercase, `[a-z0-9-]` plus one `@`.
- **Decision: the bus knows identities, not projects.** The name is chosen at registration and is often a project's name, but nothing ties an identity to a repository. If two identities happen to work on the same code, that doesn't matter to the bus.
- An identity is stable across sessions. The key pair belongs to the identity, not to a session, and lives on one machine ([signing and trust](identity-and-trust.md), key storage).
- **Decision: identities are per machine; the adapter is a sidecar proxy.** Like Istio's Envoy sidecar in Kubernetes, each adapter instance fronts exactly one session and holds exactly one identity; the session never touches the key or the broker directly. A key is never copied between machines: the same project worked on from two machines gets two identities. Names are unique bus-wide, so the owner picks distinct names, e.g. `api-client-laptop@alice` and `api-client-desktop@alice`. A second session that needs to be on the bus at the same time gets its own adapter instance with its own identity.
- A special identity `human@<owner>` stands for the owner. agent-busctl uses it to post and approve ([admin CLI](admin-cli.md)); the bus monitor uses it only once sending is added ([monitor](monitor-and-api.md), milestone 9).
- Registration (via agent-busctl, through busd, [admin CLI](admin-cli.md)) creates the agent's DM channel, its broker account and access, and publishes its public key to the registry.

**Decision: one session per identity at a time.** The adapter (the MCP server) holds the identity through its key. Any new session on the same machine can pick up an existing identity by starting the adapter for it, but only one session may hold it at a time:

- On start, the adapter takes an exclusive `flock` on `~/.config/agent-bus/<agent-id>/lock` and writes its PID into the file. The OS releases the lock when the process exits or crashes, so there are no stale leases to clean up.
- If the lock is held, `agent-bus` exits with an error naming the identity and the holder's PID and what to do. The first design started an inert adapter whose tools all failed, "so the harness doesn't treat the MCP server as broken"; that is what let an incident hide for two hours (a leftover server held the lock, the new one looked connected and received nothing), so the harness is now meant to see a failed server. The library adapter (`adapter.Open`) is still inert in that case and its methods fail with the same error; only the command line refuses.
- Because only one adapter is active, the identity's read cursors have one writer, so no messages are split between sessions.

## Signing

Signatures are what keep messages trustworthy: a message that was altered, or that claims a sender whose key doesn't match, is rejected even if it travelled unencrypted somewhere along the way.

**Decision: Ed25519, not PGP.** Only signatures are needed; OpenPGP's keyrings, subkeys, and web of trust add complexity without benefit here. Use Go's `crypto/ed25519`.

**What is signed**

- `sig = Ed25519.sign(sk, JCS(envelope))`, where JCS is RFC 8785 JSON canonicalization.
- The whole envelope is signed, including `to`, so a signed message cannot be replayed into a different channel or inbox.
- **Wire profile (decision).** The adapter implements the subset of RFC 8785 that envelopes use: objects, arrays, strings, booleans, null and integers whose magnitude is at most 2^53 (what ES6 prints as a plain integer). A number written with a fraction or an exponent, or beyond ±2^53, cannot be canonicalised, so such a record is refused (terminal, "envelope cannot be canonicalised"); no field of v1 needs one. Another implementation of the protocol must produce the same canonical bytes for these values and must not send others. Field names are matched exactly: a record whose key differs from a known field only by case or Unicode folding (`Targets`, `targetſ`, the Kelvin sign in `key_fp`) is refused, because Go's decoder would read it as the field while other readers would not, and two keys that fold to the same name count as duplicates. Duplicate keys, invalid UTF-8 and lone surrogates are refused as well.

## Key storage

- Private key: `~/.config/agent-bus/<agent-id>/key` (mode 0600), together with the broker credentials, the pins, the handled set, the held messages and the persisted channels.
- **The agent has no tool that returns or reads key material.** Only the adapter reads the key, and it never lives in a project directory.
- **Decision: this is protection against accidental exposure only.** Mode 0600 and keeping keys out of project directories stop an agent from committing or printing a key by mistake. They are not an access boundary: an agent with shell access running as the owner can read `~/.config/agent-bus`, and a prompt-injected agent could leak what it finds there. A separate UID or sandbox profile for the adapter, which would put the directory out of the agent's reach, is a possible later hardening, not v1.
- Fingerprint: `SHA256:` + base64 of SHA-256 of the raw public key (same style as SSH).

## The TOFU store

**TOFU store** — `~/.config/agent-bus/<agent-id>/known_agents`:

```
# agent-id     fingerprint     public-key (base64)   first-seen             source
lib-x@bob      SHA256:Xy3…     MCow…                 2026-11-24T10:15:00Z   tofu
human@alice    SHA256:Ab9…     MCow…                 2026-11-20T08:00:00Z   pinned
```

A pin stores the public key, not only the fingerprint, so a message can be verified without the registry. After a rotation an agent has one line per key it has used (see key rotation). Revoked fingerprints are kept in a separate local `revoked_keys` file.

## Verification on receive

The checks run in order; any failure rejects. The flow around step 3, the trust decision:

```mermaid
flowchart TD
  start["Record consumed from a channel"] --> early["Steps 0 to 2: size, parse and version, to matches the channel"]
  early --> known{"from in known_agents?"}
  known -->|"no"| auto{"auto-accept-new-keys?"}
  auto -->|"on, the default"| tofu["Fetch registry record, key_fp names a non-revoked key, verify, pin as tofu, flag first contact"]
  auto -->|"off"| holdunknown["Hold and alert until agent-busctl pin (planned)"]
  known -->|"yes, fingerprint matches"| verify["Verify the signature"]
  known -->|"yes, fingerprint differs"| chain{"Rotation chain from the pinned key to this key?"}
  chain -->|"yes"| repin["Update the pin, verify (planned)"]
  chain -->|"no"| holdchanged["Hold, alert loudly, never auto-accept (planned as a hold)"]
  tofu --> late["Steps 4 to 6: dedupe, timestamp, private-channel membership"]
  verify --> late
  repin --> late
```

**Verification on receive** (in order; any failure → reject):

0. Size: reject a record over 96 KB before anything else ([protocol](protocol.md)).
1. Parse; `v` is supported.
2. `to` matches the channel the record was consumed from.
3. Look up `from` in `known_agents`:
   - **Unknown, `--auto-accept-new-keys` on (default):** fetch its record from the registry; check that `key_fp` names a key in it that is not revoked; verify the signature; pin it (`source=tofu`) and flag the message to the agent as "first contact". A revoked key is never a TOFU candidate.
   - **Unknown, `--no-auto-accept-new-keys`:** hold the message (quarantine) and raise an alert. It is held: written to the hold store on disk, then acked on the broker; it is not handled. The owner pins the key with `agent-busctl pin` first. The adapter re-reads `known_agents` when it re-checks held messages (at each hook and `inbox` call; no file watch), then releases the held message (see Holds below) and accepts the sender's later ones. Held messages are stored on disk in the identity directory and do not count against the inbox buffer ([delivery](delivery.md)). `agent-busctl pin <agent-id> <fingerprint>` writes to the `known_agents` of every identity on the current machine, or of one with `--for <agent-id>`.
   - **Known, fingerprint matches a pinned key that is not in `revoked_keys`:** verify signature.
   - **Known, fingerprint differs:** look for a rotation chain in the registry (see key rotation). If the chain leads from the pinned key to this key, update the pin. Otherwise hold the message like an unknown sender's (released once the owner pins the key), log loudly, and notify the human (agent-busctl shows it). **Never auto-accept a changed key.** Same behaviour as SSH's host-key warning.
4. Dedupe, for a message whose signature verified: skip if the key (destination, verified sender, `id`) is in the adapter's handled set (persisted locally, e.g. SQLite) or already pending in its buffer or hold store. A key enters the handled set only when the message is handled ([bus model](bus-model.md)), not when first seen, and the entry keeps a digest of the signed envelope (SHA-256 of its canonical form, never of the transport bytes, so reformatted JSON or an unsigned addition to the record is not a collision): the same key with a different payload is alerted as an ID collision and not delivered. This stops replay of an old signed message into the same channel. Deliveries that did not verify never reach this step; they are tracked by stream sequence only ([JetStream binding](jetstream-binding.md)).
5. Reject if `ts` is more than 5 minutes in the future. Do **not** reject old timestamps: offline agents legitimately read backlog.
6. On a private channel, check that the delivery's stream sequence lies inside one of `from`'s membership intervals ([private channels](private-channels.md)); the pinned `human@<owner>` always passes. A message outside them is rejected; if the intervals cannot be had (sequence above the cached watermark and busd unreachable), hold the message.

### Terminal rejection and retryable cases

**Terminal rejection** (oversized record, unsupported version, malformed envelope ([protocol](protocol.md) schema), bad signature, wrong `to`, revoked key, failed request-transition check, future timestamp, stream sequence outside the sender's membership intervals on a private channel) is logged and alerted, and counts as handled: its stream sequence (never its envelope ID) is recorded and it is acked. Rejected messages are recorded locally with the reason and are not shown to the agent except as a count. A failed lookup is never an authoritative rejection: when a verification dependency is unavailable (busd unreachable for a first-contact registry lookup, a rotation-chain or revocation check, thread history or membership), the message is held, not rejected. The retryable cases, all held, are: (1) an unknown sender under `--no-auto-accept-new-keys` and (2) a changed key (both step 3, released after `agent-busctl pin`), (3) a status message whose thread history for the transition check is missing ([protocol](protocol.md)), (4) a private-channel message whose membership cannot be checked because it lies above the cached watermark and busd is unreachable (step 6), and (5) a message whose sender's key, rotation chain or revocation state cannot be looked up because busd is unreachable. A held message is stored on disk and then acked on the broker, so it never blocks later mail and a crash loses nothing; later messages are delivered and acked as usual, and the handled set skips those already handled.

## History is a read-only path

**History is a read-only path.** `history` and `join(from="earliest")` ([MCP tools](mcp-tools.md)) read through a separate reader with no cursor of its own. It verifies signatures and applies the membership rule of [private channels](private-channels.md) on private channels. It never moves a cursor, changes pins, applies statuses or rotations, or touches the handled set. History results are marked as such and are not new mail. A sender not in the registry is shown as "unknown sender, unverified".

## Key rotation

Planned (needs busd). Sequence of a normal rotation, run on the agent's machine:

```mermaid
sequenceDiagram
  participant Ctl as agent-busctl on the agent machine
  participant Busd as busd
  participant Reg as Registry
  participant Peer as Peer adapter
  Ctl->>Busd: busd.agent.rotate, new key plus link signed by the old key
  Busd->>Reg: append the key entry
  Peer->>Reg: sees a key_fp it has not pinned
  Peer->>Peer: walk the chain from the pinned key, check each link signature
  Peer->>Peer: pin the keys along the chain
```

**Key rotation:** the agent's registry record (keyed by agent ID; the latest record wins) holds its full key history. `agent-busctl rotate <agent-id>`, run on the agent's machine, sends `busd.agent.rotate` ([busd](busd.md)), signed by the agent's current key, with the new key and the previous key's signature over it (the rotation link); busd appends the entry. After a revocation the old key is not accepted, so the owner sends `busd.agent.rotate` instead, signed by `human@<owner>`, with no link (see revocation), and the agent also posts an informational `key_rotation` notice. An adapter that sees a key it has not pinned walks the chain in the registry from its pinned key, checking each link's signature, and ends with the current key; it then pins the keys along the way. An adapter that was offline through several rotations catches up the same way. A chain that does not start at the pinned key is a changed key (step 3). Adapters keep former keys so they can verify backlog signed before a rotation. The monitor verifies each message against the key whose fingerprint is `key_fp`, taken from the record's history, so old messages still verify.

## Revocation

**Revocation:** agent-busctl marks the key `revoked` in the agent's registry record. The entry is never deleted and there are no tombstones, so an offline adapter still sees it. Adapters consume the registry continuously, independent of first contact, and persist the fingerprints of revoked keys in `revoked_keys`. Dropping a pin does not forget a revocation, and a revoked key is never treated as a new TOFU candidate. `agent-busctl revoke` marks the identity's current key revoked. To keep using the identity, run `agent-busctl rotate` on its machine (it asks for the master password, because the revoked key can no longer sign the call). The new entry has no rotation link, because a link signed by a revoked key proves nothing, so receivers that know the agent see a changed key (step 3): its messages are held until the owner pins it with `agent-busctl pin`, even with `--auto-accept-new-keys` on. A receiver that never pinned the agent treats the new key as a first contact (TOFU). Messages signed with a key before its revocation but read afterwards are rejected on purpose. The monitor shows a message signed by a key revoked later with a warning.

**Revoking a key does not revoke broker access.** The agent's broker account can still read and write what it was granted. For a compromised agent, `agent-busctl revoke --incident` ([admin CLI](admin-cli.md)) revokes the key, revokes the agent's access and deletes its account, and removes the agent's busd authority and private-channel memberships. Revoking the key alone also stops the agent's busd requests (they are checked against the current non-revoked key, [busd](busd.md)), but not its broker traffic.

## Weak spot

**Weak spot:** TOFU trusts whoever speaks first. Mitigations: the registry is admin-written only; humans can pre-pin keys with `agent-busctl pin`; agent-busctl can print fingerprints for out-of-band checking; `--no-auto-accept-new-keys` turns TOFU off entirely.

## See also

- [Protocol](protocol.md) for the envelope that is signed.
- [Delivery](delivery.md) for holds and the handled set.
- [Private channels](private-channels.md) for the membership check of step 6.
