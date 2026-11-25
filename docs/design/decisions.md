# Decisions log

[Design index](README.md) | previous: [acceptance tests](acceptance-tests.md)

The numbered decisions of the owner, with their dates and numbering as in the original
specification. References like "decision 30" point into this list. Open questions the owner has
not answered yet are in [QUESTIONS.md](../../QUESTIONS.md).

**Editorial note.** The decisions are history: each entry says what was decided then. An entry
that a later decision replaced keeps its text and is marked **Superseded** or **Amended** at the
top, with a link to the decision that replaced it. Only names were updated afterwards, so that the
admin CLI is `agent-busctl` everywhere (decision 40) and the product is the Agent Bus. For what is
built today, read the documents each entry links to, not the entries themselves.

## Index

| No. | Decision | Date | Status |
|---|---|---|---|
| [1](#1-language) | Language | 2026-11-24 | current |
| [2](#2-one-session-per-identity) | One session per identity | 2026-11-24 | current |
| [3](#3-single-user-setup-auto-accept-messages) | Single user, `--auto-accept-messages` | 2026-11-24 | current |
| [4](#4-tls-to-the-broker) | TLS to the broker | 2026-11-24 | current |
| [5](#5-no-end-to-end-encryption) | No end-to-end encryption | 2026-11-24 | current |
| [6](#6-separate-bus-monitor) | Separate bus monitor | 2026-11-24 | amended by 41, 42 |
| [7](#7-identities-not-projects) | Identities, not projects | 2026-11-24 | current |
| [8](#8-identities-per-machine-adapter-as-sidecar) | Identities per machine, adapter as sidecar | 2026-11-24 | current |
| [9](#9-monitor-localhost-only-and-owner-key) | Monitor localhost only, owner key | 2026-11-24 | amended by 39 |
| [10](#10-libraries) | Libraries | 2026-11-24 | amended by 30 |
| [11](#11-identity-lock-and-hook-socket) | Identity lock and hook socket | 2026-11-24 | current |
| [12](#12-private-ca) | Private CA | 2026-11-24 | current |
| [13](#13-what---auto-accept-messages-allows) | What `--auto-accept-messages` allows | 2026-11-24 | current |
| [14](#14-hop-limit-and-rate-limit-defaults) | Hop limit and rate limit defaults | 2026-11-24 | current |
| [15](#15-broker-from-a-pod-manifest) | Broker from a Pod manifest | 2026-11-24 | superseded by 31 |
| [16](#16-group-channels-stay-open) | Group channels stay open | 2026-11-24 | current |
| [17](#17-general-and-idempotent-writes) | `general` and idempotent writes | 2026-11-24 | amended by 30, 34 |
| [18](#18-certificate-reissue) | Certificate reissue | 2026-11-24 | amended by 30 |
| [19](#19-delivery-and-recovery) | Delivery and recovery | 2026-11-24 | amended by 30; holds planned |
| [20](#20-delivery-promise) | Delivery promise | 2026-11-24 | current |
| [21](#21-key-trust) | Key trust | 2026-11-24 | current |
| [22](#22-loop-guard) | Loop guard | 2026-11-24 | current |
| [23](#23-requests) | Requests | 2026-11-24 | current; lifecycle planned |
| [24](#24-record-kinds) | Record kinds | 2026-11-24 | current |
| [25](#25-busd-a-control-service) | busd, a control service | 2026-11-24 | amended by 41, 42 |
| [26](#26-private-channels-by-invitation) | Private channels by invitation | 2026-11-24 | planned |
| [27](#27-agent-authentication-to-busd) | Agent authentication to busd | 2026-11-24 | amended by 30, 32 |
| [28](#28-one-internal-boundary-to-the-broker) | One internal boundary to the broker | 2026-11-24 | amended by 35 |
| [29](#29-milestone-order) | Milestone order | 2026-11-24 | current |
| [30](#30-nats-jetstream-replaces-kafkaredpanda) | NATS JetStream replaces Kafka/Redpanda | 2026-11-24 | current |
| [31](#31-the-broker-is-its-own-process) | The broker is its own process | 2026-11-24 | amended by 41, 42 |
| [32](#32-control-plane-isolation-and-busd-provisioned-consumers) | Control plane isolation, busd-provisioned consumers | 2026-11-24 | partly built |
| [33](#33-private-channel-membership-by-broker-order) | Membership by broker order | 2026-11-24 | planned |
| [34](#34-no-request-projection-in-v1) | No request projection in v1 | 2026-11-24 | current |
| [35](#35-one-small-internal-boundary-to-the-broker) | One small internal boundary | 2026-11-24 | current |
| [36](#36-targets-and-quiet-delivery) | Targets and quiet delivery | 2026-11-24 | current |
| [37](#37-presence-from-hooks) | Presence from hooks | 2026-11-24 | current |
| [38](#38-puppet-style-mtls-enrollment) | Puppet-style mTLS enrollment | 2026-11-24 | amended by 41 |
| [39](#39-the-monitor-api) | The monitor API | 2026-11-24 | current |
| [40](#40-the-admin-cli-is-agent-busctl) | The admin CLI is `agent-busctl` | 2026-11-25 | current |
| [41](#41-a-minimal-busd-holds-the-ca-key) | A minimal busd holds the CA key | 2026-11-25 | current |
| [42](#42-own-logins-for-busd-and-the-monitor) | Own logins for busd and the monitor | 2026-11-25 | current |
| [43](#43-shared-preferences) | Shared preferences | 2026-11-25 | current |
| [44](#44-the-vs-code-extension-is-an-api-client) | The VS Code extension is an API client | 2026-11-25 | current |

"Planned" means the decision is settled but the code is not built; the linked document says what
exists. The entries keep their wording from the date they were made.

## Superseded or amended

| Entry | Replaced or changed by |
|---|---|
| 10 (franz-go as broker client) | 30 (nats.go) |
| 15 (broker from a Pod manifest with Podman) | 31 (plain `nats-server` process), later the Docker image |
| 17 (Kafka topics, `IdempotentWrite`; requests channel) | 30 (`Nats-Msg-Id`), 34 |
| 18 (`tls reissue` also for busd's certificate) | 30 |
| 19 (cumulative Kafka commit) | 30 (per-message acks) |
| 27 (busd TLS certificate) | 30, 32 (signed responses) |
| 28 (not tightly bound to one broker) | 35 |
| 9 (monitor localhost-only plain HTTP) | 39 (reverse proxy allowed with `--behind-proxy`) |
| 6, 25 (the monitor may share busd's binary or deployment) | 41, 42 (separate binary and container) |
| 38 (the monitor holds the CA key; separate enrollment listener) | 41 (busd holds the CA key; no separate listener) |
| 31 (the broker and the monitor as plain binaries, a container later, a Pod manifest) | 41, 42; deployment: one image, compose or Podman scripts ([operations](operations.md)) |

## Settled on 2026-11-24

### 1. Language

Go, with kong for CLI parameters ([architecture](architecture.md)).

### 2. One session per identity

One session per identity at a time; a new session on the same machine can pick it up
([identity](identity-and-trust.md)).

### 3. Single-user setup, auto-accept messages

Single-user setup. `--auto-accept-messages` is on by default; `--no-auto-accept-messages` turns it
off ([safety](safety.md)).

### 4. TLS to the broker

TLS to the broker. The broker is on the local network; tunnels or a VPN serve anything outside it
([bus model](bus-model.md)).

### 5. No end-to-end encryption

No end-to-end encryption; signatures guard integrity. `--auto-accept-new-keys` is on by default
([signing and trust](identity-and-trust.md)).

### 6. Separate bus monitor

*Amended by [41](#41-a-minimal-busd-holds-the-ca-key) and [42](#42-own-logins-for-busd-and-the-monitor): the monitor is a separate binary and container.*

A separate bus monitor, read-only first, sending with a master password later. It may be hosted in
the same binary and deployment as busd, but it keeps its own read-only broker principal
([monitor](monitor-and-api.md), [busd](busd.md)).

### 7. Identities, not projects

The bus deals in identities, not projects; which code an identity works on does not matter to it
([identity](identity-and-trust.md)).

### 8. Identities per machine, adapter as sidecar

- Identities are per machine and keys are never copied.
- Each adapter is a sidecar proxy holding one identity for one session.
- Another concurrent session gets another adapter and identity
  ([identity](identity-and-trust.md)).

### 9. Monitor localhost only and owner key

*Amended by [39](#39-the-monitor-api): a reverse proxy is allowed with `--behind-proxy`.*

- Monitor: localhost-only plain HTTP in v1.
- An Argon2id-encrypted `human@<owner>` key; the owner identity is only honoured when pinned
  ([monitor](monitor-and-api.md)).

### 10. Libraries

*Amended by [30](#30-nats-jetstream-replaces-kafkaredpanda): the broker client is nats.go.*

The official MCP Go SDK and a JCS library ([architecture](architecture.md)). The broker client was
franz-go.

### 11. Identity lock and hook socket

Identity lock with `flock` plus a PID in the lock file; hooks talk to the adapter over a unix
socket ([identity](identity-and-trust.md), [delivery](delivery.md)).

### 12. Private CA

A private CA made by `agent-busctl tls init` ([bus model](bus-model.md)).

### 13. What `--auto-accept-messages` allows

As described in [safety](safety.md) rule 2.

### 14. Hop limit and rate limit defaults

Hop limit 6 and 30 messages per 10 minutes as defaults, configurable by flag
([safety](safety.md)).

### 15. Broker from a Pod manifest

*Superseded by [31](#31-the-broker-is-its-own-process): a plain `nats-server` process.*

The broker runs from a Kubernetes Pod manifest, started with Podman for now; another runtime
later.

### 16. Group channels stay open

- Every agent reads and writes all group channels; joining only changes subscriptions.
- Originally `agents.topic.*` through one prefixed ACL; now per-group consumer grants and the open
  `topic.*` publish grant ([JetStream binding](jetstream-binding.md)).
- Closed channels are private channels, by invitation through busd
  ([bus model](bus-model.md), [private channels](private-channels.md)).

### 17. `general` and idempotent writes

*Amended by [30](#30-nats-jetstream-replaces-kafkaredpanda) and [34](#34-no-request-projection-in-v1).*

- Agents read and write `general` and produce idempotently. Originally Kafka topics and
  `IdempotentWrite`; now `Nats-Msg-Id` dedupe ([JetStream binding](jetstream-binding.md)).
- The requests channel was dropped in v1 (decision 34).

### 18. Certificate reissue

*Amended by [30](#30-nats-jetstream-replaces-kafkaredpanda).*

`agent-busctl tls reissue` renews the broker certificate from the existing CA (originally busd's
too, before decision 30). Part of milestone 7 ([admin CLI](admin-cli.md)). Built as
`agent-busctl tls broker`.

### 19. Delivery and recovery

*Amended by [30](#30-nats-jetstream-replaces-kafkaredpanda): per-message acks replaced the cumulative Kafka commit. The hold store described here is not built (see [delivery](delivery.md)).*

- The adapter acknowledges each handled message itself; nothing advances automatically.
  (Originally a cumulative commit of the contiguous prefix on Kafka.)
- A verified message ID is handled when acked or sent by the adapter itself. A terminally rejected
  delivery is recorded by its stream sequence.
- A message from an unknown sender under `--no-auto-accept-new-keys`, or with a changed key, is
  held on disk (and acked on the broker) and released after pinning.
- Joined channels are persisted, with defined fallbacks when the broker has no stored cursor.
- The broker keeps cursors forever ([bus model](bus-model.md),
  [signing and trust](identity-and-trust.md)).

### 20. Delivery promise

- At-least-once, no outgoing journal.
- Mail reaches active sessions, the next session start and, through a waiter, idle sessions for
  targeted mail only.
- Hooks talk to their own session's adapter.
- The inbox buffer is bounded ([delivery](delivery.md), [MCP tools](mcp-tools.md)).

### 21. Key trust

- Key storage guards against accidental exposure only.
- The registry record holds the key history with rotation links; pins store public keys.
- Revoked keys stay in the registry; adapters persist revoked fingerprints
  ([signing and trust](identity-and-trust.md)).

### 22. Loop guard

The adapter sets `hops` from the parent message; only the pinned owner resets it
([safety](safety.md)).

### 23. Requests

A transition table is checked on every status path; `rejected` is terminal. Open requests are
derived from the signed thread history, with no projection in v1 (decision 34,
[protocol](protocol.md)).

### 24. Record kinds

Conversation envelopes, registry records and catalogue records have their own verification
policies. `history` and `join(from="earliest")` are read-only ([bus model](bus-model.md),
[signing and trust](identity-and-trust.md)).

## Added on 2026-11-24: busd and private channels

### 25. busd, a control service

*Amended by [41](#41-a-minimal-busd-holds-the-ca-key) and [42](#42-own-logins-for-busd-and-the-monitor): the monitor is a separate binary and container.*

**A control service, not a gateway.**

- Agents talk to the broker directly for messages. Signing, verification, TOFU, inbox state and
  hooks stay in the adapter.
- busd holds the admin principal and does private channels, membership, broker access and
  agent-busctl's management commands.
- agent-busctl keeps direct admin access as an explicit recovery path.
- The owner's encrypted signing key is never an unattended busd credential.
- The monitor may share busd's binary but runs as a separate process with its own read-only
  principal; a browser admin UI is later work.
- busd has a service name and broker account of its own and is not a bus agent.
- If busd is down, existing traffic keeps flowing and management fails clearly
  ([architecture](architecture.md), [admin CLI](admin-cli.md), [busd](busd.md)).

### 26. Private channels by invitation

*Planned: not built. See [private channels](private-channels.md).*

- **Creation and roles.** An agent creates one through busd and becomes its manager. The owner is
  recovery admin and names a new manager when none is left. Only manager and owner invite and
  remove; members may leave.
- **Invitation.** The invitee gets a signed `invite` DM. An invitation grants membership at once,
  enforced by per-member broker access in a separate namespace. No join key, no key exchange, no
  end-to-end encryption. Members read the whole retained history.
- **Privacy.** Private means private from other agents, not from the owner, the monitor or the
  broker. Private names, descriptions and requests never go to public channels.
- **Leaving.** Leaving removes access and is reported done only after the broker confirms it; the
  adapter drops pending mail.
- **Ledger.** busd keeps one membership ledger with versioned, idempotent, per-channel serialised
  operations and reconciles broker access to it, but never revokes against a missing or reset
  ledger (`busd reconcile --adopt`).
- **Incidents.** Incident revocation revokes access and deletes the account, revokes the key and
  removes busd authority.

Links: [private channels](private-channels.md), [signing and trust](identity-and-trust.md),
[admin CLI](admin-cli.md).

### 27. Agent authentication to busd

*Amended by [30](#30-nats-jetstream-replaces-kafkaredpanda) and [32](#32-control-plane-isolation-and-busd-provisioned-consumers): busd is reached over the broker connection and signs its responses.*

A per-request Ed25519 signature by the agent's identity key.

- The signature covers a domain-separated payload: busd's service name, caller, method, target,
  body digest and a single-use short-lived nonce.
- It is checked against the current non-revoked registry key. Authorisation is by that identity.
- Idempotency IDs serve retries. There is no new long-lived agent credential.
- Originally busd also had a TLS certificate for its service name. With the NATS binding
  (decision 30), busd is reached over the broker connection and signs every response with its
  pinned service key (decision 32, [busd](busd.md)).
- Nonce requests are signed by the caller and the quota is per verified caller; busd authorises
  from the signed content and checks the subject against the signed method (decision 32).

### 28. One internal boundary to the broker

*Amended by [35](#35-one-small-internal-boundary-to-the-broker).*

One internal boundary to the broker (was: not tightly bound to one broker).

- A broker-neutral model ([bus model](bus-model.md)) with one binding
  ([JetStream binding](jetstream-binding.md)), originally Kafka, replaced by decision 30.
- Two small Go interfaces (message transport, admin), one implementation, confined to
  `internal/broker`.
- Another broker is a possible later option, not a design goal (decision 35;
  [architecture](architecture.md)).

### 29. Milestone order

Milestone 1 is done; busd comes after the adapter core and before the MCP surface, so
private-channel tools are exposed only after authorisation, the ledger and reconciliation exist
([milestones](milestones.md)).

## Added on 2026-11-24: backend switch

### 30. NATS JetStream replaces Kafka/Redpanda

The first binding was Kafka (Redpanda for development, franz-go, SASL/SCRAM, ACLs, consumer-group
offsets with a contiguous-prefix commit, compacted topics). The owner moved to NATS JetStream
(Apache 2.0, CNCF): `nats-server` v2.15.3, `nats.go` v1.56.0 (`jetstream`).

- One stream `BUS` for conversation channels.
- Per-agent durable pull consumers created by busd, with per-message acks and `Nats-Msg-Id`
  dedupe over a 24-hour window.
- KV buckets for registry and catalogue, and a plain `LEDGER` stream for busd's membership ledger,
  all in a second account (`CONTROL`) that only busd and the admin reach.
- Static users with per-user subject permissions, TLS from the private CA, and permission changes
  by rewriting the authorization file and reloading. JWT accounts and `nsc` are not needed for one
  owner.
- The two Go interfaces stay as one internal boundary; JetStream is the only implementation, in
  `internal/broker` ([architecture](architecture.md), [JetStream binding](jetstream-binding.md),
  [milestones](milestones.md) milestone 1a).

### 31. The broker is its own process

*Amended by [41](#41-a-minimal-busd-holds-the-ca-key) and [42](#42-own-logins-for-busd-and-the-monitor): deployment is one image with compose or Podman scripts.*

The broker is its own process, not embedded in busd, so a busd crash does not drop agent
connections. On the dev machine it is a plain binary; a container is optional later. The server
configuration plus the start script are the only runtime-specific files
([architecture](architecture.md), [JetStream binding](jetstream-binding.md)).

### 32. Control plane isolation and busd-provisioned consumers

*Partly built: the `CONTROL` account, and busd's enrollment and registry reads on it, exist. Signed `busd.<method>` responses and operation intents are planned. A restart loop in the container entrypoint is built; further supervision is planned ([busd](busd.md)).*

**Isolation.**

- JetStream writes responses to a reply subject the caller names, so authoritative data
  (registry, catalogue, `LEDGER`) sits in a second NATS account, `CONTROL`, that agents cannot
  reach.
- busd serves registry, catalogue and history over request/reply and signs every response with a
  service key pinned at setup.
- No third account: receivers check broker order instead of sender time (decision 33).

**Consumers and channels.**

- Agents get no consumer create, delete or update grants. busd creates their consumers (on
  register, group `join`, invite) and deletes them (on leave, removal, incident). Group `join`
  and `leave` become busd calls.
- busd also creates the monitor's consumer; the monitor has no consumer grant and signs as
  `monitor@<owner>`, which may call only read-only busd methods and is never the owner key.
- Channels exist in the catalogue, not in the broker; the adapter refuses to publish elsewhere.

**Redirection inside `BUS`.**

- An agent can make the server deliver a payload it stored (a DM to itself) to any `BUS` subject
  it can name, including a private channel it cannot publish to. The integration test showed the
  delivered copy is not stored again.
- Any raw JetStream reader can redirect payloads within `BUS`; the receiver checks make that
  harmless.

**Handled state and holds.**

- Only verified envelopes enter the handled set, keyed by destination, verified sender and ID.
  Rejected or unverified deliveries are tracked by stream sequence.
- Holds are written to disk and then acked on the broker. An agent's ack is confirmed by the
  broker before the handled state is written. A held message keeps its record until handled state
  is committed.

**busd operations.**

- Permission changes are confirmed operations (validate, reload, confirm the generation).
- busd authorises from the verified signed request (caller, method, target) and checks the subject
  against the signed method; nonce requests are signed and limited per verified caller.
- Operations are crash-safe: an operation intent keyed by (caller, idempotency ID), with the
  assigned resource IDs, is persisted before any effect. Recovery finishes it, the result comes
  from confirmation events and the hash covers the semantic request only.
- Key rotation is `busd.agent.rotate`, signed by the old key.
- busd runs supervised, with a table of what works while it is down and no HA.

The grants list full subjects ([JetStream binding](jetstream-binding.md),
[private channels](private-channels.md), [signing and trust](identity-and-trust.md),
[busd](busd.md)).

## Added on 2026-11-24: JetStream review fixes

### 33. Private-channel membership by broker order

*Planned: private channels are not built.*

Membership is decided by broker order, not sender time.

- The broker assigns each stored message a stream sequence the sender cannot set. A message
  redirected onto a private subject is delivered but not stored again (proved by the integration
  test), so the interval check is defence in depth.
- busd records the sequence at every confirmed join and removal. A receiver accepts a
  private-channel message only if its sequence lies in one of the sender's membership intervals;
  `human@<owner>` always passes.
- busd's membership answers carry a signed watermark. Above it the adapter refreshes; with busd
  unreachable it holds.
- History reads and status folding use the same rule.
- The `ts` is no longer used for membership, and there is no backdating caveat
  ([JetStream binding](jetstream-binding.md), [private channels](private-channels.md),
  [signing and trust](identity-and-trust.md)).

### 34. No request projection in v1

The `requests` tool and `agent-busctl requests` derive open requests from thread history through
the read-only path. The requests KV, its grants, `busd.requests.project` and its tests are dropped;
a projection is a possible later optimisation ([protocol](protocol.md)).

### 35. One small internal boundary to the broker

The two Go interfaces keep the client library in one package. Kafka or another broker is a
possible later option, not a design goal ([architecture](architecture.md)).

### 36. Targets and quiet delivery

- The envelope carries signed `targets` (agent IDs or `@all`). Every message has non-empty
  targets: the sending adapter fills DMs, replies and `@all` for requests, and refuses the rest.
- Agents are prompted only for mail that targets them. `--deliver=targeted` (default) keeps mail
  aimed at others out of the agent's inbox entirely; `--deliver=all` delivers it.
- Requests and questions get an immediate `ack` reply and a later `answer`; acks, answers and
  statuses are not answered ([protocol](protocol.md), [delivery](delivery.md)).

## Added on 2026-11-24: presence

### 37. Presence from hooks

Presence for the owner's monitor, from hooks, in stream `PRESENCE`.

- Each adapter publishes its agent's state, activity, workers and unread targeted mail on change
  (at most every 3 seconds) and as a 30-second heartbeat.
- Hooks (Claude Code) and a plugin (OpenCode) feed it through `agent-bus event`.
- Readers call an agent offline after 90 seconds without a heartbeat. An idle agent with targeted
  mail unseen for 10 minutes is `unresponsive`.
- One message per agent in a separate stream of the `BUS` account, written only by the agent
  itself. Agents do not poll it; sending to an agent that cannot respond still works and warns.
- `wait_for_messages` returns only mail not yet returned. A message the agent sent an `ack` for
  shows `acked`, and `mark_read all` leaves unanswered requests and questions
  ([delivery](delivery.md), [MCP tools](mcp-tools.md), [monitor](monitor-and-api.md),
  [docs/monitor.md](../monitor.md)).

## Added on 2026-11-24: access control

### 38. Puppet-style mTLS enrollment

*Amended by [41](#41-a-minimal-busd-holds-the-ca-key): busd, not the monitor, holds the CA key, and there is no separate enrollment listener.*

Puppet-style mTLS enrollment ([mTLS login](access-control.md)).

- Agents enroll with a CSR; the owner approves in the monitor or with `agent-busctl ca sign`.
- The monitor holds the CA key; optional autosigning with an enrollment secret; a separate
  enrollment listener.
- Certificates are built from the approved ID; removed IDs are not reused while their certificates
  are valid.
- Order: agent removal with directory refresh, the protocol fixes, the minimal monitor API, then
  enrollment, then the VS Code extension.

## Added on 2026-11-24: monitor API

### 39. The monitor API

The monitor API is minimal, versioned and authenticated; there is no TLS in the monitor. Every
endpoint is under `/api/v1` with JSON errors `{"error", "code"}`
([API](../api.md), [openapi.yaml](../openapi.yaml)).

- **Users.** One user, one password (`agent-busctl monitor password`). Sending stays the monitor's
  one identity (`--agent`); more users come later.
- **Sessions.** Random server-side sessions (12 h idle, 7 d max; rotated at login, ended by logout
  and by a restart, which a new password needs). The cookie is `HttpOnly`/`SameSite=Strict` and
  `Secure` behind https; `Authorization: Bearer` or Basic also work. The token never goes in a
  URL.
- **Requests.** Every change needs a same-origin `Origin` or an Authorization header. There is no
  CORS. Five free wrong passwords per client, then a doubling delay.
- **Binding.** The monitor always binds to loopback unless `--behind-proxy`. That requires the
  password and trusts forwarded headers only from `--trusted-proxy` (default loopback) or uses
  `--public-url`. `--allowed-host` extends the Host check, which accepts the loopback names and
  the listen host with any port.
- **Public access** only through a reverse proxy doing TLS (samples in `deploy/proxy/`).
- **Events.** Snapshot-and-reconnect, each with an `id` but no durable replay yet.

## Added on 2026-11-25

### 40. The admin CLI is `agent-busctl`

The admin CLI is `agent-busctl`, not `busctl`. `busctl` is systemd's D-Bus tool on every systemd
machine, so a shell without our alias ran the wrong program. The default was taken on 2026-11-25
while the owner was away (QUESTIONS.md); `abctl` or `agent-bus admin …` were the alternatives. The
decisions above use the current name.

### 41. A minimal busd holds the CA key

*Added after an external spec review.*

**A minimal busd holds the CA key; the monitor stays read-only; approvals in a TUI.**

- The monitor is the component exposed through a reverse proxy, so it must not hold the CA key or
  reload the broker.
- A minimal busd in the broker container holds the CA key, signs certificates, keeps pending
  enrollment requests and makes all auth-file changes and reloads. It has no HTTP interface.
- Approvals: `agent-busctl ca review` (a TUI, Bubble Tea, run with `podman exec -it`) and
  `agent-busctl ca list|sign|reject`. The monitor shows only a count of pending requests.
- This replaces the plan in decision 38 that the monitor holds the CA key
  ([access control](access-control.md), [busd](busd.md)).

**Built 2026-11-25.**

- busd's enrollment and approval API lives in the `CONTROL` account, out of the reach of the
  redirection that agents can do in `BUS`. The enrollment login is a shared password user there.
- `agent-bus enroll` needs the CA from a copied `ca.pem` or from the broker's handshake matched to
  the owner's fingerprint.
- Autosign, limits and expiry are described in
  [access control](access-control.md#phase-b-implemented-the-enrollment-flow).

### 42. Own logins for busd and the monitor

busd and the monitor have logins of their own, and the monitor a config directory of its own.

- busd logs in as `busd`, with read grants on the registry and the catalogue. It writes the
  registry and answers only into the caller's inbox space.
- The monitor reads with `monitor-bus` (BUS) and `monitor-control` (CONTROL, no JetStream API
  subject). It takes the registry and the catalogue through busd (`busd.monitor.registry` and
  `busd.monitor.catalogue`).
- The monitor's config directory has no CA key and no admin logins (`agent-busctl monitor init`).
  It also holds the sending agent's key when `--agent` is given.
- `agent-busctl service init` adds the logins to a setup made before they existed. An old config
  directory falls back to the admin logins, with a warning.
- Commits 575f226, 9471fb8, 9bf14d3, 9e2fdfb ([busd](busd.md), [monitor](monitor-and-api.md),
  [access control](access-control.md)).

### 43. Shared preferences

Shared preferences (read markers, favourites, folded sections) are kept by the monitor.

- They live in `monitor-web/prefs.json`.
- The API is `GET /api/v1/prefs`, `POST /api/v1/prefs/read` (merged by maximum) and
  `PUT /api/v1/prefs/favourites` and `/collapsed`, with a `prefs` event.
- There is one password, so there is one user. Theme, layout and filters stay per client.
- "Read-only" refers to the broker, not to this file ([monitor](monitor-and-api.md)).

### 44. The VS Code extension is an API client

The VS Code extension talks only to the monitor's `/api/v1`.

- It has no NATS connection and no keys. It logs in with the monitor password (Bearer token),
  sends as the monitor's agent and shares the preferences of decision 43.
- A new client that talks to the broker needs a decision of its own.
- This is the last step of decision 38's order ([monitor](monitor-and-api.md),
  [architecture](architecture.md)).
