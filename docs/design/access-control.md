# Access control

[Design index](README.md) | previous: [safety](safety.md) | next: [private channels](private-channels.md)

Who may connect to the broker and what each account may do: accounts and TLS, the grants matrix per
role, and how agents authenticate (passwords, client certificates in phase A, a Puppet-style
enrollment flow in phase B).

| Part | Status |
|---|---|
| TLS-only broker, private CA (`agent-busctl tls init`), static users with bcrypt passwords, per-role grants, `admin init`, generated `auth.conf` | implemented |
| Roles `agent`, `admin` | implemented; `monitor` and `enrollment` roles are reserved with no access (the monitor logs in as the two admin users) |
| mTLS phase A: certificate login on a second listener (`cert issue`, `cert sign`, `cert switch`) | implemented |
| mTLS phase B: enrollment flow, `ca list\|sign\|reject\|fingerprint\|review`, autosigning, CA key in a minimal busd (decision 41) | implemented (see below) |
| The busd and monitor columns of the grants matrix | **planned**: the minimal busd logs in as the two admin users and serves only enrollment and approval; there is no `monitor_<owner>` user yet |

## Accounts and access

- Every client has its own broker account. An agent's account may: read and acknowledge under its own name, read its own DM channel, write any DM channel, read and write `general` and every group channel, read the registry and the public catalogue (through busd), and publish idempotently, so its own retries are not written twice within the broker's window. It gets access to a private channel only when busd grants it ([private channels](private-channels.md)).
- Only the admin principal writes the registry and the public catalogue and creates channels. Agents hold no admin rights and cannot choose broker resource names. They cannot create or delete their own read cursors either: busd provisions them (on registration, `join` and invitation) and removes them when access ends ([JetStream binding](jetstream-binding.md), [busd](busd.md)).
- The monitor's account may pull from its own durable consumer `bus-monitor` over every channel; busd creates that consumer, the monitor creates none and never touches an agent's consumer ([JetStream binding](jetstream-binding.md), [monitor](monitor-and-api.md)).
- **Decision: encrypted connection to the broker.** Every client (`agent-bus`, `agent-busctl`, `busd`, `bus-monitor`) connects over TLS; the broker has no plaintext listener. The broker is assumed to be on the local network. If a machine is elsewhere, the owner sets up a secure tunnel or VPN to reach it; the bus does not provide one. agent-busctl creates a private CA and the broker's certificate during setup (`agent-busctl tls init`). Clients trust that CA only, given by a `--ca-cert` flag (default `~/.config/agent-bus/ca.pem`). No public CA is needed on a LAN.

Note that broker access and message signatures do different jobs: access rules control who can reach what on the broker; signatures prove authorship end-to-end and survive replication, export, and a compromised broker.

## Encrypted connection

**TLS.** The server's `tls { cert_file, key_file }` block is the only listener (no plaintext port). The certificate comes from the private CA (`agent-busctl tls init`, [bus model](bus-model.md)); clients trust that CA as their root (`nats.Secure` with a `tls.Config{RootCAs}`). TLS protects the connection to the server; it does not authenticate busd ([busd](busd.md)).

The two client listeners:

```mermaid
flowchart LR
  pw["Agents and admin tools using a password"] -->|"4222, TLS"| l1["Password listener"]
  cert["Agents and admin tools using a client certificate"] -->|"4223, wss://, TLS with verify_and_map"| l2["WebSocket listener"]
  l1 --> server["nats-server users of account BUS or CONTROL"]
  l2 --> server
  l2 -.-> san["One URI SAN, agentbus:role/name, is matched to the user name"]
```

## Grants matrix

**Accounts and permissions.** Every client is a static user with a password (stored bcrypt-hashed) and its own permissions. Credentials sit with the adapter's key, never in a project directory. NATS authentication identifies the account; the Ed25519 signature ([signing and trust](identity-and-trust.md), [busd](busd.md)) is the identity. A publisher can name any reply subject, so busd's responses go where the caller says; that is harmless because they are signed and inbox tokens are random. A request can also arrive by redirection; busd authorises from its signed content, not from the subject ([busd](busd.md)).

