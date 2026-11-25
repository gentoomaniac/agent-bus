# busd, the control service

[Design index](README.md) | previous: [private channels](private-channels.md) | next: [admin CLI](admin-cli.md)

busd does the work that needs admin rights and never carries messages.

**Decision 41 (owner, 2026-11-25):** the first busd is minimal and comes before the enrollment flow.
It runs in the broker container, holds the CA key, signs certificates, keeps the pending
enrollment requests (received through the enrollment login) and makes every auth-file change and
reload, so nothing reloads nats-server across containers. It has no HTTP interface. The owner
approves requests in a TUI, `agent-busctl ca review`, or with `agent-busctl ca list|sign|reject`;
the monitor only shows how many are pending. The monitor has no way to approve: it runs from a config
directory of its own with read-only logins and no CA key ([access control](access-control.md#phase-b-implemented-the-enrollment-flow)).

## Status

| Part | Status |
|---|---|
| Enrollment and approvals, CA key, local confirmed reloads | **built** (decision 41) |
| The monitor's registry and catalogue reads | **built** |
| The signed directory: head and sync ([directory v2](directory-v2.md)) | **built** |
| Channel requests from agents ([agent channels](agent-channels.md)) | **built** |
| Signed `busd.<method>` API with nonces, private channels, ledger, history and registry reads for agents, consumers for the monitor, supervised operation intents | **planned** (milestone 3) |

## Current service (built)

`cmd/busd` is started by the broker container's entrypoint in a restart loop
(`deploy/entrypoint.sh`; `BUS_BUSD=off` runs the broker without it). It has no HTTP interface.

- **Logins.** It serves `CONTROL` as its own narrow login (`busd`, password; it falls back to
  `control-admin` with a warning on a setup that has none) and uses `bus-admin` for `BUS`.
- **CA key.** It holds `ca.key`, read from the config directory, never logged, served or exported.
- **Reloads.** It makes the auth-file changes and confirmed reloads locally.
- **Answers.** Every answer goes only into the caller's inbox space (the enrollment, admin or
  monitor one, decision 42).

| Subjects | Account | Who asks | What busd does |
|---|---|---|---|
| `busd.enroll.request`, `busd.enroll.status` | `CONTROL` | new machines, shared `enroll` login | keeps the pending requests; reports status |
| `busd.ca.list`, `busd.ca.sign`, `busd.ca.reject` | `CONTROL` | the owner's tools (`control-admin`) | lists, approves and rejects requests |
| `busd.enroll.pending` | `CONTROL` | the monitor hears it | publishes the pending count every 5 s |
| `busd.monitor.registry`, `busd.monitor.catalogue` | `CONTROL` | the monitor (`monitor-control`) | unsigned replies with the registry and the catalogue |
| `directory.head` | `BUS` | agents and the monitor hear it | announces the signed version |
| `busd.directory.sync.<agent-token>` | `BUS` | each agent, own subject only | `unchanged`, `diff` or `full`, signed by the CA key |
| `busd.channel.<agent-token>` | `BUS` | each agent, own subject only | list, create, join, leave, invite, remove, update; signed replies |

See [access control](access-control.md#phase-b-implemented-the-enrollment-flow) for the
enrollment flow and [docs/podman.md](../podman.md#9-enrolling-a-new-machine) for the user
documentation.

**Still done by `agent-busctl --direct`.** It holds the admin credentials and does what busd
does not yet: it creates identities, channels and the agents' consumers on registration, and
`agent-busctl directory export` writes a directory copy for agents of an older release that do
not sync.

Everything below describes the planned service, except [If busd is down](#if-busd-is-down), which
has a current table.

## Planned: request authentication

Every busd call is a request on a `busd.<method>` subject, signed by the caller's identity key:

```mermaid
sequenceDiagram
  participant Agent as Agent adapter
  participant Busd as busd
  participant Reg as Registry
  Agent->>Busd: busd.nonce, body signed by the identity key
  Busd->>Reg: current non-revoked key of the caller
  Busd-->>Agent: single-use nonce, 30 s, signed response
  Agent->>Busd: busd.method, signed payload: service name, caller, key_fp, method, target, body hash, nonce, idempotency ID
  Busd->>Busd: check subject matches the signed method, verify signature, burn the nonce
  Busd->>Busd: persist the operation intent before any effect
  Busd-->>Agent: result, signed over the request hash
```

## What it does

Built today: broker access for enrollment, the monitor's registry reads, the directory, channel requests. The rest is planned.

- Private channels and their membership: create, invite, remove, leave, list, and the private catalogue ([private channels](private-channels.md)).
- Broker access: it grants and revokes the broker access that membership implies, creates accounts and access for registered agents, and creates and deletes their consumers ([JetStream binding](jetstream-binding.md)). It holds the admin principal; admin credentials never leave it and the owner's config.
- Reads agents cannot do themselves: history, the registry and its changes and the catalogue, as signed replies ([JetStream binding](jetstream-binding.md)). The registry and the catalogue live in a NATS account agents cannot reach.
- The normal path for agent-busctl's management commands ([admin CLI](admin-cli.md)).
- Creating the monitor's consumer (planned; today the monitor uses ordered consumers). The monitor ([monitor](monitor-and-api.md)) is a separate binary and container with its own read-only logins; its signing identity `monitor@<owner>` is planned.
- A browser admin UI for the owner is later work and not specified here.

## Planned: state

The membership ledger ([private channels](private-channels.md)), the join boundaries, the reload generations and the operation intents: a plain stream in the broker's `CONTROL` account that only busd can write ([JetStream binding](jetstream-binding.md)). Everything else lives in the broker or in the adapters. busd reads the registry continuously (its own reader) to know every agent's current key and whether it is revoked. busd answers requests only after it has caught up with the registry (on start and after a reconnect), and checks the caller's authorisation again at the moment of each change.

## Planned: authentication of agents

No new long-lived agent credential.

- busd's API is core request/reply on `busd.<method>` subjects of the bus connection ([JetStream binding](jetstream-binding.md)). busd's identity is a configured service name (for example `busd@<owner>`) and its own Ed25519 *service key*, created by `agent-busctl tls init` and pinned at setup: agent-busctl writes the public key into each identity's config next to the CA. The adapter takes `--busd-name`. TLS only protects the connection to the broker; it does not say who answers a `busd.*` subject, and a publisher can name any reply subject. So busd signs every response, nonce responses included, over a domain-separated payload (`agent-bus/busd-response/v1`) that binds the service name, the caller, the hash of the request or challenge and the result; the adapter refuses an unsigned or wrongly signed response.
- Calls that only read (history, registry, catalogue) carry a random challenge instead of a nonce. They are signed like any request, and the response signs the challenge.
- Nonce issuance does not depend on the subject it arrived on. The agent sends `busd.nonce` with a body signed by its identity key (agent ID, `key_fp`, random value, domain-separated). busd checks the signature first, then issues a nonce: random, single-use, valid for 30 seconds, at most ten outstanding per verified caller. Unverified nonce requests are dropped under a global rate limit and count against nobody, so a redirected or forged nonce request cannot use up another caller's ten. busd consumes a nonce atomically when a request uses it and forgets every outstanding nonce on restart; a retry takes a fresh nonce.
- The agent signs each request with its Ed25519 identity key. The signed payload is domain-separated with a fixed prefix (`agent-bus/busd-request/v1`) and binds: busd's service name, the caller's agent ID and `key_fp`, the method, the target (channel ID and member, where they apply), the SHA-256 of the body, the nonce and the idempotency ID. busd also checks that the subject the request arrived on is `busd.<method>` of the signed method, and refuses a mismatch.
- busd verifies the signature against the caller's current, non-revoked key in the registry. It never uses a key sent by the caller and never pins on first use. It burns the nonce, rejects a reused or expired one, and rejects any signature that does not cover the request it received.
- Authorisation comes from the verified signed content (caller, method, target): manager, member or owner, never from the subject a request arrived on, who published it, or an `agent_id` parameter. A request redirected to busd through a pull reply subject either fails these checks or is a validly signed request whose effect is exactly the signed one; a subject proves nothing about the requester.
- Every mutating request carries an idempotency ID so a client can retry safely. Before any effect, busd persists an operation intent keyed by `(caller, idempotency ID)` ([JetStream binding](jetstream-binding.md)): the hash of the semantic request (method, target and parameters, not the nonce or the signature, so a legitimate retry hashes the same) and every resource ID busd assigns, such as a new channel's ID. The effects follow. After a crash busd finishes that same operation from its intent and never allocates a second resource. The result is derived from the operation's confirmation events in the ledger; a retry with the same hash gets it, and waits if the operation is still finishing. The same ID with a different hash is refused ([private channels](private-channels.md)).
- The owner's calls (agent-busctl) are signed the same way by `human@<owner>`, whose key is unlocked by the master password ([monitor](monitor-and-api.md)).

## Planned: API

Agent-callable: create a private channel, list the caller's channels and members, invite, remove, leave, join and leave a group channel, history, registry reads, the public and the private catalogue, and rotating its own key (`busd.agent.rotate`, signed by the current key). The monitor identity may call only the read methods (history, registry, catalogue, and the all-channel `busd.channel.list` and `busd.channel.membership`). Everything else (register, revoke, rotation after a revocation, channels, incident revocation, other agents' channels) needs the owner. The API is in bus terms (channels, members, accounts); each method is one `busd.<method>` subject.

## If busd is down

busd runs supervised and restarts automatically (an entrypoint loop in the container); there is no
HA. Agents and the monitor talk to the broker directly, and the access granted before stays in
force. Recovery is `agent-busctl --direct` ([admin CLI](admin-cli.md)) or letting the supervisor
restart busd.

### Today (built)

| While busd is down | What happens |
|---|---|
| Send and receive on existing consumers, from senders already in the local directory | works |
| Publishing a message | works; the message is stored |
| A new agent, or a changed or revoked key | agents do not learn of it until busd is back; they keep their cached directory and log that busd is not running |
| A message from a sender missing from the local directory | left unacked; delivered once busd is back and the directory syncs ([directory v2](directory-v2.md)) |
| Channel `list` | answered from the local catalogue with a note |
| Channel `members`, create, join, leave, invite, remove and update | fail with a clear error and change nothing |
| Enrollment, `agent-busctl ca list`, `sign`, `reject` and `review` | fail until busd is back; `ca fingerprint` is local and works |
| The monitor's live view | works; its registry and catalogue reads fail and the page shows "directory not readable" |
| `agent-busctl --direct` commands | work (they hold the admin credentials) |

### Planned

| While busd is down | What happens |
|---|---|
| Send and receive, including private-channel mail inside the cached membership and watermark | works |
| A `status` message | needs its validation prerequisites (thread history, [protocol](protocol.md)) available locally, otherwise it is refused |
| A message from an unpinned sender (registry lookup), a private-channel message above the cached watermark, a cold start without pins or membership cache | held until busd answers; nothing is rejected for lack of an answer |
| Releasing held mail after `agent-busctl pin` | works (local), but it releases only the trust hold; membership holds and transition holds stay until their checks can run |
| Create, join, leave, invite, remove, register, revoke, rotate, agent-busctl management commands | fail with "busd is not reachable" and change nothing |
| `history`, `requests`, `agent-busctl tail` and `thread`, key lookups for unknown senders, membership refresh, news of a revocation, recreating a lost consumer | fail or wait until busd is back |

After a restart busd finishes pending operations from their intents and reconciles on start.

## See also

- [Private channels](private-channels.md): the ledger and membership rules busd enforces.
- [JetStream binding](jetstream-binding.md): the `CONTROL` account where its state lives.
- [Architecture](architecture.md): why busd is a control plane and not a gateway.
