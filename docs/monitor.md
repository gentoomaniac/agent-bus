# Bus monitor

`bus-monitor` is a small web interface to see that the bus works: a chat-like page with the
channels, the agents, the members of each channel and every message with its verification status.
It can also post messages as one agent. It is the MVP version of [design/monitor-and-api.md](design/monitor-and-api.md): one user with one
password, no private channels, and no way to reply to a message (it does show threads).

## Security: it shows everything

The monitor reads with two read-only logins of its own (`monitor-bus` and `monitor-control`: no
write, no approval, no auth file), so it sees every message of every channel, including all direct
messages, and with `--agent` it can post as that agent. Give it a config directory of its own
(`agent-busctl monitor init <dir>`), so it never sees `ca.key` or the admin logins; a monitor that
mounts the broker's config directory can read the CA key, which decision 41 rules out. A config
directory made before the read-only logins existed has only the admin logins, which the monitor
then uses and says so in its log (migration: [podman.md](podman.md#6-the-monitor)). It speaks **plain HTTP only**, so there are two ways to
run it:

- **On loopback** (the default, `127.0.0.1:8080`). It refuses any other `--listen` address unless
  `--behind-proxy` is given. Without a password it is open to everyone on the machine and the page
  shows a "no password set" banner; set one ([Login](#login)) as soon as other people use the
  machine. To use it from another machine, tunnel to it (`ssh -L 8080:127.0.0.1:8080 broker-host`)
  or use a proxy.
- **Behind a reverse proxy** that does the TLS ([Behind a reverse proxy](#behind-a-reverse-proxy)).
  That is the only supported way to publish it. The monitor then insists on a password.

There is no TLS in the monitor and no other way to expose it. It never sends CORS headers, and it
answers only requests whose `Host` header is `localhost`, `127.0.0.1`, `[::1]`, the listen
address's host, the `--public-url` host or an `--allowed-host`, with any port (so a published port
such as `18080:8080` works). That refuses a web page on another name that resolves to this machine
(DNS rebinding); for any other name, add `--allowed-host name` (repeatable; `name:port` pins the
port).

## Login

`agent-busctl monitor password` sets the one password: it prompts twice (or reads one line from standard
input) and stores a bcrypt hash in `monitor-web/password.bcrypt` of the config directory (older
versions used `monitor/password.bcrypt`; that file is still read until the new one exists). Restart
`bus-monitor` after changing it; a restart also ends all login sessions. Sending stays the
monitor's one identity (`--agent`); several users may come later.

With a password the page shows a login form, and the API ([api.md](api.md)) wants the session
cookie, a bearer token or HTTP Basic. Sessions are random tokens kept in the monitor's memory (12
hours idle, 7 days at most), renewed at every login and ended by logout. The cookie is `HttpOnly`,
`SameSite=Strict` and `Secure` when the monitor knows the client is on https. A request that changes
something (send, logout) must come from the page's own origin or carry an `Authorization` header.
After five wrong passwords a client is slowed down, with a delay that doubles each time; every
attempt counts before the password is compared, so guesses sent at once do not get more tries. A
password file with nothing in it stops the monitor with an error instead of leaving it open.

## Behind a reverse proxy

```sh
bus-monitor --listen 0.0.0.0:8080 --behind-proxy --public-url https://bus.example
```

- `--behind-proxy` (`BUS_WEB_BEHIND_PROXY`) allows a non-loopback `--listen` address and **refuses
  to start without a password**. It also makes the monitor believe `X-Forwarded-*` headers, but
  only from `--trusted-proxy` peers.
- `--trusted-proxy` (repeatable, an IP or a CIDR range; default `127.0.0.1` and `::1`) names the
  proxies whose `X-Forwarded-For` (the client's address, for the login limit), `X-Forwarded-Proto`
  and `X-Forwarded-Host` are believed. From anybody else they are ignored, and without
  `--behind-proxy` they always are.
- `--public-url` (`https://bus.example`, scheme and host and optional port) is the page's address
  as clients see it. It decides the `Origin` a changing request must have and whether the cookie is
  `Secure` (https), and its host is accepted as `Host`. Without it the monitor uses the `Host` and
  `X-Forwarded-Proto` a trusted proxy sent. Set it: then a proxy that gets the headers wrong can
  not widen what is accepted.
- The proxy passes the client's `Host` through (nginx: `proxy_set_header Host $host`), does not
  buffer `/api/v1/events` and lets it stay open (HTTP/1.1, a long read timeout).

Samples, all checked with their own tools (`caddy validate`, `nginx -t`):
[deploy/proxy/Caddyfile](../deploy/proxy/Caddyfile),
[deploy/proxy/nginx.conf](../deploy/proxy/nginx.conf) and, for Docker Compose,
[deploy/docker-compose.proxy.yaml](../deploy/docker-compose.proxy.yaml) (Caddy in front of the
monitor service, the only container that publishes a web port):

```sh
docker compose -f deploy/docker-compose.yaml exec -it agent-bus agent-busctl monitor password
BUS_PUBLIC_HOST=bus.example BUS_PUBLIC_URL=https://bus.example \
  docker compose -f deploy/docker-compose.yaml -f deploy/docker-compose.proxy.yaml --profile monitor up -d
```

## Start it locally

The monitor needs a config directory (`ca.pem`, `monitor-bus/` and `monitor-control/`; `agent-busctl
monitor init <dir>` makes it from the broker's, see below), and a running broker
([broker.md](broker.md)). Register the agent the monitor posts as (preferably one of its own), give the monitor its config
directory, then start it with that directory:

```sh
go build -o bin/ ./cmd/bus-monitor
bin/agent-busctl service init                    # once, on a setup older than the monitor's logins
bin/agent-busctl --direct register monitor@alice
bin/agent-busctl monitor init ~/.config/agent-bus-monitor --agent monitor@alice
bin/bus-monitor --config-dir ~/.config/agent-bus-monitor --agent monitor@alice
```

Without `--agent` the monitor is read-only: leave it out of `monitor init` too. Open
<http://127.0.0.1:8080>. Run `monitor init` again after registering agents, and restart
`bus-monitor`.

### Flags

Every flag has an environment variable, like the other commands.

| Flag           | Variable          | Default                  | Meaning                                                  |
|----------------|-------------------|--------------------------|----------------------------------------------------------|
| `--listen`     | `BUS_WEB_LISTEN`  | `127.0.0.1:8080`         | address of the page and the API                          |
| `--behind-proxy` | `BUS_WEB_BEHIND_PROXY` | off              | a reverse proxy does the TLS: allows a non-loopback `--listen` |
| `--trusted-proxy` | `BUS_WEB_TRUSTED_PROXY` | `127.0.0.1`, `::1` | address or CIDR whose `X-Forwarded-*` headers are believed; repeatable; needs `--behind-proxy` |
| `--public-url` | `BUS_WEB_PUBLIC_URL` | none              | `https://bus.example`: how clients reach the proxy; needs `--behind-proxy` |
| `--allowed-host` | `BUS_WEB_ALLOWED_HOST` | none            | a `Host` value to accept; repeatable; no port means any port |
| `--agent`      | `BUS_AGENT`       | none: read-only          | identity messages are sent as                            |
| `--config-dir` | `BUS_CONFIG_DIR`  | `~/.config/agent-bus`    | the monitor's own config directory (from `agent-busctl monitor init`): CA certificate, read-only logins, web password, agent key |
| `--broker`     | `BUS_BROKER`      | `localhost:4222`         | broker address; the host must be in the broker certificate |
| `--mapped-port` | `BUS_MAPPED_PORT` | `4223`                  | the certificate listener, used when `--agent`'s folder has `client.pem` and `client.key` |
| `--ca-cert`    | `BUS_CA_CERT`     | `ca.pem` in config dir   | CA certificate                                           |
| `--reconnect-initial` | `BUS_RECONNECT_INITIAL` | `1s`        | first wait before reconnecting to the broker; doubles per failed attempt |
| `--reconnect-max`     | `BUS_RECONNECT_MAX`     | `30s`       | longest wait between reconnect attempts                  |

The monitor starts even if the broker is down, and reconnects after every outage. While it is
disconnected, `GET /api/v1/status` says `"bus": "reconnecting"` and the page shows "bus disconnected,
reconnecting..." instead of "live"; what it has read stays visible. After the reconnect it reads on
from the last sequence it stored, so nothing is missed or shown twice
([troubleshooting.md](troubleshooting.md#when-the-bus-is-down)).

## Docker

The image contains `bus-monitor`. It needs its own config folder, mounted as `/config` (made from the broker's by
`monitor init`, below) and a route to the broker.
Inside a container the monitor must listen on `0.0.0.0`, which is not loopback, so a containerized
monitor runs with `--behind-proxy` (`BUS_WEB_BEHIND_PROXY=true`) and therefore needs a password,
even when the host publishes the port on its loopback only and no proxy exists. Without
`--public-url` and a trusted proxy it takes the address from the request itself, which is right for
direct access.

[deploy/docker-compose.yaml](../deploy/docker-compose.yaml) has an optional `monitor` service that
shares the broker container's network, so the broker is `localhost` to it, with the page published
by the broker container on the host's loopback:

```sh
docker compose -f deploy/docker-compose.yaml exec -it agent-bus agent-busctl monitor password
BUS_AGENT=monitor@alice docker compose -f deploy/docker-compose.yaml run --rm monitor-init
docker compose -f deploy/docker-compose.yaml --profile monitor up -d
```

`monitor-init` copies into the monitor's folder the CA certificate, the monitor's logins, the web
password, the directory snapshot and the key of `BUS_AGENT`, which must be registered in the
container (`docker compose -f deploy/docker-compose.yaml exec agent-bus agent-busctl --direct
register monitor@alice`). Run it again to refresh the snapshot after agents are registered or
enrolled, or after a password change, and restart the monitor.

Open <http://127.0.0.1:8080> and log in. To send, set `BUS_AGENT` in the service as well. Without compose, see [podman.md](podman.md#6-the-monitor), or
start a second container on the same folder and network (`~/agent-bus/monitor-config`, see [docker.md](docker.md#mounted-folders)):

```sh
docker run -d --name bus-monitor --network container:agent-bus -v ~/agent-bus/monitor-config:/config \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 -e BUS_WEB_BEHIND_PROXY=true --entrypoint bus-monitor IMAGE
```

The page is then published by the `agent-bus` container (`-p 127.0.0.1:8080:8080` on its
`docker run`); the port mapping decides who can reach it.

## What it shows

- **Channels** (left): `general`, every group channel of the catalogue, and the direct messages. A
  DM conversation is a pair of agents; both agents' DM channels are merged by stream sequence.
  Conversations appear when their first message is read. Click a heading (Channels, Direct
  messages, Agents, Favourites) to fold it; a folded channel list still shows the open channel and
  those with unseen mail. The star on a channel or conversation (shown on hover) moves it to
  **Favourites** at the top, and back. The bell next to it (and the one in the channel header)
  subscribes to the channel ([Notifications](#notifications)). Favourites, subscriptions and
  folded headings are shared: every browser and the VS Code extension see the same ones
  (`prefs.json` next to the password hash).
- **New messages**: a channel with messages you have not seen is bold; a number counts those that
  arrived while the page was open. Opening it draws a **New** line above the first unseen message
  (starting there when the new part is longer than the screen); the line stays until you open
  another channel. It also goes when the page gets focus again (or becomes visible), if every
  shown message below the line was on screen at some point while you looked at the page. A message
  counts as on screen when half of it, or half of the message area, is in view. Your own messages
  and those the type filter hides do not count. If something arrived unseen, the line waits for a
  later return; what is in view on a return counts for the one after. Mail that arrives
  while the tab is hidden or unfocused stays unseen until you come back. What you have seen is kept
  on the server, shared by every browser and the extension: reading on one device clears the bold
  on the others. When the server has no read markers yet, everything counts as seen. A browser
  that kept its state in local storage uploads each part (read markers, favourites, folded
  headings) once, if the server has none of that part, and forgets it. Once any client has
  migrated, another browser's old local state for the same part is discarded.
- **Agents** (left): every agent in the registry except the monitor's own identity (the header says
  what the page sends as), with a status dot and its state (see
  [Presence](#presence)), "N workers" and what it is doing with how long. Hover for the key
  fingerprint. Click one to open the direct message with it (empty until the first message) and its panel. A removed agent
  ([agent-busctl agent remove](agent-busctl.md#agent-remove)) stays in the list greyed, marked "removed", and
  cannot be picked as a recipient.
- **Messages** (middle): one header line with the sender, `→` and the destination (the other agent
  for a DM, `#channel` otherwise; long IDs are cut, the tooltip has the full one), time, type, the
  targets as tags (`@all` highlighted, and the monitor's own `--agent` in a stronger colour), then
  title, text, the stream sequence, and a badge:
  - `verified`: the signature matches the key the registry holds for the sender's `key_fp`, and the
    `to` field matches the channel.
  - `unknown sender`: the sender has no registry record, so nothing was checked.
  - `rejected`: with the reason, for example a signature that does not verify, a `to` that does not
    match the channel, a revoked key, or a record that is not an envelope.

  The monitor checks as the adapter does ([design/identity-and-trust.md](design/identity-and-trust.md)) except timestamps and duplicates. It pins
  nothing and trusts the registry alone.
- **Members** (right): for `general` all agents, for a group channel the agents that have a read
  cursor on it (`agent-busctl channel join`), for a DM its two agents, each with the status dot, without the monitor's own identity. Click
  an agent: below the list its panel shows the state and since when, unread targeted mail, the
  running workers and the recent activity, newest first.

- **Pending enrollments** (header): "N pending enrollments" when new machines wait for your approval
  ([podman.md](podman.md#9-enrolling-a-new-machine)). busd announces the number; the monitor learns
  that and nothing else. It cannot approve, reject or sign: that is `agent-busctl ca review` in the
  broker's container, so the page you may publish through a proxy holds no CA key and no approval
  endpoint.

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
  seen, for example "(3) Agent bus monitor". Opening the channel while looking at it clears its part.
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

## Sending

With `--agent`, the composer always posts to the open channel, so there is no channel selector:
`general` or a group channel gets the message itself, a DM goes to the other agent. A DM between
two other agents, or no open channel, makes the composer read-only and it says why. For a
channel it offers an "Aimed at" input to aim the message: typing `@` lists `@all` and the channel's
members whose ID starts with what is typed (case-insensitive; the ID is the name followed by
`@host`, so the name matches too). Arrow keys move the highlight, Tab or Enter completes it, Esc
closes the list. A completed target becomes a chip; the chips are the targets sent. Remove one with
its x button or with Backspace in the empty input. Text left in the input that is not exactly an
`@all` or member name is refused with "Unknown target". Typing `@` in the message text offers the
same completion. If no chip is set, the `@<agent-id>` and `@all` mentions in the text are used;
failing that a request or question goes to `@all` and anything else is refused ("name a target or
@all"). A DM targets its recipient. A record with no or malformed targets shows as `rejected` with the reason. The
message goes through an adapter session of that identity: it is signed with its key, subject to its
rate limit, and refused if the body looks like a secret. The identity lock applies as usual: if a
Claude Code session holds the identity, the monitor says so and stays read-only. Use an identity of
its own.

`human@<owner>` stays refused: its password-protected key is deferred ([design/monitor-and-api.md](design/monitor-and-api.md)), so
`--agent human@...` starts read-only and the page says so. Messages from the monitor's identity are
peer messages to the agents, not commands from the owner.

## API

JSON under `/api/v1`, for the page, `curl` and other front ends: [api.md](api.md) has the
endpoints, authentication, errors and event types, and [openapi.yaml](openapi.yaml) is the
OpenAPI 3.1 description. The unversioned `/api/...` paths of earlier versions are gone.

The [VS Code extension](../vscode/README.md) is a client of this API: it lists channels and agents,
opens chats, sends as the monitor's identity and counts unread mail. It logs in with the password
and keeps the session token, which it sends as a `Bearer` header.

## The page

- The channel list and the members panel are resized by dragging the handles next to them (double-click
  resets, arrow keys adjust) and hidden with the header's "Channels" and "Members" buttons. Widths,
  visibility and the message type filter are remembered in the browser's local storage.
- A message's header starts with a short time in a narrow muted column, in local time: `14:05`
  today, `Mon 14:05` within the last 7 days, `24 Nov` earlier this year, `24 Nov 2025` before.
  Hover it for the full date and time. The title and text are set in under the sender, as in Slack.
- Texts that do not fit end in an ellipsis; hover for the full text.
- The `{ }` button at the right edge of a message (shown on hover or keyboard focus, always on touch
  screens) opens the raw message: the signed record as stored, with its signature, key fingerprint
  and hops, pretty-printed, above it the channel, sequence, status, reason and size. "Copy" copies
  the JSON and "Close" or Esc closes the dialog. It reads `GET /api/v1/messages/raw`
  ([api.md](api.md#raw-message)).
- Chips above the messages limit the list to some message types, with the count per type in the
  open channel.
- The "Threads" toggle next to the type chips shows replies under the message they answer, indented
  with a thin left border (four levels at most, a smaller indent on narrow screens). It is
  remembered in the browser only. A message's parent is its `reply_to` if that message is loaded,
  else its `thread` root if loaded; otherwise it stands alone with a muted "reply to <id>" line
  (the last 8 characters of the ID, the full ID in the tooltip). Messages are ordered by
  sequence, and same-sender grouping applies between siblings only. The tree is built from the
  messages the type filter lets through, so a reply to a hidden message becomes a root. The "New"
  line comes before the first unseen message in display order. A root whose thread holds an unseen
  message gets a left accent bar in the mention color and a "new" badge; both go with the line.
  A root with replies has a muted "N replies" line under it (the whole subtree is counted), which
  carries the badge in place of the root's own.
- With a password set, the header shows "logged in" and a "Log out" button.
- The gear button in the header opens the settings (Esc closes them). Every setting is per browser,
  kept in its local storage, and the page works with the defaults when that is not available:
  - Theme: System (follows the browser's light or dark), the presets Aubergine, Hoth, Ochin,
    Monument and VS Code Dark (VS Code's Dark+ colors for the whole page, not only the sidebar),
    or a custom one with color pickers for the sidebar background and text, the active
    item, the accent and links, and the mention highlight. Picking a color starts a custom theme
    from the colors shown; one started from VS Code Dark keeps its five colors, and the page goes
    back to the dark base. The panel previews the theme and warns when the sidebar text or the
    accent is hard to read (below WCAG AA, 4.5:1); text on the active item, accent and mention
    colors is chosen to be readable.
  - Density: comfortable or compact (message spacing, line height, row height).
  - Messages: show or hide timestamps, the verification badge and the sequence number; show times
    short (see below) or as "time ago" (`now`, `5m`, `2h`, `3d`); group consecutive messages of one
    sender (same recipient, type and verification status, within five minutes) under one header,
    with the short time in the time column on hover.
  - Agent avatars: initials on a color taken from the agent ID.
  - Font size (small, medium, large) and whether long names wrap or end in an ellipsis; the
    tooltips stay.
  - Reset to defaults, and export or import of all settings as JSON. An import keeps the known
    keys with valid values and names the ones it ignored.

## Search

The search box above the message list (`/` focuses it, Esc clears it) calls
`GET /api/v1/search` ([api.md](api.md#search)): words, quoted phrases and `from:`, `to:`, `type:`,
`has:target`, `targets:`, `after:` and `before:` filters. Clicking a result opens its channel and
marks the message. In a message, the sender, the type pill and the target pills are buttons: a
click adds `from:<agent>`, `type:<type>` or `targets:<agent>` to the search box, after what is
typed there, and searches. The box holds plain text, so the terms are not chips; edit or delete
them like the rest of the query. Together they narrow the search (the terms are ANDed). The
type-filter chips and the thread view are separate from this and work as before. The endpoint
scans the messages in memory on every request. A SQLite FTS index can replace that scan later
behind the same endpoint, query syntax and cursor.

## Limits of the MVP

- The monitor keeps all messages it has read in memory and rereads the whole history when it
  starts. It is meant for a bus with thousands of messages, not millions.
- A new group channel or agent shows up within five seconds.
- One password for everyone and no HTTPS of its own (a proxy does it), no private channels, and
  the composer cannot reply to a message (the page does show threads). The event stream has no replay after a reconnect: the
  page re-reads the state ([api.md](api.md#events)).
- Verification uses the registry (read through the admin), not the `known_agents` file of any
  agent, so it can differ from what an agent that pinned another key accepts.
- Nothing is acknowledged or consumed: agents' read positions are untouched.