| Role (account) | Operation | Subjects |
|---|---|---|
| Agent `<t>` (`BUS`) | send and publish | `dm.*`, `general`, `topic.*`, and `private.<id>` for each private channel it belongs to |
| | receive, per own consumer `<c>` | `$JS.API.CONSUMER.MSG.NEXT.BUS.<c>`, `$JS.API.CONSUMER.INFO.BUS.<c>` (to notice a lost consumer), `$JS.ACK.BUS.<c>.>`; the consumers are `<t>__dm`, `<t>__general`, `<t>__g__<name>` for every catalogued group and `<t>__p__<id>` for each private channel it belongs to |
| | replies | subscribe `_INBOX.<t>.>` (the client uses `nats.CustomInboxPrefix("_INBOX.<t>")`) |
| | presence | publish `presence.<t>` (own record only); direct get `$JS.API.DIRECT.GET.PRESENCE.presence.*` (stream `PRESENCE` only) |
| | call busd | publish `busd.nonce`, `busd.channel.create`, `busd.channel.list`, `busd.channel.members`, `busd.channel.membership`, `busd.channel.invite`, `busd.channel.remove`, `busd.channel.leave`, `busd.channel.join`, `busd.history`, `busd.registry.get`, `busd.registry.changes`, `busd.catalogue.list`, `busd.agent.rotate` |
| | never | consumer create, delete or update; any `$JS.API.DIRECT.GET` except the one for stream `PRESENCE`, subject `presence.*` (granted as `$JS.API.DIRECT.GET.PRESENCE.presence.*`, so `list_agents` can read others' presence; never a direct get on stream `BUS`); any wildcard consumer, `MSG.NEXT` or ack grant; any `$KV.*` or `ledger.*` subject; the owner-only busd methods |
| Owner `human_<owner>` (`BUS`, used by agent-busctl) | post, and the agent's busd calls | as an agent for DMs, `general` and groups; `private.<id>` where busd granted it ([private channels](private-channels.md)); no consumers |
| | owner-only busd methods | publish `busd.nonce`, `busd.agent.register`, `busd.agent.revoke`, `busd.agent.rotate`, `busd.channel.manager`, `busd.channel.create_group` |
| Monitor `monitor_<owner>` (`BUS`; also used by `agent-busctl` read commands) | read, from the consumer busd made | `$JS.API.CONSUMER.MSG.NEXT.BUS.bus-monitor`, `$JS.API.CONSUMER.INFO.BUS.bus-monitor`, `$JS.ACK.BUS.bus-monitor.>`, subscribe `_INBOX.monitor.>` |
| | read-only busd methods | publish `busd.nonce`, `busd.history`, `busd.registry.get`, `busd.registry.changes`, `busd.catalogue.list`, `busd.channel.list`, `busd.channel.membership` (`busd.channel.membership` for all channels, with signed watermarks; `busd.channel.list` returns the private catalogue of all channels to this identity) |
| | never | any consumer create, update or delete; any publish on a message subject, any stream, KV or `CONTROL` subject; any other busd method |
| busd, message connection (`BUS`) | consumers (agents' and the monitor's), history, stream info | `$JS.API.>` |
| | its API | subscribe `busd.>`, subscribe `_INBOX.busd.>`, publish `_INBOX.>` (replies) |
| | acks | none for ordered consumers; a durable consumer of its own needs `$JS.ACK.BUS.<name>.>` |
| busd, control connection (`CONTROL`) | registry, catalogue, ledger | `$JS.API.>`, `$KV.>`, `ledger.>`, subscribe `_INBOX.busd.>`, and `$JS.ACK.<stream>.<name>.>` for a durable consumer of its own |
| `bus-admin` (`BUS`) and `control-admin` (`CONTROL`), used by `agent-busctl --direct` over two connections | everything in its own account | publish and subscribe `>`; NATS usernames are server-wide, so the two are distinct users with separate passwords, which live only in the owner's config ([admin CLI](admin-cli.md)) |

The monitor has its own narrow signing identity, `monitor@<owner>`, with its own Ed25519 key registered with the role `monitor`: busd lets it call only the read-only methods above. It never uses the owner's key. busd, not the monitor, creates the monitor's consumer, so the monitor needs no create grant. Read-only still has a limit: any raw JetStream reader, the monitor included, can make the server deliver a payload it can read to any subject of `BUS` it can name. That cannot be closed by grants within one account. The receiver checks ([private channels](private-channels.md), [signing and trust](identity-and-trust.md)) are what make it harmless. The grants for the busd methods and the consumers are generated; the per-channel part is rewritten by reload (below).

## mTLS login and enrollment

### Phase A (implemented): certificate login without enrollment

**Phase A (built): certificate login without enrollment.** The owner issues the certificates at
the broker's host (`agent-busctl --direct cert issue|sign|switch`, docs/agent-busctl.md) and
copies them to the agent's machine; no request or approval flow. What differs from the plan below:

- *The second listener is nats-server's WebSocket listener.* nats-server has one client port and
  allows `verify_and_map` once per listener, which then demands a client certificate from every
  client of it. So the password listener (`BUS_NATS_PORT`, 4222) is unchanged, and certificate users
  log in on a `websocket` listener with its own TLS block and `verify_and_map` (`BUS_MAPPED_PORT`,
  4223; clients use a `wss://` address). Users of certificate mode carry
  `allowed_connection_types: ["WEBSOCKET"]`.
- *Mapping.* The certificate has exactly one URI SAN, `agentbus:<role>/<name>`: `agentbus:agent/<agent
  token>`, `agentbus:admin/bus-admin`, `agentbus:admin/control-admin`. nats-server matches
  email, DNS and URI SANs against user names before the subject DN, and the NATS user of a
  certificate login is named exactly that URI; the permissions are those of the role (agent grants
  use the token). Verified in nats-server v2.15.3 (`checkClientTLSCertSubject`, `canTLSMap`).
- *Certificates* are built by `tlsinit.CA.IssueClientCert` from the approved identity only: client
  auth, not a CA, one year by default, no names from a CSR (`SignCSR` checks the request's signature
  and uses only its public key).
- *Roles and modes.* The authorization files model agent, admin, monitor and enrollment users, each
  in password or certificate mode, and are rewritten without losing any entry (the role is a trailing
  comment; older files are still read). Monitor and enrollment are reserved with no access. The web
  monitor has no user of its own yet: it logs in as the two admin users, so `cert issue --admin`
  covers it.
- *Rollback and removal.* `cert switch <id> --password` goes back; `agent remove` drops the
  mapping with the account and the tombstone keeps the ID from being certified again.
- *Reload topology.* Only the broker's container reloads nats-server; the monitor container never
  signals (docs/broker.md).

### Phase B (implemented): the enrollment flow

**Phase B (built, decision 41): the enrollment flow.** A new machine asks for a client certificate
and the owner approves it; the owner's tools are `agent-busctl ca review` (a terminal screen,
`podman exec -it agent-bus agent-busctl ca review`) and `agent-busctl ca list|sign|reject|fingerprint`.
User documentation: [docs/podman.md](../podman.md#9-enrolling-a-new-machine). **Decided (owner,
2026-11-25): busd holds the CA key, not the monitor.** A minimal busd ([busd](busd.md)) runs in the
broker's container, holds the CA key, signs certificates, keeps the pending requests and makes every
auth-file change and reload locally. It has no HTTP interface. The monitor stays strictly
read-only: it shows the number of pending requests and has no CA key and no approval endpoint.

