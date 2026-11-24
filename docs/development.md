# Development

## Build and check

```sh
make check    # gofmt, go vet and the unit tests; needs no broker
make itest    # integration tests against a real nats-server
```

`make itest` builds the pinned nats-server into `bin/` (`deploy/nats.sh install`) on first use. The
binaries:

```sh
go build -o bin/ ./cmd/busctl ./cmd/agent-bus
```

## Code layout

`internal/bus` holds the broker-neutral model and the two interfaces (`Transport`, `Admin`).
`internal/broker` implements them on NATS JetStream and is the only package that imports the NATS
client library (`internal/itest` also does, to test against a real server).
`internal/monitor` is the backend of `bus-monitor` (design.md §10a): it follows every channel with
the admin's transport, verifies each message against the registry, keeps them in memory and serves
the JSON API, the event stream and the embedded page (`web/index.html`, plain HTML, CSS and
JavaScript without a build step). It sends through an adapter session.
`internal/identity` also reads the private key back and takes the identity lock (design.md §3): an
exclusive `flock` on `<agent-dir>/lock` holding the owner's PID, released by the OS when the process
ends.
`internal/adapter` is the adapter core. Its handled set (design.md §4) is the append-only file
`<agent-dir>/handled.jsonl`, synced to disk (after the broker confirmed an agent's ack, before acking a
rejection): verified messages by
(destination, sender, ID) with a digest of the signed record, rejected deliveries by stream sequence
only. No SQLite: a file read into memory at start is enough for one identity. Outgoing messages
are rate limited (30 per 10 minutes, in memory) and their bodies scanned for obvious secrets
(private keys, AWS, GitHub, Slack and Anthropic tokens, bearer tokens, `password=`-style
assignments); a match blocks the send. The inbox buffer holds verified, unacknowledged messages, at
most 500 and 8 MB; the reader fetches only as many records as would still fit (counting each as
96 KB) and tells the broker `InProgress` for every buffered message every 20 seconds, inside the
consumers' one-minute `AckWait`.

The adapter API (`adapter.Open`, then `Send`, `Publish`, `Poll` or `Run`, `Inbox`, `Ack`) follows
design.md §4a and §6. Per delivery: a sequence already handled or rejected is acked at once; a
record that fails size, version, `to`, sender, signature or timestamp checks, or whose key changed,
is recorded by sequence, written to `<agent-dir>/alerts.log` and acked; a sender whose registry
record cannot be read is left unacked, so the broker redelivers it after `AckWait`; a verified
message already handled is skipped, one already buffered gets `InProgress`, a new one is buffered.
`Ack` acks the broker first (a confirmed double ack), then writes the handled set, so a failed broker ack leaves the message unread. Sends set `hops` from the parent: the
`reply_to` the agent names, or else the message it read or acked in the last five minutes; the
parent must be known locally, and a parent at 6 hops stops the send. If another process holds the
identity lock, `Open` still returns an adapter, but every method fails with an error naming the
holder's PID. Alerts go to `alerts.log` only; `busctl alerts` does not exist yet.
The adapter also remembers, in memory, which buffered messages the delivery hooks already told the
agent about (`NewMail`, `UnreadSummary`): new mail is reported once, and not again within 10 seconds
of the last report (design.md §8 rule 3; held-back mail is reported by the next call).
`internal/hook` is the socket between the MCP server and the hooks, and the hook output formats.
`internal/mcpserver` is the MCP server of one adapter session (official Go SDK, pinned in `go.mod`).
`internal/trust` is the `known_agents` file of an identity (pinned public keys, design.md §6) and
trust on first use: a sender with no pin is looked up in the registry record, its `key_fp` must name
a key in it that is not revoked, and the key is pinned (`tofu`) after the signature verified. A
changed key is refused. Not done yet: holds, rotation chains and `revoked_keys`. Agents cannot read
the registry on the broker (§4a) and busd does not exist yet, so the MVP registry read path is
`<config-dir>/directory/<agent-token>.json`, one record per agent, which `busctl register` writes.
To use an agent from another machine, copy that directory there.
`internal/envelope` is the signed message (design.md §5, §6): its fields, the canonical JSON that is
signed, signing and the stateless checks (size, version, `to`, timestamp). The canonical form is
RFC 8785 (JCS), implemented in the package for the JSON subset the bus produces (integers up to
2^53, no floats); a message with other numbers cannot be verified.

## Tests

