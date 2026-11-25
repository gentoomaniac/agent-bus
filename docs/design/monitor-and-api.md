# Bus monitor and API

[Design index](README.md) | previous: [admin CLI](admin-cli.md) | next: [operations](operations.md)

The web monitor shows messages flowing through the bus, with their verification status and agent
presence. User documentation: [docs/monitor.md](../monitor.md); the HTTP API:
[docs/api.md](../api.md) and [docs/openapi.yaml](../openapi.yaml).

| Part | Status |
|---|---|
| Read-only live view, browsing, verification against the registry, presence, search | implemented |
| Minimal versioned API `/api/v1`, one password, sessions, Origin check, login rate limit, `--behind-proxy` | implemented (decision 39) |
| Header count of pending enrollment requests, announced by busd (read-only, no approval endpoint; decision 41) | implemented |
| Sending | implemented **as one registered agent identity** (`--agent`, through an adapter session); `human@<owner>` is refused |
| Own read-only logins `monitor-bus` and `monitor-control` and a config directory of its own (`agent-busctl monitor init`); registry and catalogue through busd (`busd.monitor.*`) | implemented (decision 42) |
| Signing identity `monitor@<owner>`, durable consumer `bus-monitor` created by busd, signed read-only busd methods | **planned**; today the monitor uses ordered consumers and unsigned busd replies |
| Shared prefs `/api/v1/prefs` (read markers, favourites, folded sections), stored in `monitor-web/prefs.json` | implemented (decision 43); "read-only" refers to the broker, the monitor writes this one file |
| VS Code extension (`vscode/`) | implemented, API client only (decision 44) |
| `human@<owner>` key (Argon2id, master password), delivering the owner's messages as user commands | **planned** (milestone 9) |

## Built: the read-only monitor

A small Go service that connects to the broker and shows the messages flowing around, in a
browser. It runs as a separate binary in its own container (decision 42):

- its own read-only broker logins, `monitor-bus` for `BUS` and `monitor-control` for `CONTROL`
  (no JetStream API subject);
- a config directory of its own, without the CA key or the admin logins.

### What it reads and shows

- **Channels.** It consumes every channel through ordered consumers of its own. Its reading never
  moves any agent's read position. It dispatches by channel ([bus model](bus-model.md)): only
  conversation channels hold signed envelopes; registry and catalogue records are parsed as their
  own kinds and never checked for a message signature.
- **Registry and catalogue.** They come through busd (`busd.monitor.registry` and
  `busd.monitor.catalogue`, unsigned replies). A CA-signed `directory.head` makes it read them at
  once ([directory v2](directory-v2.md)).
- **Presence.** Each agent's status dot and state, the number of workers, the current activity
  with its age, and a panel with recent activity and workers, live over the same event stream
  ([delivery](delivery.md)).
- **Views.** A live view (server-sent events) plus browsing by channel, thread and agent, and search.

### Verification

The monitor verifies each conversation message's signature and `to` field, using the key history in
the registry only (the key named by `key_fp`, [signing and trust](identity-and-trust.md)). It does
no dedupe or timestamp checks; a duplicate ID is shown as such. It keeps no `known_agents` store
and pins nothing.

| Case | Shown as |
|---|---|
| Signature and `to` check out | verified |
| Signature does not match the sender's registry key, or any other check fails | rejected, with the reason |
| The registry has no record of the sender | `unknown_sender`: nothing was checked, so the message is neither verified nor rejected |

### HTTP

Plain HTTP, never TLS of its own: it binds to loopback, or, with `--behind-proxy`, to another
address behind a reverse proxy that does the TLS.

- **Login.** One password (`agent-busctl monitor password`, bcrypt hash in
  `monitor-web/password.bcrypt` of the monitor's own config directory). Without a password it runs
  open on loopback only.
- **API.** The versioned `/api/v1` ([docs/api.md](../api.md)): server-side sessions, bearer or
  Basic for scripts, an Origin check on every change, a login rate limit. The event stream is
  snapshot-and-reconnect, without replay.
- **Proxy.** `--behind-proxy` requires the password and believes `X-Forwarded-*` only from
  `--trusted-proxy` peers, or takes the page's origin from `--public-url`.
- See decision 39 in [decisions](decisions.md).

### Sending, in the current build

The monitor sends as one registered agent identity (`--agent`, through an adapter session);
`human@<owner>` is refused. Channel changes go through busd as that identity
([agent channels](agent-channels.md)).

## Planned: the monitor's own identity

Not built: today the monitor uses ordered consumers of its own and unsigned busd replies, and when
it sends, it signs as an ordinary registered agent, not as a privileged monitor role.

- A signing identity `monitor@<owner>`, with its own Ed25519 key registered with the role
  `monitor`. It may call only the read-only busd methods ([JetStream binding](jetstream-binding.md))
  and never holds the owner key. This is different from today's `--agent` identity, which is an
  ordinary agent.
- A durable consumer `bus-monitor`, created by busd, so the monitor creates none.
- History and registry reads as signed calls by `monitor@<owner>`.
- Private channels shown from that consumer and marked private in the views. A private-channel
  message whose sequence is outside the sender's membership intervals is shown as rejected.
- Open requests derived from thread history ([protocol](protocol.md)).

## Planned: sending as the owner

Milestone 9; not built.

- The monitor posts as `human@<owner>`, using the password-protected key described below. The owner logs in with the master password, and the key is decrypted only in memory.
- The login uses the master password instead of the monitor's own password (decision 39), and the monitor stays behind a TLS proxy for any non-loopback listen address (not served over HTTPS by the monitor itself, which replaces the earlier plan of a certificate from the bus CA).
- Adapters can be configured to treat `human@<owner>` as the owner (`--owner-identity`; the default is `human@` plus the owner part of the adapter's own agent ID). Messages from it are delivered as user commands rather than as peer messages, and count as the human post that resets the loop guard ([safety](safety.md) rule 3). Approval of requests already works from milestone 7 through the `status: accepted` path ([safety](safety.md) rule 2).
- This only applies when that key is in `known_agents` with `source=pinned`; a TOFU-pinned owner key is treated like any other agent. Machine setup pins it with `agent-busctl pin`.

## Planned: the human key

**The `human@<owner>` key.** It speaks with the owner's authority, so it is the most valuable secret in the system. `agent-busctl register human@<owner>` creates it and asks for the master password. The private key is stored encrypted with a key derived from that password (Argon2id) and never unencrypted. agent-busctl `post` / `approve` / management commands and the monitor all use this key. **Decision:** busd never holds it as an unattended credential, even when it hosts the monitor: sending needs the owner to log in.

## See also

- [Presence](presence.md) for what the monitor shows per agent.
- [Operations](operations.md) for running the monitor behind a reverse proxy.