```mermaid
sequenceDiagram
  participant Agent as agent-bus enroll on the agent machine
  participant Busd as busd in the broker container
  participant Owner as Owner, agent-busctl ca review
  participant Broker as nats-server
  Agent->>Agent: create TLS key, CSR and proof of possession
  Agent->>Agent: CA: a copied ca.pem, or fetched from the handshake and matched to the owner's fingerprint
  Agent->>Busd: request on busd.enroll.request, as the shared enrollment login
  Busd-->>Agent: pending, with the approval code
  Owner->>Busd: ca sign (or autosign with the secret)
  Busd->>Busd: build the certificate from the approved ID, one URI SAN
  Busd->>Broker: registry record, read cursors, certificate account, confirmed reload
  Agent->>Busd: busd.enroll.status, polling with a backoff (--waitforcert)
  Busd-->>Agent: certificate, CA certificate, directory
  Agent->>Broker: log in with the client certificate on the mapped listener
```

What was built, and where it differs from the plan above it:

- **Where busd's API lives: the `CONTROL` account.** The subjects `busd.enroll.request`,
  `busd.enroll.status`, `busd.ca.list|sign|reject` and the announcement `busd.enroll.pending` are in
  `CONTROL`, which agents cannot reach: not by login, and not by the JetStream redirection that lets
  any `BUS` reader make the server publish a stored payload onto a subject of `BUS` it names. busd
  also drops every message without a reply subject, so a reply or a publication redirected onto
  one of its subjects is not taken for a request. The owner's tools log in as `control-admin`.
