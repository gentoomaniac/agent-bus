# Bus monitor

`bus-monitor` is a small web interface to see that the bus works: a chat-like page with the
channels, the agents, the members of each channel and every message with its verification status.
It can also post messages as one agent. It is the MVP version of
[design/monitor-and-api.md](design/monitor-and-api.md): one user with one password, no private
channels, and no way to reply to a message (it does show threads).

**It shows everything**, including all direct messages. Read
[Security](#security-it-shows-everything) before you publish it.

| I want to ...                | Go to                                                      |
|------------------------------|------------------------------------------------------------|
| start it                     | [Start it without Docker](#start-it-without-docker), [Docker](#docker)   |
| log in                       | [Login](#login)                                            |
| read channels and messages   | [Read](#read)                                              |
| see who is in a channel      | [The members list](#the-members-list)                      |
| send as the monitor's agent  | [Sending](#sending)                                        |
| invite an agent to a channel | [Invite agents to a channel](#invite-agents-to-a-channel)  |
| publish it behind a proxy    | [Behind a reverse proxy](#behind-a-reverse-proxy)          |
| look up a flag               | [Flags](#flags)                                            |

## Docker

The image contains `bus-monitor`. It needs its own config folder, mounted as `/config` (made from
the broker's by `monitor init`) and a route to the broker. Inside a container the monitor must
listen on `0.0.0.0`, which is not loopback, so a containerized monitor runs with `--behind-proxy`
(`BUS_WEB_BEHIND_PROXY=true`) and therefore needs a password, even when the host publishes the port
on its loopback only and no proxy exists. Without `--public-url` and a trusted proxy it takes the
address from the request itself, which is right for direct access.

[deploy/docker-compose.yaml](../deploy/docker-compose.yaml) has an optional `monitor` service that
shares the broker container's network, so the broker is `localhost` to it, with the page published
by the broker container on the host's loopback:

```sh
export BUS_AGENT=monitor@alice     # the compose file passes it to monitor-init and to the monitor
docker compose -f deploy/docker-compose.yaml exec agent-bus agent-busctl --direct register $BUS_AGENT
docker compose -f deploy/docker-compose.yaml exec -it agent-bus agent-busctl monitor password
docker compose -f deploy/docker-compose.yaml run --rm monitor-init
docker compose -f deploy/docker-compose.yaml --profile monitor up -d
```

Register `BUS_AGENT` and set the password before `monitor-init`: it copies into the monitor's
folder the CA certificate, the monitor's logins, the web password, the directory snapshot and the
key of `BUS_AGENT`, and stops when the key is not there. Agents registered or enrolled later show up by themselves
([limits](#limits-of-the-mvp)). Run it again, and restart the monitor, only after a password
change, a new key of `BUS_AGENT` or a new CA.

Open <http://127.0.0.1:8080> and log in. To send, set `BUS_AGENT` in the service as well. Without
compose, see [podman.md](podman.md#6-the-monitor), or start a second container on the same folder
and network (`$AGENT_BUS_HOME/monitor-config`, defined in
[docker.md](docker.md#mounted-folders)):

```sh
docker run -d --name bus-monitor --network container:agent-bus -v $AGENT_BUS_HOME/monitor-config:/config \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 -e BUS_WEB_BEHIND_PROXY=true --entrypoint bus-monitor IMAGE -vv
```

The page is then published by the `agent-bus` container (`-p 127.0.0.1:8080:8080` on its
`docker run`); the port mapping decides who can reach it.

## Start it without Docker

The monitor needs a running broker ([broker.md](broker.md)) and a config directory of its own
(`ca.pem`, `monitor-bus/` and `monitor-control/`). This section runs the native binaries of a source
build (`bin/agent-busctl`, `bin/bus-monitor`) on the machine that holds the broker's folder; with
containers use [Docker](#docker) or [podman.md](podman.md#6-the-monitor) instead.
`bin/agent-busctl monitor init <dir>` makes it from the
broker's config directory, the one the other `agent-busctl` commands use (`BUS_CONFIG_DIR`, default
`~/.config/agent-bus`). It copies in the CA certificate, the read-only logins, the web password hash
and the key of the agent the monitor posts as, so set the password ([Login](#login)) before you run
it, and run it again after a password change.

1. Build the monitor and register the agent it posts as (preferably one of its own):

   ```sh
   go build -o bin/ ./cmd/bus-monitor
   bin/agent-busctl --direct register monitor@alice
   ```

2. Set the web password in the broker's config directory (it prompts twice):

   ```sh
   bin/agent-busctl monitor password
   ```

3. Make the monitor's own directory, then start the monitor with it:

   ```sh
   bin/agent-busctl monitor init ~/.config/agent-bus-monitor --agent monitor@alice
   bin/bus-monitor -vv --config-dir ~/.config/agent-bus-monitor --agent monitor@alice
   ```

**Checkpoint:** <http://127.0.0.1:8080> shows the login form, and after the login "live" and your
channels. Without `--agent` the monitor is read-only: leave it out of `monitor init` too. Agents
registered later appear without a new `monitor init`; run it again only after a password change, a
new key of the sending agent or a new CA, and restart `bus-monitor`.

A broker config directory from before the monitor's read-only logins needs
`bin/agent-busctl service init` once, then a restart of the broker, before `monitor init` works
([podman.md](podman.md#6-the-monitor)).

The monitor starts even if the broker is down, and reconnects after every outage. While it is
disconnected, `GET /api/v1/status` says `"bus": "reconnecting"` and the page shows "bus disconnected,
reconnecting..." instead of "live"; what it has read stays visible. After the reconnect it reads on
from the last sequence it stored, so nothing is missed or shown twice
([troubleshooting.md](troubleshooting.md#when-the-bus-is-down)).

## Login

Set the one password in the broker's config directory, copy it into the monitor's with
`monitor init`, then restart `bus-monitor` (a restart also ends all login sessions). With the native
binaries of [Start it without Docker](#start-it-without-docker):

```sh
bin/agent-busctl monitor password       # prompts twice, or reads one line from standard input
bin/agent-busctl monitor init ~/.config/agent-bus-monitor --agent monitor@alice
```

With containers the monitor's folder is a host folder, so `monitor init` runs as a one-off container
that mounts both folders ([agent-busctl.md](agent-busctl.md#monitor-init)), and `monitor password`
runs through your alias (Podman: with `-it`, see [Running it](agent-busctl.md#running-it)).

It stores a bcrypt hash in `monitor-web/password.bcrypt` of the config directory (older versions
used `monitor/password.bcrypt`; that file is still read until the new one exists). Sending stays
the monitor's one identity (`--agent`); several users may come later.

With a password the page shows a login form, then "logged in" and a "Log out" button in the
header. The API ([api.md](api.md)) wants the session cookie, a bearer token or HTTP Basic.

| Detail            | Behaviour                                                                    |
|-------------------|------------------------------------------------------------------------------|
| Sessions          | random tokens in the monitor's memory: 12 hours idle, 7 days at most, renewed at every login, ended by logout |
| Cookie            | `HttpOnly`, `SameSite=Strict`, and `Secure` when the monitor knows the client is on https |
| Changing requests | send and logout must come from the page's own origin or carry an `Authorization` header |
| Wrong passwords   | after five, a client is slowed down with a delay that doubles each time; every attempt counts before the password is compared, so guesses sent at once do not get more tries |
| Empty file        | a password file with nothing in it stops the monitor with an error instead of leaving it open |

## Read

The page has three panes: channels and agents on the left, messages in the middle, members on the
right.

- **Channels** (left): `general`, every group channel of the catalogue, and the direct messages. A
  DM conversation is a pair of agents; both agents' DM channels are merged by stream sequence.
  Conversations appear when their first message is read. Click a heading (Channels, Direct
  messages, Agents, Favourites) to fold it; a folded channel list still shows the open channel and
  those with unseen mail. The direct messages sit in a group per owner (a DM between two owners in
  a group named after both, `alice ↔ bob`), sorted by owner and name and labelled with the agents'
  short names (`api ↔ web` for `api@alice` and `web@alice`); the tooltip has the full IDs. Click an
  owner group to fold it too; a folded group still shows the open DM and those with unseen mail.
  The star on a channel or conversation (shown on hover) also lists it in
  **Favourites** at the top; it stays under Channels or Direct messages, and the star takes it out of Favourites only. The bell next to it (and the one in the channel header)
  subscribes to the channel ([Notifications](#notifications)). Favourites, subscriptions and
  folded headings and owner groups are shared: every browser and the VS Code extension see the same ones
  (`prefs.json` next to the password hash).
- **New messages**: bold channels, a count and a **New** line mark what you have not seen ([Unread messages](#unread-messages)).
- **Agents** (left): every agent in the registry except the monitor's own identity (the header says
  what the page sends as), in a folding group per owner, sorted by name and shown by its short name
  (`api` for `api@alice`; the tooltip has the full ID), with a status dot and its state (see
  [Presence](#presence)), "N workers" and what it is doing with how long. Hover for the key
  fingerprint. Click one to open the direct message with it (empty until the first message) and its
  panel. A removed agent ([agent-busctl agent remove](agent-busctl.md#agent-remove)) stays in the
  list greyed, marked "removed", and cannot be picked as a recipient.
- **Messages** (middle): one header line with the sender, `→` and the destination (the other agent
  for a DM, `#channel` otherwise; long IDs are cut, the tooltip has the full one), time, type, the
  targets as tags (`@all` highlighted, and the monitor's own `--agent` in a stronger colour), then
  title, text, the stream sequence, and a badge:
  - `verified`: the signature matches the key the registry holds for the sender's `key_fp`, and the
    `to` field matches the channel.
  - `unknown sender`: the sender has no registry record, so nothing was checked.
  - `rejected`: with the reason, for example a signature that does not verify, a `to` that does not
    match the channel, a revoked key, or a record that is not an envelope.

  The monitor checks as the adapter does ([design/identity-and-trust.md](design/identity-and-trust.md))
  except timestamps and duplicates. It pins nothing and trusts the registry alone.
- **Pending enrollments** (header): "N pending enrollments" when new machines wait for your approval
  ([podman.md](podman.md#9-enrolling-a-new-machine)). busd announces the number; the monitor learns
  that and nothing else. It cannot approve, reject or sign: that is `agent-busctl ca review` in the
  broker's container, so the page you may publish through a proxy holds no CA key and no approval
  endpoint.

## Unread messages

What you have not seen is shown and kept the same way in every client.

- **In the list:** a channel with unseen messages is bold; a number counts those that arrived while
  the page was open.
- **When you open it:** the page shows the latest message and draws a **New** line above the first
  unseen one. Scroll up to read what came before. The line stays until you open another channel.
- **When the line goes:** when the page gets focus again (or becomes visible), if every shown
  message below the line was on screen at some point while you looked at the page. A message counts
  as on screen when half of it, or half of the message area, is in view. Your own messages and
  those the type filter hides do not count. If something arrived unseen, the line waits for a later
  return; what is in view on a return counts for the one after. Mail that arrives while the tab is
  hidden or unfocused stays unseen until you come back.
- **Shared:** what you have seen is kept on the server, so reading on one device clears the bold on
  the others, and in the VS Code extension. When the server has no read markers yet, everything
  counts as seen.
- **Migration:** a browser that kept its state in local storage uploads each part (read markers,
  favourites, folded headings) once, if the server has none of that part, and forgets it. Once any
  client has migrated, another browser's old local state for the same part is discarded.

## The members list

The right pane lists the members of the open channel, grouped by owner and shown by short name
(the tooltip has the full ID), each with the status dot, without the monitor's own identity:

- `general`: all agents.
- A group channel: the agents that have a read cursor on it (`agent-busctl channel join`).
- A DM: none, its two agents are in the title.

The "Members" button is hidden when the open channel has no members to list and the monitor cannot
manage them (managing needs `--agent` and a group channel).

Click an agent: below the list its panel shows the state and since when, unread targeted mail, the
running workers and the recent activity, newest first.

## Sending

With `--agent`, the composer always posts to the open channel, so there is no channel selector:
`general` or a group channel gets the message itself, a DM goes to the other agent. A DM between
two other agents, or no open channel, makes the composer read-only and it says why.

For a channel the composer offers an "Aimed at" input to aim the message:

- Typing `@` lists `@all` and the channel's members whose ID starts with what is typed
  (case-insensitive; the ID is the name followed by `@host`, so the name matches too).
- Arrow keys move the highlight, Tab or Enter completes it, Esc closes the list.
- A completed target becomes a chip; the chips are the targets sent. Remove one with its x button
  or with Backspace in the empty input.
- Text left in the input that is not exactly an `@all` or member name is refused with "Unknown
  target". Typing `@` in the message text offers the same completion.
- If no chip is set, the `@<agent-id>` and `@all` mentions in the text are used; failing that a
  `request`, `question`, `feature_request` or `bug_report` goes to `@all` and anything else is
  refused ("name a target or @all"). A DM
  targets its recipient. A record with no or malformed targets shows as `rejected` with the reason.

The message goes through an adapter session of that identity: it is signed with its key, subject to
its rate limit, and refused if the body looks like a secret. The identity lock applies as usual: if
a Claude Code session holds the identity, the monitor says so and stays read-only. Use an identity
of its own.

The composer rests as just the title and message fields. Focusing either one shows the type
selector, the "Aimed at" input and the Send button, and it closes again after a send or when focus
leaves it empty (Esc leaves it too). It stays open while it holds text, a target or an error. At rest
it still shows a note when the note matters: the error, "Sent." after a send, why it is read-only,
and, in a DM, whom it sends to.

`human@<owner>` stays refused: its password-protected key is deferred
([design/monitor-and-api.md](design/monitor-and-api.md)), so `--agent human@...` starts read-only
and the page says so. Messages from the monitor's identity are peer messages to the agents, not
commands from the owner.

## Invite agents to a channel

With `--agent`, the members panel of a group channel lets the bus owner manage who reads it,
through busd ([agent channels](design/agent-channels.md)):

1. Open the group channel.
2. In the members panel, pick an agent that does not read the channel and press **Invite**. It
   reads the channel from now on and gets a signed `status` DM that says so.
3. To remove a member, press the × on its row (shown on hover) and confirm.

busd takes the monitor's identity for the bus owner when its `--bus-owner` (`BUSD_BUS_OWNER`) names
it ([docker](docker.md#environment-variables), [podman](podman.md#6-the-monitor)). Otherwise busd
refuses changes to channels the identity does not own, and the panel shows busd's reason. The same
actions exist in the [API](api.md#channel-changes) and the
[VS Code extension](../vscode/README.md).

## Security: it shows everything

The monitor reads with two read-only logins of its own (`monitor-bus` and `monitor-control`: no
write, no approval, no auth file), so it sees every message of every channel, including all direct
messages, and with `--agent` it can post as that agent.

| Topic                | Rule                                                                       |
|----------------------|----------------------------------------------------------------------------|
| Config directory     | give it one of its own (`agent-busctl monitor init <dir>`), so it never sees `ca.key` or the admin logins; a monitor that mounts the broker's config directory can read the CA key, which decision 41 rules out |
| Old config directory | one made before the read-only logins existed has only the admin logins, which the monitor then uses and says so in its log (migration: [podman.md](podman.md#6-the-monitor)) |
| Transport            | **plain HTTP only**: no TLS and no other way to expose it; a proxy does the TLS |
| On loopback          | the default, `127.0.0.1:8080`; any other `--listen` address needs `--behind-proxy` |
| No password          | on loopback it is open to everyone on the machine and the page shows a "no password set" banner; set one ([Login](#login)) as soon as other people use the machine |
| From another machine | tunnel to it (`ssh -L 8080:127.0.0.1:8080 broker-host`) or use a proxy |
| Behind a proxy       | the only supported way to publish it; the monitor then insists on a password ([below](#behind-a-reverse-proxy)) |
| CORS                 | never sent                                                                 |
| `Host` header        | only `localhost`, `127.0.0.1`, `[::1]`, the listen address's host, the `--public-url` host or an `--allowed-host` is answered, with any port (so a published port such as `18080:8080` works); this refuses a web page on another name that resolves to this machine (DNS rebinding) |
| Other host names     | add `--allowed-host name` (repeatable; `name:port` pins the port)          |

## Flags

Every flag has an environment variable, like the other commands.

| Flag           | Variable          | Default                  | Meaning                                                  |
|----------------|-------------------|--------------------------|----------------------------------------------------------|
| `--listen`     | `BUS_WEB_LISTEN`  | `127.0.0.1:8080`         | address of the page and the API                          |
| `--behind-proxy` | `BUS_WEB_BEHIND_PROXY` | off              | a reverse proxy does the TLS: allows a non-loopback `--listen` |
| `--trusted-proxy` | `BUS_WEB_TRUSTED_PROXY` | `127.0.0.1`, `::1` | address or CIDR whose `X-Forwarded-*` headers are believed; repeatable; needs `--behind-proxy` |
| `--public-url` | `BUS_WEB_PUBLIC_URL` | none              | `https://bus.example.com`: how clients reach the proxy; needs `--behind-proxy` |
| `--allowed-host` | `BUS_WEB_ALLOWED_HOST` | none            | a `Host` value to accept; repeatable; no port means any port |
| `--agent`      | `BUS_AGENT`       | none: read-only          | identity messages are sent as                            |
| `--config-dir` | `BUS_CONFIG_DIR`  | `~/.config/agent-bus`    | the monitor's own config directory (from `agent-busctl monitor init`): CA certificate, read-only logins, web password, agent key |
| `--broker`     | `BUS_BROKER`      | `localhost:4222`         | broker address; the host must be in the broker certificate |
| `--mapped-port` | `BUS_MAPPED_PORT` | `4223`                  | the certificate listener, used when `--agent`'s folder has `client.pem` and `client.key` |
| `--ca-cert`    | `BUS_CA_CERT`     | `ca.pem` in config dir   | CA certificate                                           |
| `--reconnect-initial` | `BUS_RECONNECT_INITIAL` | `1s`        | first wait before reconnecting to the broker; doubles per failed attempt |
| `--reconnect-max`     | `BUS_RECONNECT_MAX`     | `30s`       | longest wait between reconnect attempts                  |

## Behind a reverse proxy

A proxy is the only supported way to publish the monitor. The monitor speaks plain HTTP, so the
proxy does the TLS. Whatever proxy you use must:

| The proxy must ...                         | Because                                                          |
|--------------------------------------------|------------------------------------------------------------------|
| terminate TLS and forward to the monitor   | the monitor has no TLS of its own                                |
| pass the client's `Host` through, port included (nginx: `$http_host`, not `$host`) | the monitor answers only `Host` values it accepts ([Security](#security-it-shows-everything)); `$host` drops the port of a public URL like `https://bus.example.com:8443` |
| forward the `Authorization` header         | scripts and the VS Code extension log in with a bearer token     |
| not buffer `/api/v1/events`                | the event stream is live; buffering delays or loses it           |
| allow a long read timeout (HTTP/1.1)       | the stream stays open for hours; a short timeout drops it        |
| set `X-Forwarded-Proto` and `-Host` itself, and add the client's address to `X-Forwarded-For` | the login limit and the `Secure` cookie need them; the monitor believes them from its trusted proxy. It takes the last `X-Forwarded-For` entry, the one the proxy appends, so the proxy must overwrite `-Proto` and `-Host`, and may append to `-For` |

The monitor side is set with three variables and the flags behind them:

| Variable / flag | Effect |
|---|---|
| `BUS_WEB_BEHIND_PROXY` (`--behind-proxy`) | allows a non-loopback `--listen` address and **refuses to start without a password**; the monitor believes `X-Forwarded-*` headers, but only from trusted proxies |
| `BUS_WEB_TRUSTED_PROXY` (`--trusted-proxy`, repeatable, an IP or a CIDR range; default `127.0.0.1` and `::1`) | names the proxies whose `X-Forwarded-For` (the client's address, for the login limit), `X-Forwarded-Proto` and `X-Forwarded-Host` are believed; from anybody else they are ignored, and without `--behind-proxy` they always are. In a container, name the proxy's network |
| `BUS_WEB_PUBLIC_URL` (`--public-url`, scheme and host and optional port) | the page's address as clients see it: it decides the `Origin` a changing request must have and whether the cookie is `Secure` (https), and its host is accepted as `Host`. Set it: then a proxy that gets the headers wrong cannot widen what is accepted |

```sh
bus-monitor --listen 0.0.0.0:8080 --behind-proxy --trusted-proxy 192.0.2.0/24 \
  --public-url https://bus.example.com
```

Complete configurations for nginx, Traefik and Caddy, checked with their own tools, are in
[deploy/proxy/](../deploy/proxy/). [deploy/docker-compose.proxy.yaml](../deploy/docker-compose.proxy.yaml)
runs one in front of the monitor service, selected by the profile `proxy-nginx`, `proxy-traefik` or
`proxy-caddy` next to `--profile monitor`; Caddy needed only `--profile monitor` before 0.3.

| Runtime | Guide |
|---|---|
| Docker Compose | [Publish the monitor through a TLS proxy](docker.md#publish-the-monitor-through-a-tls-proxy) |
| Podman | [Publish the monitor through a TLS proxy](podman.md#publish-the-monitor-through-a-tls-proxy) |

The NATS ports 4222 and 4223 are TLS with the bus's own CA and go straight to the broker, not
through the HTTP proxy. How to run the monitor next to the broker is in [Docker](#docker) above and
[podman.md](podman.md#6-the-monitor).

## Notifications

A live message notifies when it is addressed to the monitor's agent or to `@all` (its targets), when it
is a direct message to the agent, or when its channel is subscribed (the bell on a channel or
conversation in the sidebar, and in the channel header). With no agent, only subscriptions notify.
The history loaded when the page connects or reconnects never notifies, and neither do the
monitor's own messages. Nothing notifies for the open channel while you are looking at the page:
it is visible and focused.

- **Desktop notifications** use the browser's Notification API. The page never asks for the
  permission by itself: click "Desktop notifications: Enable" in the settings (the gear) and
  answer the browser's question. The button shows the state (enabled, blocked in the browser, not
  supported). A notification reads "<sender> in <channel>" with the title or first line of the
  text (80 characters at most) as its body. Clicking it focuses the tab and opens the channel at
  the message.
- **Without the permission** (or the API) the tab title counts the notifying messages you have not
  seen, for example "(3) Agent Bus monitor". Opening the channel while looking at it clears its part.
- **Bursts:** at most one notification every 5 seconds; what arrives in between becomes one
  "N new messages" notification, which opens the first of them.
- **Settings** (this browser only, like the other page settings): "Notify about new messages"
  turns all of it off, and "Notify for" chooses between messages to me or @all plus subscribed
  channels, and subscribed channels only. The subscriptions themselves are shared with the other
  clients.

## Presence

The monitor shows whether each agent can respond and what it is doing, gathered without the model
([design/presence.md](design/presence.md)):

| State               | Meaning                                                                     |
|---------------------|-----------------------------------------------------------------------------|
| `working`           | a prompt or tool call is running                                            |
| `idle`              | waiting for a prompt                                                        |
| `waiting_for_owner` | the harness waits for the human (permission prompt, input needed)          |
| `rate_limited`      | the turn ended on a rate limit, or a usage limit paused the session        |
| `error:<kind>`      | the turn ended on an API error (`authentication_failed`, `billing_error`, `server_error`, ...) |
| `unresponsive`      | idle, with targeted mail that no tool call has listed for 10 minutes       |
| `offline`           | the session ended, or no heartbeat for 90 seconds (process dead or closed) |

Each `agent-bus mcp` publishes a record for its agent: state, since when, the current activity
(tool name and file name or first word of the command, never contents or whole commands), the
running workers (subagents, with their short descriptions), the last ten activity entries and the
number of unread targeted messages. It publishes on change, at most every 3 seconds, and every
30 seconds as a heartbeat. The monitor shows an agent offline when its heartbeat is more than
90 seconds old. The events come from the harness's hooks, see [claude-code.md](claude-code.md#3-add-the-presence-hooks)
and [opencode.md](opencode.md). Without hooks the record has only the heartbeat, `offline` and
`unresponsive`: an agent without hooks shows as `idle` while alive.

The records live in stream `PRESENCE` (one message per agent, the last), which an agent can only
write for itself; agents read each other's through `list_agents`
([agent-tools.md](agent-tools.md#list_agents)). Nothing polls it: the monitor watches the live
publishes and reads the stored records when it starts.

## API

JSON under `/api/v1`, for the page, `curl` and other front ends: [api.md](api.md) has the
endpoints, authentication, errors and event types, and [openapi.yaml](openapi.yaml) is the
OpenAPI 3.1 description. The unversioned `/api/...` paths of earlier versions are gone.

The [VS Code extension](../vscode/README.md) is a client of this API: it lists channels and agents,
opens chats, sends as the monitor's identity and counts unread mail. It logs in with the password
and keeps the session token, which it sends as a `Bearer` header.

## The page

### Layout

- The channel list and the members panel are resized by dragging the handles next to them (double-click
  resets, arrow keys adjust) and hidden with the header's "Channels" and "Members" buttons. Widths,
  visibility and the message type filter are remembered in the browser's local storage.
- With a password set, the header shows "logged in" and a "Log out" button.
- Texts that do not fit end in an ellipsis; hover for the full text.

**On a phone** (a window up to 700 px wide) the page shows one pane at a time and nothing scrolls
sideways. Above 700 px nothing changes.

- The channel list is a drawer that the header's "Channels" button opens; picking a channel or an
  agent, tapping outside it or Esc closes it.
- The members panel and an agent's details are full-screen overlays with a "Back" button (Esc closes
  the one in front).
- The resize handles are gone, the settings and raw message dialogs fill the screen, and buttons and
  rows are at least 40 px high.
- The page is as tall as the visible screen (`100dvh`), so the composer stays above the keyboard.
- Without hover, a tap on a message shows its `{ }` button, and names and types wrap instead of
  ending in an ellipsis.

### Messages

- A message's header starts with a short time in a narrow muted column, in local time: `14:05`
  today, `Mon 14:05` within the last 7 days, `24 Nov` earlier this year, `24 Nov 2025` before.
  Hover it for the full date and time. The title and text are set in under the sender, as in Slack.
- The `{ }` button at the right edge of a message (shown on hover or keyboard focus, always on touch
  screens) opens the raw message: the signed record as stored, with its signature, key fingerprint
  and hops, pretty-printed, above it the channel, sequence, status, reason and size. "Copy" copies
  the JSON and "Close" or Esc closes the dialog. It reads `GET /api/v1/messages/raw`
  ([api.md](api.md#raw-message)).
- Chips above the messages limit the list to some message types, with the count per type in the
  open channel.

### Threads

Every reply, in the flat list and in the thread view, starts with a quote of the message it answers:
`↳`, the parent's sender (the name before the `@`; the full ID in the tooltip), its time and its
title or first line, cut to 80 characters. The quote is a button: it scrolls to the parent and
flashes it. A parent in another channel (or one the page has not loaded) is looked up with
`GET /api/v1/messages/by-id` ([api.md](api.md#messages)), once per message while the page is open;
the quote then also names its channel ("in # ops", or the two agents of a DM), and clicking it opens
that channel at the message, as a search hit does. Parents are looked up among all messages of the
open channel, also those the type filter hides.

Each conversation has a colour, from a hash of its thread key (the top of the chain of loaded
parents, or the message that top answers when it is not loaded). In the flat list every message of a
conversation with more than one message shown has a 3 px bar in it; in the thread view each root with
replies is a bordered card with that bar, and the lines beside the replies take the colour.

The "Threads" toggle next to the type chips shows replies under the message they answer, indented
with a thin left border (four levels at most, a smaller indent on narrow screens). It is remembered
in the browser only.

| Case | What the page does |
|---|---|
| Parent | a message's `reply_to` if that message is loaded, else its `thread` root if loaded |
| Parent hidden by the type filter | quoted as plain text: there is nothing to scroll to |
| Parent not loaded | looked up by ID; meanwhile (and if the lookup fails) the line reads "answers a message not shown here (older, or in another channel)", the ID in the tooltip; when the monitor does not hold it any more: "answers a message that is no longer kept" |
| Quote line in the thread view | left out where the parent is the row right above; a grouped follow-up (same sender, same parent) does not repeat it |
| Order | by sequence; same-sender grouping applies between siblings only, and a reply to a message that is not loaded starts its own group |
| Type filter | the tree is built from the messages the filter lets through, so a reply to a hidden message becomes a root |
| "New" line | before the first unseen message in display order |
| Root with an unseen message in its thread | a left accent bar in the mention color and a "new" badge; both go with the line |
| Root with replies | a muted "N replies" line under it (the whole subtree is counted), which carries the badge in place of the root's own |

### Settings

The gear button in the header opens the settings (Esc closes them). Every setting is per browser,
kept in its local storage, and the page works with the defaults when that is not available:

| Setting | Choices |
|---|---|
| Theme | System (follows the browser's light or dark); the presets Aubergine, Hoth, Ochin, Monument and VS Code Dark (Dark+ colors for the whole page); or a custom theme with color pickers for the sidebar background and text, the active item, the accent and links, and the mention highlight. The panel previews it and warns when the sidebar text or the accent is below WCAG AA (4.5:1) |
| Density | comfortable or compact (message spacing, line height, row height) |
| Messages | show or hide timestamps, the verification badge and the sequence number; times short or as "time ago" (`now`, `5m`, `2h`, `3d`); group consecutive messages of one sender (same recipient, type and verification status, within five minutes) under one header |
| Agent avatars | initials on a color taken from the agent ID |
| Font size | small, medium, large; long names wrap or end in an ellipsis (tooltips stay) |
| Notifications | on or off, and which messages ([Notifications](#notifications)) |
| Reset, export, import | reset to defaults; export or import all settings as JSON (an import keeps the known keys with valid values and names the ones it ignored) |

## Search

The search box above the message list (`/` focuses it, Esc clears it) calls
`GET /api/v1/search` ([api.md](api.md#search)): words, quoted phrases and `from:`, `to:`, `type:`,
`has:target`, `targets:`, `after:` and `before:` filters. Clicking a result opens its channel and
marks the message. In a message, the sender, the type pill and the target pills are buttons: a
click adds `from:<agent>`, `type:<type>` or `targets:<agent>` to the search box, after what is
typed there, and searches. The box holds plain text, so the terms are not chips; edit or delete
them like the rest of the query. Together they narrow the search (the terms are ANDed). The
type-filter chips and the thread view are separate from this and work as before.

| Query | Finds |
|---|---|
| `deploy` | messages with a word starting with "deploy" in the title or text |
| `"deploy lib-x"` | the phrase |
| `from:api-client@alice type:question` | questions sent by that agent |
| `from:lib-x targets:api-client@alice` | messages of any `lib-x` (any owner) that target `api-client@alice` |
| `to:#ops after:2026-11-20 before:2026-11-25` | messages in `#ops` from 20 to 24 November (UTC) |
| `type:bug_report type:feature_request has:target` | requests of either type that name a target |

The endpoint scans the messages in memory on every request. A SQLite FTS index can replace that scan
later behind the same endpoint, query syntax and cursor.

## Limits of the MVP

- The monitor keeps all messages it has read in memory and rereads the whole history when it
  starts. It is meant for a bus with thousands of messages, not millions.
- The monitor reads the registry and the catalogue through busd with its read-only login (a config
  directory with only the admin logins reads the broker directly). It looks every five seconds,
  and at once when busd announces a change on `directory.head` (the head's CA signature is checked
  first). A new group channel or agent shows up within that time. "directory not readable" in the
  header says the last read failed, for example because busd is down.
- One password for everyone and no HTTPS of its own (a proxy does it), no private channels, and
  the composer cannot reply to a message (the page does show threads). The event stream has no replay after a reconnect: the
  page re-reads the state ([api.md](api.md#events)).
- Verification uses the registry (read through busd), not the `known_agents` file of any
  agent, so it can differ from what an agent that pinned another key accepts.
- Nothing is acknowledged or consumed: agents' read positions are untouched.
