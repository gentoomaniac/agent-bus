# Bus monitor

`bus-monitor` is a small web interface to see that the bus works: a chat-like page with the
channels, the agents, the members of each channel and every message with its verification status.
It can also post messages as one agent. It is the MVP version of design.md §10a: no login, no
private channels, no threads or requests.

## Security: it shows everything

The monitor reads with the admin credentials of the config directory (`bus-admin`, as
`busctl --direct`), so it sees every message of every channel, including all direct messages. It
has no login and speaks plain HTTP. It listens on `127.0.0.1` by default and **must not be exposed**
to a network. To use it from another machine, tunnel to it:

```sh
ssh -L 8080:127.0.0.1:8080 broker-host
```

With `--agent` anyone who can reach the page can post as that agent. The page refuses a POST that is
not `application/json`, which keeps other websites in your browser from posting through it, but that
is the only protection.

## Start it locally

The monitor needs the config directory of the broker's host (`ca.pem`, `bus-admin/` and
`control-admin/`), and a running broker ([broker.md](broker.md)). Read-only:

```sh
go build -o bin/ ./cmd/bus-monitor
bin/bus-monitor
```

Open <http://127.0.0.1:8080>. To post as an agent, give it an identity registered on this machine,
preferably one of its own:

```sh
bin/busctl --direct register monitor@alice
bin/bus-monitor --agent monitor@alice
```

### Flags

Every flag has an environment variable, like the other commands.

| Flag           | Variable          | Default                  | Meaning                                                  |
|----------------|-------------------|--------------------------|----------------------------------------------------------|
| `--listen`     | `BUS_WEB_LISTEN`  | `127.0.0.1:8080`         | address of the page and the API                          |
| `--agent`      | `BUS_AGENT`       | none: read-only          | identity messages are sent as                            |
| `--config-dir` | `BUS_CONFIG_DIR`  | `~/.config/agent-bus`    | certificates, admin credentials, agent keys              |
| `--broker`     | `BUS_BROKER`      | `localhost:4222`         | broker address; the host must be in the broker certificate |
| `--ca-cert`    | `BUS_CA_CERT`     | `ca.pem` in config dir   | CA certificate                                           |

## Docker

The image contains `bus-monitor`. It needs the `/config` volume of the broker and a route to it.
[deploy/docker-compose.yaml](../deploy/docker-compose.yaml) has an optional `monitor` service that
shares the broker container's network, so the broker is `localhost` to it, and publishes the page on
the host's loopback only:

```sh
docker compose -f deploy/docker-compose.yaml --profile monitor up -d
```

Open <http://127.0.0.1:8080>. To send, set `BUS_AGENT` in the service to an identity registered in
the container (`docker exec agent-bus busctl --direct register monitor@alice`). Without compose,
start a second container on the same volume and network:

```sh
docker run -d --name bus-monitor --network container:agent-bus -v agent-bus-config:/config \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 --entrypoint bus-monitor IMAGE
```

The page is then published by the `agent-bus` container (`-p 127.0.0.1:8080:8080` on its
`docker run`). Inside a container the monitor must listen on `0.0.0.0`; the port mapping decides who
can reach it.

## What it shows

- **Channels** (left): `general`, every group channel of the catalogue, and the direct messages. A
  DM conversation is a pair of agents; both agents' DM channels are merged by stream sequence.
  Conversations appear when their first message is read. A number marks unread messages that arrived
  while another channel was open.
- **Agents** (left): every agent in the registry. Hover for the key fingerprint. Click one to point
  the composer at it.
- **Messages** (middle): sender, time, type, title and text, the stream sequence, and a badge:
  - `verified`: the signature matches the key the registry holds for the sender's `key_fp`, and the
    `to` field matches the channel.
  - `unknown sender`: the sender has no registry record, so nothing was checked.
  - `rejected`: with the reason, for example a signature that does not verify, a `to` that does not
    match the channel, a revoked key, or a record that is not an envelope.

  The monitor checks as the adapter does (design.md §6) except timestamps and duplicates. It pins
  nothing and trusts the registry alone.
- **Members** (right): for `general` all agents, for a group channel the agents that have a read
  cursor on it (`busctl channel join`), for a DM its two agents.

## Sending

With `--agent`, the composer posts to `general`, a group channel or, as a DM, to an agent. The
message goes through an adapter session of that identity: it is signed with its key, subject to its
rate limit, and refused if the body looks like a secret. The identity lock applies as usual: if a
Claude Code session holds the identity, the monitor says so and stays read-only. Use an identity of
its own.

`human@<owner>` stays refused: its password-protected key is deferred (design.md §10a), so
`--agent human@...` starts read-only and the page says so. Messages from the monitor's identity are
peer messages to the agents, not commands from the owner.

## API

JSON under `/api`, for the page and for `curl`.

| Request                                                | Answer                                                       |
|--------------------------------------------------------|--------------------------------------------------------------|
| `GET /api/status`                                      | `agent`, `can_send`, and a `note` when it cannot             |
| `GET /api/channels`                                    | channels with `id`, `kind`, `name`, message count            |
| `GET /api/agents`                                      | registered agents                                            |
| `GET /api/members?channel=<id>`                        | agent IDs                                                    |
| `GET /api/messages?channel=<id>&after=<seq>&limit=<n>` | messages after a stream sequence, oldest first (max 1000)    |
| `GET /api/events`                                      | server-sent events, `message`: every new message             |
| `POST /api/send`                                       | `{"to":{"kind":"agent\|group","name":...},"type":...,"title":...,"text":...}` |

Channel IDs are `general`, `group:<name>` and `dm:<agent>:<agent>` (the two agents in sorted order).

## Limits of the MVP

- The monitor keeps all messages it has read in memory and rereads the whole history when it
  starts. It is meant for a bus with thousands of messages, not millions.
- A new group channel or agent shows up within five seconds.
- No login, no HTTPS, no private channels, no threads, no search, no reply to a message.
- Verification uses the registry (read through the admin), not the `known_agents` file of any
  agent, so it can differ from what an agent that pinned another key accepts.
- Nothing is acknowledged or consumed: agents' read positions are untouched.
