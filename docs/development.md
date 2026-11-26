# Development

## Prerequisites

| Tool | Version | Needed for |
|---|---|---|
| Go | the version in `go.mod` (`go version` must be at least that) | everything |
| Node.js and npm | 22, as in the release workflow | `vscode/` |
| Podman or Docker | any recent | `make image` |
| `curl` and `envsubst` (package `gettext`) | any | `deploy/nats.sh` ([below](#running-the-broker-without-docker)) |

## Build

```sh
go build -o bin/ ./cmd/agent-busctl ./cmd/agent-bus ./cmd/bus-monitor ./cmd/busd
make image    # the central image, agent-messenger:dev
make vsix     # the VS Code extension, vscode/agent-messenger.vsix
```

**Checkpoint:** `bin/agent-busctl --version` prints a version, `podman images agent-messenger:dev`
(or `docker images`) lists the image, and `vscode/agent-messenger.vsix` exists.

## Test levels

| Command                       | Runs                                                              | Needs                  |
|-------------------------------|-------------------------------------------------------------------|------------------------|
| `make check`                  | gofmt check, `go vet` and the unit tests                          | Go only, no broker     |
| `make itest`                  | integration tests against a real `nats-server`                    | Go; builds the pinned nats-server into `bin/` on first use (`deploy/nats.sh install`) |
| `cd vscode && npm ci && npm run check` | the extension's typecheck, unit tests (`node --test`) and bundle | Node.js and npm  |

`make check` is the gate for every commit. Run `make itest` when you touch the broker, the adapter,
the hooks or the monitor, and the vscode check when you touch `vscode/`. Each ends without error
messages; `make itest` and `make check` end with `ok` lines per package.

## Code layout

| Path                | What it is                                                                          |
|---------------------|-------------------------------------------------------------------------------------|
| `cmd/agent-bus`     | the adapter: MCP server, hooks, `enroll`                                            |
| `cmd/agent-busctl`  | the admin CLI ([agent-busctl.md](agent-busctl.md))                                  |
| `cmd/busd`          | the control service beside `nats-server`: CA key, enrollment, approvals ([design/busd.md](design/busd.md)) |
| `cmd/bus-monitor`   | the web monitor ([monitor.md](monitor.md))                                          |
| `internal/bus`      | the broker-neutral model and the two interfaces, `Transport` and `Admin`            |
| `internal/broker`   | those interfaces on NATS JetStream; the only package that imports the NATS client (`internal/itest` also does, to test against a real server) |
| `internal/envelope` | the signed message: fields, canonical JSON (RFC 8785), signing, stateless checks ([design/protocol.md](design/protocol.md)) |
| `internal/identity` | an agent's keys and the identity lock                                               |
| `internal/adapter`  | the adapter core: verify, buffer, ack, send                                         |
| `internal/trust`    | the `known_agents` file and trust on first use                                      |
| `internal/directory`| the registry and catalogue snapshot agents read ([design/directory-v2.md](design/directory-v2.md)) |
| `internal/mcpserver`| the MCP server of one adapter session (official Go SDK)                             |
| `internal/hook`     | the socket between the MCP server and the hooks, hook output formats, `agent-bus event` |
| `internal/presence` | the presence record, tracker and publisher ([design/delivery.md](design/delivery.md)) |
| `internal/monitor`  | the monitor backend: JSON API, event stream and the embedded page `web/index.html` (plain HTML, CSS and JavaScript, no build step) |
| `internal/channelapi` | the group channel requests and answers shared by busd and its clients             |
| `internal/busd`, `internal/enroll`, `internal/enrollclient`, `internal/review` | busd, the enrollment protocol, `agent-bus enroll` and `agent-busctl ca review` |
| `internal/creds`, `internal/tlsinit`, `internal/files`, `internal/testcert` | stored passwords and client certificates, the CA and broker certificate, files with the permissions secrets need, test certificates |
| `internal/itest`    | the integration tests behind `make itest`                                           |
| `deploy/`           | the Dockerfile, compose files, proxy samples, `nats.sh` and the OpenCode plugin (`deploy/opencode/agent-bus.js`, no automated test: check it by hand, [opencode.md](opencode.md)) |
| `vscode/`           | the VS Code extension ([vscode/README.md](../vscode/README.md))                     |

### What to read for each part

| Part | Short version | Details |
|---|---|---|
| Adapter | verifies each delivery, buffers verified mail, acks on request, rate limits and secret-scans what it sends, keeps a handled set in `<agent-dir>/handled.jsonl` and rejected mail in `alerts.log` | [design/bus-model.md](design/bus-model.md), [design/safety.md](design/safety.md), [design/jetstream-binding.md](design/jetstream-binding.md) |
| Identity | an exclusive `flock` on `<agent-dir>/lock` holds the identity, released by the OS when the process ends | [design/identity-and-trust.md](design/identity-and-trust.md) |
| Trust | `known_agents` pins keys on first use from the registry record; a changed key is refused | [design/identity-and-trust.md](design/identity-and-trust.md) |
| Directory | agents sync the registry and the channel catalogue from busd; `<config-dir>/directory/` is the older copy path | [design/directory-v2.md](design/directory-v2.md) |
| Monitor | read-only logins, verification against the registry, an index in memory with bodies read from the stream, sends through an adapter session | [design/monitor-and-api.md](design/monitor-and-api.md) |
| Presence | hooks feed events, a tracker builds the record, a publisher throttles and heartbeats it | [design/delivery.md](design/delivery.md) |
| Envelope | the signed message and its canonical JSON (RFC 8785 subset: integers up to 2^53, no floats) | [design/protocol.md](design/protocol.md) |

### What the integration tests cover

`make itest` starts its own `nats-server` (temporary config directory, random ports, TLS) and
checks, at the raw NATS level and through `agent-busctl --direct`, these areas. The files are in
`internal/itest/`:

| Area | Where |
|---|---|
| TLS, two-account isolation, per-role grants, consumers, dedupe, config reloads | `itest_test.go` |
| the adapter on a real broker: DMs, groups, rejection, replay, ordering, identity lock, hop limit, secret scan | `adapter_test.go` |
| `agent-bus mcp` as a stdio process and the hooks | `mcp_test.go`, `hook_test.go` |
| certificate login, enrollment through busd, wake-up of an idle session | `mtls_test.go`, `enroll_test.go`, `wake_test.go` |
| outages and reconnects, agent removal, mail targets | `outage_test.go`, `remove_test.go`, `targets_test.go` |
| presence, and the monitor with its API and the proxy | `presence_test.go`, `presence_e2e_test.go`, `monitor_test.go`, `monitor_proxy_test.go` |

## Running the broker without Docker

`deploy/nats.sh` runs the pinned `nats-server` as a plain background process, for development and
the integration tests. It is not how the bus is run for real ([docker.md](docker.md)). Use a
disposable directory and ports of their own, so it cannot touch a real setup in
`~/.config/agent-bus` or clash with a broker on the default ports:

```sh
export BUS_CONFIG_DIR="$HOME/bus-dev"          # disposable: rm -rf it when done
export BUS_NATS_PORT=14222 BUS_MAPPED_PORT=14223 BUS_MONITOR_PORT=18222
export BUS_LISTEN_IP=127.0.0.1                 # loopback only
export BUS_BROKER="localhost:$BUS_NATS_PORT"   # read by agent-busctl
export BUS_MONITOR_URL="http://127.0.0.1:$BUS_MONITOR_PORT"

go build -o bin/ ./cmd/agent-busctl ./cmd/agent-bus ./cmd/busd
bin/agent-busctl tls init
bin/agent-busctl admin init
deploy/nats.sh install    # builds nats-server v2.15.3 with go install into <config-dir>/bin/
deploy/nats.sh up         # renders nats/nats-server.conf, tests it, starts the server, waits until ready
bin/agent-busctl --direct init
```

**Checkpoint:** `curl -fs "$BUS_MONITOR_URL/healthz"` succeeds and `bin/agent-busctl --direct agent
list` prints an empty list without an error.

```sh
deploy/nats.sh down       # keeps the data; `down --purge` deletes it too
rm -rf "$BUS_CONFIG_DIR"  # when you are done
```

This does not start busd, so the `ca` commands and enrollment do not work here; run `bin/busd`
yourself for those. The pid file and `nats-server.log` are in `<config-dir>/nats/`. Environment
variables of the script:

| Variable           | Default                          | Meaning                                         |
|--------------------|----------------------------------|-------------------------------------------------|
| `BUS_CONFIG_DIR`   | `~/.config/agent-bus`            | config directory                                |
| `BUS_NATS_SERVER`  | `$BUS_CONFIG_DIR/bin/nats-server`| the server binary (must be named `nats-server`) |
| `BUS_NATS_PORT`    | `4222`                           | client port, password login (TLS only)          |
| `BUS_MAPPED_PORT`  | `4223`                           | client port, certificate login (TLS, `wss://`)  |
| `BUS_MONITOR_PORT` | `8222`                           | monitoring port, 127.0.0.1 only                 |
| `BUS_LISTEN_IP`    | `0.0.0.0`                        | address of the client port                      |
| `BUS_DATA_DIR`     | `$BUS_CONFIG_DIR/nats/data`      | JetStream storage                               |

`agent-busctl` finds the server binary for its reload test at `<config-dir>/bin/nats-server`
(`--nats-server`).

## Cutting a release

`.github/workflows/release.yaml` runs on GitHub Actions (github.com/gentoomaniac/agent-bus). On
every push to `main` and every pull request it runs `make check` and a goreleaser snapshot build
that publishes nothing. A tag `v*` publishes the release.

1. Check that `main` is green: `make check` and `make itest` pass, and the vscode check passes if
   `vscode/` changed.
2. Declare the version and the remote, then run the preflight checks. `RELEASE_REMOTE` is the
   remote of the GitHub repository (find it with `git remote -v`), not the one you push work to:

   ```sh
   VERSION=v0.2.0                      # vX.Y.Z
   RELEASE_REMOTE=github               # a remote that points at github.com/gentoomaniac/agent-bus
   git remote get-url "$RELEASE_REMOTE" | grep -q 'gentoomaniac/agent-bus' || echo "wrong remote"
   echo "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || echo "not vX.Y.Z"
   test -z "$(git status --porcelain)" || echo "working tree is not clean"
   git rev-parse --short HEAD          # the commit you intend to release
   git rev-parse -q --verify "refs/tags/$VERSION" && echo "tag exists"
   ```

   **Checkpoint:** none of the `echo` lines printed, and the commit is the one you mean.

3. Tag it and push the tag:

   ```sh
   git tag "$VERSION"
   git push "$RELEASE_REMOTE" "$VERSION"
   ```

4. The release workflow then runs on that tag:
   - goreleaser publishes the archives, `checksums.txt` and the changelog as a GitHub release and
     pushes the image to `ghcr.io/gentoomaniac/agent-messenger` ([docker.md](docker.md));
   - a second job, `vsix`, packs the VS Code extension ([vscode/README.md](../vscode/README.md)) with
     the tag's version and attaches `agent-messenger.vsix` to the release. It is not published to a
     marketplace; `make vsix` builds the same file locally.
5. After the first release, make the ghcr.io package public in the package settings on GitHub: a
   new package is private, and `docker pull` without a login fails until then.

The job's `GITHUB_TOKEN` creates the release and pushes the image; the workflow asks for the
`contents: write` and `packages: write` permissions it needs. No secret has to be set. The optional
repository variable `IMAGE_NAME` overrides the image name (default
`ghcr.io/<owner>/agent-messenger`).

To try the build locally:

```sh
goreleaser release --snapshot --clean --skip=publish
make image    # only the image, for this machine's architecture: agent-messenger:dev
```

The agents' binary (`agent-bus`) is in the archives; the image is for the central components
only.
