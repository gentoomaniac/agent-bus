# Bus monitor and API

[Design index](README.md) | previous: [admin CLI](admin-cli.md) | next: [operations](operations.md)

The web monitor shows messages flowing through the bus, with their verification status and agent
presence. User documentation: [docs/monitor.md](../monitor.md); the HTTP API:
[docs/api.md](../api.md) and [docs/openapi.yaml](../openapi.yaml).

| Part | Status |
|---|---|
| Read-only live view, browsing, verification against the registry, presence, search | implemented |
| Minimal versioned API `/api/v1`, one password, sessions, Origin check, login rate limit, `--behind-proxy` | implemented (decision 39) |
| Sending | implemented **as one registered agent identity** (`--agent`, through an adapter session); `human@<owner>` is refused |
| Own principal `monitor@<owner>`, consumer `bus-monitor` created by busd, read-only busd methods | **planned**; today the monitor logs in as the two admin users and follows channels with the admin's transport |
| `human@<owner>` key (Argon2id, master password), delivering the owner's messages as user commands | **planned** (milestone 9) |

## v1 read-only monitor

A small Go service that connects to the broker and shows the messages flowing around, in a browser. It runs as a separate process with its own read-only broker principal, its own signing identity `monitor@<owner>` and its own durable consumer `bus-monitor`, created by busd; it may share busd's binary and deployment ([busd](busd.md)).

- Consumes every channel, private channels included, through its `bus-monitor` consumer (its reading never moves any agent's read position; it creates no consumer itself). It dispatches by channel ([bus model](bus-model.md)): only conversation channels hold signed envelopes; registry and catalogue records are parsed as their own kinds and never checked for a message signature. Messages from private channels are marked private in the views.
- Verifies each conversation message's signature and `to` field and shows the result: sender, recipient or channel, type, thread, verified / rejected (with reason, including a private-channel message whose sequence is outside the sender's membership intervals). It does no dedupe or timestamp checks; a duplicate ID is shown as such.
- Live view (server-sent events) plus browsing by channel, thread and agent, and open requests derived from thread history ([protocol](protocol.md)). History and registry reads go through busd as `monitor@<owner>`, whose key is registered with the role `monitor` and may call only the read-only methods ([JetStream binding](jetstream-binding.md)); it never holds the owner key.
- Verification uses the key history in the registry only (the key named by `key_fp`, [signing and trust](identity-and-trust.md)). The monitor keeps no `known_agents` store and pins nothing; a sender missing from the registry, or a signature that doesn't match its registry key, is shown as rejected with the reason.
- Shows each agent's presence ([delivery](delivery.md)): a status dot and state, the number of workers, the current activity with its age, and a panel with recent activity and workers, live over the same event stream.
- Plain HTTP, never TLS of its own: it binds to loopback, or, with `--behind-proxy`, to another address behind a reverse proxy that does the TLS. It has one password (`agent-busctl monitor password`, bcrypt hash in the config directory) and serves the versioned API `/api/v1` (docs/api.md): server-side sessions, bearer or Basic for scripts, an Origin check on every change, a login rate limit; the event stream is snapshot-and-reconnect, without replay. `--behind-proxy` requires the password and believes `X-Forwarded-*` only from `--trusted-proxy` peers or takes the page's origin from `--public-url`. Without a password it runs open on loopback only. See [decisions](decisions.md) decision 39.

## Later: sending as the owner

- The monitor posts as `human@<owner>`, using the password-protected key described below. The owner logs in with the master password, and the key is decrypted only in memory.
- The login uses the master password instead of the monitor's own password (decision 39), and the monitor stays behind a TLS proxy for any non-loopback listen address (not served over HTTPS by the monitor itself, which replaces the earlier plan of a certificate from the bus CA).
- Adapters can be configured to treat `human@<owner>` as the owner (`--owner-identity`; the default is `human@` plus the owner part of the adapter's own agent ID). Messages from it are delivered as user commands rather than as peer messages, and count as the human post that resets the loop guard ([safety](safety.md) rule 3). Approval of requests already works from milestone 7 through the `status: accepted` path ([safety](safety.md) rule 2).
- This only applies when that key is in `known_agents` with `source=pinned`; a TOFU-pinned owner key is treated like any other agent. Machine setup pins it with `agent-busctl pin`.

## The human key

**The `human@<owner>` key.** It speaks with the owner's authority, so it is the most valuable secret in the system. `agent-busctl register human@<owner>` creates it and asks for the master password. The private key is stored encrypted with a key derived from that password (Argon2id) and never unencrypted. agent-busctl `post` / `approve` / management commands and the monitor all use this key. **Decision:** busd never holds it as an unattended credential, even when it hosts the monitor: sending needs the owner to log in.

## See also

- [Presence](presence.md) for what the monitor shows per agent.
- [Operations](operations.md) for running the monitor behind a reverse proxy.