`make itest` builds the pinned nats-server into `bin/` (`deploy/nats.sh
install`), then starts and stops its own server (temporary config directory, random ports, TLS) with
`deploy/nats.sh` and checks, at the raw NATS level and through `busctl --direct`:

- TLS only: no plaintext, no untrusted CA, no wrong password; the two admin users are distinct and
  each sees only its own account.
- Two-account isolation: an agent cannot publish, subscribe or call JetStream for anything in
  `CONTROL`; even `bus-admin` (all subjects) cannot see it. Every JetStream API an agent holds,
  called with a reply subject of `$KV.registry.<key>`, `$KV.catalogue.<key>`, `ledger.<x>` or a
  `busd.*` subject, delivers inside `BUS` only and leaves `CONTROL` untouched. A pulled record sent
  to a reply subject is delivered but not stored again; a JetStream API response sent to one is
  stored anew with a new stream sequence.
- Per-role grants: an agent cannot pull from, inspect or ack another agent's consumer, read its DM
  channel, direct-get a message, subscribe to another agent's inbox prefix or to `busd.>`, publish
  to `private.*`, `$KV.*`, `ledger.*` or an owner-only busd method, or create, update or delete a
  consumer, its own included; what it is granted works.
- Consumers: `register` creates `<token>__dm` and `<token>__general` with the fixed configuration
  (`MaxDeliver` -1, `MaxAckPending` 500 holding back the 501st unacknowledged message, `AckWait` 1
  minute); a group cursor starts after the boundary it is given; the stream sequence is in the
  delivery metadata; history moves no cursor; `LastSequence` reads the stream's last sequence.
- `Nats-Msg-Id` dedupe keyed by destination subject and envelope ID; `LEDGER` refuses a stale
  `Nats-Expected-Last-Subject-Sequence`.
- Reloads: a grant added or revoked takes effect on a live connection, a removed user is
  disconnected, an invalid candidate is refused before it replaces `auth.conf`, and each reload is
  confirmed by the monitoring endpoint's config load time.
- The adapter on a real broker (`adapter_test.go`, one fresh bus per case): two agents exchange a DM
  and a `general` post and both pin the other; an agent joined to a group with `channel join` reads
  another agent's post there; a message altered after signing is rejected, alerted
  and acked on the broker; a repeated publish ID is stored once and a replayed signed record is
  delivered once (the copy that arrives next to the buffered original gets `InProgress`, then is
  acked after redelivery); ack B before A and restart: A comes back, B does not; a second adapter on
  one identity fails every operation until the first session ends; two auto-repliers stop at 6
  hops, also alternating `send` and `publish`; the secret scan blocks a send.
- `agent-bus mcp` as a real stdio process (`mcp_test.go`): alice's `send` reaches bob's
  `wait_for_messages` framed as untrusted and bob acks it; a second server on one identity fails
  every tool and names the holder; SIGTERM ends a server and frees the identity.
- The hooks (`hook_test.go`): `agent-bus pending --format=hook` as a child of the same process as
  an MCP server is silent without mail, blocks once for a message from another session without
  its text, then is silent; `session-start` summarises it; mail for a second session (another
  parent PID) is not reported to the first; the sockets are gone when both servers end.
- The registry and catalogue hold what `register` and `channel create` wrote; `busctl` needs
  `--direct`, refuses a second registration and gives up on a dead broker.

## Cutting a release

`.github/workflows/release.yaml` runs on GitHub Actions (github.com/gentoomaniac/agent-messenger).
On every push to `main` and every pull request it runs `make check` and a goreleaser snapshot build that
publishes nothing. A tag `v*` publishes the archives, `checksums.txt` and the changelog as a
GitHub release and pushes the image to `ghcr.io/gentoomaniac/agent-messenger` ([docker.md](docker.md)):

```sh
git tag v0.1.0
git push origin v0.1.0
```

The job's `GITHUB_TOKEN` creates the release and pushes the image; the workflow asks for the
`contents: write` and `packages: write` permissions it needs. No secret has to be set. The optional
repository variable `IMAGE_NAME` overrides the image name (default
`ghcr.io/<owner>/agent-messenger`).

A new ghcr.io package is private. After the first release, make it public in the package settings
on GitHub, or `docker pull` without a login fails.

To try the build locally:

```sh
goreleaser release --snapshot --clean --skip=publish
```

The agents' binary (`agent-bus`) is in the archives; the image is for the central components
only.