- **The enrollment login** is the shared user `enroll`, role `enrollment`, in `control-auth.conf`,
  on the password listener (no listener of its own). Grants: publish `busd.enroll.request` and
  `busd.enroll.status`, subscribe `_INBOX.enroll.>`; nothing else, so no stream, JetStream API or
  `BUS` access. Its password is made by `agent-busctl enroll init` (`enroll/password`, shown once).
  Each client uses a random inbox prefix under `_INBOX.enroll.`, but the login is shared, so
  **residual risk:** anyone who has it can read the replies meant for other enrollees. Replies
  therefore hold only public data (certificate, CA certificate, directory); the client believes only an
  answer that names its own request ID and installs only a certificate for exactly its agent and
  TLS key, signed by the CA it trusts. The login proves no identity: it is a ticket to ask.
- **Request.** `agent-bus enroll --broker <host> --agent <id> [--waitforcert[=<max>]] [--secret <s>]
  [--ca-fingerprint <sha256>]`. An existing identity keeps its signing key (and so its pins, ID and
  consumers); only the TLS key and CSR are new. The request has the agent ID, CSR, public signing
  key, hostname and a fresh nonce, with an Ed25519 signature over all of them (proof of
  possession, domain-separated). The **approval code** is the first 40 bits of the SHA-256 of that
  whole request (`XXXX-XXXX`), the request ID is the full digest. The machine prints the code and
  the signing key fingerprint; the owner sees them with the TLS key fingerprint.
- **Trust in the broker.** `enroll` needs the CA certificate: a copied `ca.pem`, or `--ca-fingerprint`
  from `agent-busctl ca fingerprint`. With the fingerprint, the CA is read from the broker's TLS
  handshake (`tls init` now has the broker send the CA after its own certificate) *before* any
  password is sent, and kept only if its SHA-256 matches and it signs the broker's certificate. A CA
  learned from the enrollment endpoint alone is never trusted. A broker certificate made by an older
  `tls init` lacks the CA: copy `ca.pem` or append it once (optional migration, docs/podman.md).
- **Pending requests** (busd, in memory and `enroll/state.json`, which holds public data only):
  at most 8 KiB each, 100 pending, 3 per agent ID, 10 requests a minute (burst 10), expiry after 24
  hours (the agent then hears "expired"), a decided request is kept 24 hours for the agent to
  collect, a request of the same agent ID and signing key supersedes the earlier one. Over a limit
  the answer is `busy` and the agent retries.
- **Approval** is single-use: only a pending request can be approved. busd builds the certificate with
  `SignCSR` from the approved agent ID and the CSR's public key alone, the CSR's own names and
  usages are ignored (phase A code). A removed (tombstoned) ID is refused at request and at approval.
  An ID that is registered *with the same signing key* only switches to certificate login. An ID
  registered *with another key* is flagged `replaces` and needs an explicit confirmation (`ca sign
  --replace`, a yes in the screen); then its current key is revoked and the new one added. Steps: the
  registry record, the read cursors of DM and `general`, the account in certificate mode (one confirmed
  reload), `<config>/directory` updated; each repeatable, so a failed approval stays pending.
- **Autosign** (off by default): `agent-busctl enroll autosign` writes `enroll/autosign`, which busd reads on
  every request; a request with the matching secret (constant-time compare) is approved at once, never
  for an ID that would be replaced, and logged. An empty or missing file means off.
- **Monitor.** busd announces the pending count every five seconds on `busd.enroll.pending`; the
  monitor shows it in its header and hides it when it stops coming.
- **Certificates** are as in phase A: client use only, not a CA, one URI SAN, one year (`BUS_CERT_VALIDITY`
  of busd). Renewal is still `cert issue`, or enrolling again from the machine.
- **Removal** is as before (`agent remove`: mapping dropped with the account, ID tombstoned, never
  reused). The directory still reaches other agents by hand (`directory export`); an enrolled machine
  starts with the directory busd returns.

Not built from the earlier plan: busd's signed `busd.<method>` API for agents (only the enrollment
and approval subjects exist), reload through a helper called by the monitor (busd reloads locally, the
monitor never signals), a certificate renewal command, and a separate enrollment listener (the shared
login uses the password listener). Admin certificates are still `cert issue --admin`; busd must be
restarted after it (`pkill busd`, the entrypoint starts it again) to read them.

## See also

- [JetStream binding](jetstream-binding.md): what the subjects and consumers mean.
- [Operations](operations.md): reload, ports and the two-container topology.
- [docs/agent-busctl.md](../agent-busctl.md#cert-issue): the `cert` commands.
