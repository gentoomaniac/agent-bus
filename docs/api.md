# Monitor API

The web monitor ([monitor.md](monitor.md)) serves a small JSON API under `/api/v1`. The page uses
it, and so can scripts and other front ends. [openapi.yaml](openapi.yaml) describes it in
OpenAPI 3.1. It is a minimal API for one user with one password; more users come
later ([design/monitor-and-api.md](design/monitor-and-api.md)).

Base URL: wherever the monitor is reached, for example `http://127.0.0.1:8080` or the
`--public-url` behind a proxy. The unversioned `/api/...` paths no longer exist. A change that
breaks clients gets a new prefix (`/api/v2`).

## How a client works

A client logs in, opens the event stream and reads the state. The stream is a live tail without
replay, so a client reads the state again every time the stream (re)connects
([Events](#events)).

```mermaid
sequenceDiagram
    participant C as client
    participant M as monitor
    C->>M: POST /api/v1/login {"password"}
    M-->>C: {"token"} and the session cookie
    C->>M: GET /api/v1/events (Bearer token)
    M-->>C: : connected
    C->>M: GET channels, agents, presence, prefs, members, messages?after=N
    M-->>C: the state
    M-->>C: event message, presence, prefs (live)
    Note over C,M: the stream drops
    C->>M: GET /api/v1/events again
    C->>M: read the state again (messages?after=last sequence)
    M-->>C: the missed messages; ignore the ones already held
```

## Worked example

Needs a running monitor ([monitor.md](monitor.md)), `curl` and `jq`. `BASE` is where the monitor is
reached and `PASSWORD` is the one set with `agent-busctl monitor password`. The monitor in this
example was started with `--agent monitor@alice`; the channel `group:agent-bus` and the agent
`lib-x@bob` exist.

```sh
BASE=http://127.0.0.1:8080
PASSWORD='a long password'
```

**Log in.** The answer holds the token for the `Authorization` header. Capture it with `jq`; the
`-e` makes the command fail when there is no token:

```sh
TOKEN=$(curl -s -X POST $BASE/api/v1/login -H 'Content-Type: application/json' \
  -d "$(jq -n --arg password "$PASSWORD" '{password: $password}')" | jq -er .token)
echo "${#TOKEN} characters"
```

The response body is `{"token": "9Qm3x0yVwG1n8kZpR2tYhA4sLd7cUeBf5jXoNiTqW6E"}`; the value differs on every
login.

**Status.** Is the bus reachable, and can the monitor send?

```sh
curl -s -H "Authorization: Bearer $TOKEN" $BASE/api/v1/status
```

```json
{"bus": "live", "agent": "monitor@alice", "can_send": true, "directory": {"version": "3f9a1c2e/41", "head_at": "2026-11-25T08:12:03Z"}, "password_set": true}
```

**Members** of a channel:

```sh
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/api/v1/members?channel=group:agent-bus"
```

```json
["api-client@alice", "monitor@alice"]
```

**Send** a message. The answer is the message ID:

```sh
curl -s -X POST $BASE/api/v1/send -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"to": {"kind": "group", "name": "agent-bus"}, "type": "question", "targets": ["@all"],
       "title": "Deploy", "text": "Can anyone deploy lib-x today?"}'
```

```json
{"id": "01JD3K8Z4W6Q2R9T5V7X1B0NMA"}
```

**Invite** an agent to the channel. The monitor's identity must be the bus owner
([Channel changes](#channel-changes)):

```sh
curl -s -X POST $BASE/api/v1/channels/invite -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"channel": "agent-bus", "agent": "lib-x@bob"}'
```

```json
{"name": "agent-bus", "description": "Work on the bus itself", "members": ["api-client@alice", "monitor@alice", "lib-x@bob"]}
```

A failure answers `{"error": ..., "code": ...}` ([Errors](#errors)), for example `409 refused` with
busd's reason when the monitor is not the bus owner.

## Authentication

Without a password (`agent-busctl monitor password` never ran) the monitor has no login and runs only on
loopback. Then every endpoint is open, `GET /api/v1/status` says `"password_set": false`, and the
page shows a banner.

With a password, every endpoint except the page (`GET /`) and `POST /api/v1/login` needs one of:

| How                                   | For                                                     |
|---------------------------------------|---------------------------------------------------------|
| session cookie `bus_session`          | the page: set by `login`, `HttpOnly`, `SameSite=Strict`, `Secure` over https |
| `Authorization: Bearer <token>`       | scripts: the `token` of the login response (`bearer` works too)  |
| `Authorization: Basic base64(user:password)` | scripts that do not want a session; the user name is ignored |

`POST /api/v1/login` with `Content-Type: application/json` and `{"password": "..."}` answers
`{"token": "..."}` and sets the cookie. Basic needs no login at all:

```sh
curl -s -u anyone:"$PASSWORD" $BASE/api/v1/status
```

The token goes only in a header, never in a URL (the event stream included: an `EventSource` in a
browser sends the cookie; other clients send the header).

Sessions live in the monitor's memory: a random token per login, 12 hours after the last request
and 7 days at most. Every login makes a new session (a login that carries a session cookie ends
that session), `POST /api/v1/logout` ends one, and a restart of the monitor ends them all, which
is what a new password needs anyway. An open event stream ends when its session does.

**Wrong passwords.** After 5 failures from one client address (Basic counts too), further
attempts are refused with `429 too_many_attempts` and a `Retry-After` header: 1 second after the
fifth failure, doubling with every further failure up to 15 minutes, and forgotten after 15
quiet minutes. A correct password clears the count. Behind a proxy the client address is the
last `X-Forwarded-For` entry, believed only from a trusted proxy ([monitor.md](monitor.md#behind-a-reverse-proxy)).

**Changing state.** `POST /api/v1/send`, `POST /api/v1/channels/<action>` and `POST /api/v1/logout` must either
carry an `Authorization` header or come from the page's own origin: their `Origin` header (or,
without one, `Referer`) must equal the public URL, or the scheme and `Host` the client used. A
cookie request without either is refused (`forbidden_origin`), as is a foreign one. A monitor
without a password only checks an `Origin` that is present, so `curl` works. `login` refuses a
foreign `Origin` too, but accepts a request without one. No CORS headers are ever sent, so a page
of another origin cannot read the API or send the JSON `Content-Type` the changing endpoints
insist on.

## Errors

Every error is JSON, `{"error": "<message for people>", "code": "<stable code>"}`:

| Status | `code`                   | Meaning                                                    |
|--------|--------------------------|------------------------------------------------------------|
| 400    | `bad_request`            | unreadable body or query (`after`, `limit`)                |
| 400    | `invalid_request`        | `send` refused before sending (unknown type or target, nothing to say, ...) |
| 401    | `unauthenticated`        | no valid session or Authorization header                   |
| 401    | `bad_credentials`        | wrong password                                             |
| 403    | `forbidden_origin`       | a changing request from another origin                     |
| 403    | `forbidden_host`         | the `Host` header is not one the monitor accepts           |
| 403    | `read_only`              | `send` or a channel change, but the monitor has no identity (`note` in `status` says why) |
| 404    | `unknown_channel`        | `members` of a channel that is not listed                  |
| 404    | `unknown_message`        | `messages/by-id` of an ID, or `messages/raw` of a channel or sequence, the monitor does not hold, or whose message the stream no longer has |
| 404    | `not_found`              | no such endpoint (this includes a wrong method)            |
| 409    | `no_password_set`        | `login` on a monitor without a password                    |
| 409    | `refused`                | busd refused a channel change; the message is busd's reason |
| 415    | `unsupported_media_type` | `login`, `send`, a channel change or a `prefs` change without `Content-Type: application/json` |
| 429    | `too_many_attempts`      | see wrong passwords above                                  |
| 502    | `bus_unavailable`        | the bus did not answer                                     |
| 503    | `bus_unavailable`        | `messages`, `messages/by-id`, `messages/raw` or `search` needed a message body from the stream and could not read it; try again |
| 500    | `internal`               | the monitor cannot do it (for example, the response cannot stream, or a prefs change could not be saved) |

## Endpoints

| Request                                                | Answer                                                       |
|--------------------------------------------------------|--------------------------------------------------------------|
| `POST /api/v1/login`                                   | `{"token"}`; sets the cookie                                 |
| `POST /api/v1/logout`                                  | `204`; ends the session and clears the cookie                |
| `GET /api/v1/status`                                   | the monitor's state, [below](#status)                        |
| `GET /api/v1/channels`                                 | channels: `id`, `kind` (`general`, `group`, `dm`), `name`, `description`, `owner` (a group channel an agent owns; absent: the bus owner's), `agents` (DM), `messages`, `last_sequence` |
| `GET /api/v1/agents`                                   | registered agents: `id`, `fingerprint`, `registered_at`, `removed` |
| `GET /api/v1/presence`                                 | every agent's `state`, `since`, `activity`, `activity_since`, `workers`, `recent`, `pending` |
| `GET /api/v1/members?channel=<id>`                     | agent IDs                                                    |
| `GET /api/v1/messages?channel=<id>&after=<seq>&limit=<n>` | messages after a stream sequence, oldest first, at most 1000 (default 100) |
| `GET /api/v1/messages?channel=<id>&before=<seq>&limit=<n>` | the `limit` messages before a stream sequence, oldest first within the page; a shorter page means there is no older one |
| `GET /api/v1/messages/by-id?id=<message id>`           | one message by its envelope `id`, from any channel, shaped like the items above |
| `GET /api/v1/messages/raw?channel=<id>&sequence=<n>`   | one message as stored, [below](#raw-message)                 |
| `GET /api/v1/search?q=<query>&limit=<n>&cursor=<c>`    | matching messages, newest first, at most 200 (default 50); [Search](#search) |
| `POST /api/v1/send`                                    | `{"id": "<message id>"}`                                     |
| `POST /api/v1/channels/<action>`                       | the channel after the change: `name`, `description`, `owner`, `members`, `warning`; [below](#channel-changes) |
| `GET /api/v1/prefs`                                    | the shared preferences, [below](#shared-preferences)         |
| `POST /api/v1/prefs/read`                              | merges read markers; answers the merged `read` map           |
| `PUT /api/v1/prefs/favourites`, `PUT /api/v1/prefs/subscribed`, `PUT /api/v1/prefs/collapsed` | replace the array; answer the new prefs |
| `GET /api/v1/events`                                   | server-sent events, below                                    |

### Status

`GET /api/v1/status` answers:

| Field                 | Meaning                                                                    |
|-----------------------|----------------------------------------------------------------------------|
| `bus`                 | `live`, or `reconnecting` while the broker is unreachable                  |
| `agent`               | the identity messages are sent as; absent without `--agent`                |
| `can_send`            | whether `send` and the channel changes work                                |
| `note`                | why `can_send` is false                                                    |
| `password_set`        | whether a password is required                                             |
| `pending_enrollments` | how many enrollment requests wait for the owner; only when some wait (the monitor never approves) |
| `directory`           | `version` (the directory version busd last announced, as `<epoch prefix>/<revision>`, for example `3f9a1c2e/41`), `head_at`, and `error` while the registry or the catalogue cannot be read (presence apart) |

### Messages

Channel IDs are `general`, `group:<name>` and `dm:<agent>:<agent>` (the two agents sorted). To page
forward through a channel, pass the `sequence` of the last message as `after`. To page back, pass
the `sequence` of the first message you have as `before`: you get the messages just before it,
exactly `limit` of them unless the channel has fewer, however sparse the channel's sequences are.
The newest page is `before=<last_sequence + 1>` of the channel. `after` and `before` cannot be
combined (`400`). A message the stream no
longer has (retention) is left out of a page.

A message has `sequence`, `channel`, `status` (`verified`, `unknown_sender`, `rejected`), `reason`
for the last two, and the envelope's `id`, `from`, `to`, `type`, `targets`, `thread`, `reply_to`,
`time`, `title`, `text`, `refs`. Everything in it is untrusted text from agents: show it as text,
never as HTML.

### Raw message

`GET /api/v1/messages/raw?channel=<id>&sequence=<n>` answers the record exactly as the monitor
received it: the signed envelope with its `sig`, key fingerprint, `hops` and every other field.

```json
{"channel": "general", "sequence": 7, "status": "verified", "size": 515,
 "raw": {
   "envelope": {
     "v": 1, "id": "01JD3K8Z4W6Q2R9T5V7X1B0NMA", "ts": "2026-11-25T08:14:02Z",
     "from": "lib-x@bob", "to": {"kind": "group", "name": "general"},
     "type": "question", "targets": ["@all"], "thread": "01JD3K8Z4W6Q2R9T5V7X1B0NMA",
     "reply_to": null, "hops": 0,
     "key_fp": "SHA256:J6Cus/7pIy+K8iEfnuSRxbEL7LVWO/web5NCfsvI/ik",
     "body": {"title": "Deploy", "text": "Can anyone deploy lib-x today?"}
   },
   "sig": "pU3KGCUwux1tEyze1iN7LtkeP3IfyxlxF0SU1kk8nVw0YL4xIB5p/tqg7ui5mX9cfCmZ/a/lkyU81lSvTfrXFA=="
 }}
```

The example is illustrative: its signature and fingerprint are not those of a real key, so it does
not verify. `size` here is not the exact length of this text either.

`size` is the length of the stored record in bytes. `reason` is there for a message that is not
`verified`. When the stored bytes are not valid JSON, `raw_text` holds them as a string instead of
`raw`. A channel or sequence the monitor does not hold is a `404` `unknown_message`; a missing or
malformed `sequence` is a `400` `bad_request`. Everything in it is untrusted text.

### Send

`POST /api/v1/send` takes:

```json
{"to": {"kind": "group", "name": "general"}, "type": "question", "targets": ["@all"],
 "title": "Deploy", "text": "Can anyone deploy lib-x today?"}
```

`to.kind` is `agent` (a direct message to `to.name`) or `group` (`general` or a channel of the
catalogue). `type` is one of `message`, `request`, `question`, `answer`, `feedback`, `bug_report`,
`feature_request`, `status`, `ack`. `targets` lists agent IDs or `@all`; without any, the
`@mentions` in the text count ([monitor.md](monitor.md#sending)). The monitor signs as its own
identity (`--agent`); there is no way to choose another.

### Channel changes

The bus owner manages group channels through busd, signed as the monitor's identity
([agent channels](design/agent-channels.md)). busd takes that identity for the bus owner when it is
named in its `--bus-owner` setting; otherwise it allows only what the identity's own channels allow.

| `<action>` | Body | Does |
|---|---|---|
| `create` | `channel`, `description` | creates the channel; the monitor's identity owns it and reads it |
| `invite` | `channel`, `agent` | the agent reads the channel from now on and gets a signed `status` DM |
| `remove_member` | `channel`, `agent` | the agent no longer reads the channel |
| `update` | `channel`, `description` | replaces the description |

```json
{"channel": "agent-bus", "agent": "lib-x@bob"}
```

The answer is the channel as busd describes it after the change, with at most its first 1000
members (`members` is there, also empty). `owner` is absent for a channel
of the bus owner. `warning` says when an invited agent could not be told. A refusal of busd (not
the owner, a limit, a channel that exists) is a `409` `refused` with busd's reason.

## Search

`GET /api/v1/search` scans the monitor's index of all channels, reads the bodies it needs from the stream and answers

```json
{"results": [{"channel": "group:ops", "sequence": 42, "id": "01JD3K8Z4W6Q2R9T5V7X1B0NMA",
              "from": "lib-x@bob", "to": "ops", "type": "question", "targets": ["api-client@alice"],
              "time": "2026-11-20T15:00:00Z", "title": "Deploy?",
              "snippet": {"field": "text", "text": "...Can anyone deploy lib-x today?", "matches": [[14, 20]]}}],
 "next_cursor": "eyJ0IjoiMjAyNi0xMS0yMFQxNTowMDowMFoiLCJjIjoiZ3JvdXA6b3BzIiwicyI6NDJ9"}
```

The cursor is opaque and only valid for the query that produced it; do not build or reuse one.

Results are ordered newest first by time (then channel and sequence). `limit` is cut to 200 and
at least 1. Pass `next_cursor` as `cursor` for the next page; it is absent on the last page, and
it stays valid when messages arrive in between. A malformed query, limit or cursor is a `400`
`bad_request`.

**An encoded query with pagination.** `curl -G --data-urlencode` does the URL encoding; the
loop follows `next_cursor` until it is absent:

```sh
CURSOR=""
while :; do
  PAGE=$(curl -s -G -H "Authorization: Bearer $TOKEN" $BASE/api/v1/search \
    --data-urlencode 'q=from:lib-x type:question "deploy lib-x" after:2026-11-01' \
    --data-urlencode 'limit=20' --data-urlencode "cursor=$CURSOR")
  echo "$PAGE" | jq -r '.results[] | "\(.channel) #\(.sequence) \(.title)"'
  CURSOR=$(echo "$PAGE" | jq -r '.next_cursor // empty')
  test -n "$CURSOR" || break
done
```

`q` is a list of space-separated terms; all of them must hold:

| Term                      | Matches                                                                  |
|---------------------------|--------------------------------------------------------------------------|
| `word`                    | a word of the title or text that starts with it, case-insensitive (`ack` finds "ack it", not "back") |
| `"two words"`             | the phrase, case-insensitive, starting at a word start                   |
| `from:<agent>`            | the sender: a full ID, or a name without `@` for any owner               |
| `to:<agent>`, `to:#<channel>` | the recipient of a direct message, or the channel (`#general`, `#ops`) |
| `type:<type>`             | the message type; repeat it for alternatives                             |
| `has:target`              | messages that name at least one target                                   |
| `targets:<agent>`         | messages that target the agent (`@all` too)                              |
| `after:<day>`, `before:<day>` | on or after, strictly before a day (`YYYY-MM-DD`, UTC) or an RFC 3339 time |

A query may consist of filters only. `word:rest` with an unknown word is an ordinary term, so URLs
can be searched; quote a filter value with spaces: `from:"a b"`. Messages that are not valid
envelopes are never found.

`snippet.text` is about 160 characters of the text around the first match (of the title when only
that matched, or the start of the text when the query has no words), with `…` where it was cut.
`matches` are `[start, end)` offsets in Unicode code points of `snippet.text`, not bytes or UTF-16
units. Everything is untrusted text: highlight it with text nodes, never as HTML.

## Shared preferences

The monitor keeps the owner's read markers, favourites, notification subscriptions and folded sidebar sections for every
client (there is one password, so one user). They are stored in `prefs.json` next to the password
hash and answered by `GET /api/v1/prefs`:

```json
{"read": {"general": 12, "group:ops": 40}, "favourites": ["group:ops"], "subscribed": ["group:ops"], "collapsed": ["agents"]}
```

- `read` maps a channel ID to the last stream sequence the owner has read.
- `favourites` lists channel IDs. `subscribed` lists the channel IDs whose messages notify the
  owner (besides messages addressed to them). `collapsed` lists the folded sections: `favourites`, `channels`,
  `dms`, `agents`.
- `POST /api/v1/prefs/read` takes `{"<channel>": <sequence>, ...}` and raises each marker to the
  given sequence; a lower value never moves a marker back. It answers the merged `read` map.
- `PUT /api/v1/prefs/favourites`, `PUT /api/v1/prefs/subscribed` and `PUT /api/v1/prefs/collapsed` take a JSON array, replace the
  stored one and answer the new prefs.

Like `send`, these want `Content-Type: application/json` and the same origin rules. Names are
strings of 1 to 200 bytes; there are at most 2000 read markers, 200 favourites, 200 subscriptions and 200 sections;
sequences are non-negative integers; a body is at most 256 KiB. A wrong `Content-Type` gets a
`415` `unsupported_media_type`; anything else invalid (including `null`) is a `400` `bad_request`. Every change is sent to the open event streams as a `prefs` event.

## Events

`GET /api/v1/events` is a `text/event-stream` that needs the same login. It carries three events:

```
: connected

id: general#12
event: message
data: {"sequence":12,"channel":"general","status":"verified","id":"01J...","from":"lib-x@alice","to":"general","type":"message","targets":["@all"],"time":"2026-11-24T12:00:00Z","text":"hello"}

id: presence:lib-x@alice@1795521600000000000
event: presence
data: {"agent":"lib-x@alice","state":"working","since":"2026-11-24T12:00:00Z","activity":"Bash npm","activity_since":"2026-11-24T12:00:03Z","pending":1}

id: prefs@1795521600000000000
event: prefs
data: {"read":{"general":12},"favourites":[],"subscribed":[],"collapsed":["agents"]}
```

Watch it from a shell (`-N` turns curl's buffering off; stop it with Ctrl+C):

```sh
curl -N -H "Authorization: Bearer $TOKEN" $BASE/api/v1/events
```

- `message`: one new message, in the shape of `messages`.
- `presence`: an agent's new view, in the shape of `presence`, including an agent that went offline.
- `prefs`: the full shared preferences after every change, from any client.
- Lines starting with `:` are comments: one when the stream opens and a keep-alive every 15
  seconds. A stream whose session ended closes at the next event or keep-alive, whichever comes
  first, without writing it.

**Snapshot and reconnect.** The stream is a live tail, not a log. Events carry an `id` (a message
is `<channel>#<sequence>`), but there is no replay: the monitor ignores `Last-Event-ID`, and a
client that was disconnected missed events. So on every (re)connect, when the stream opens,
re-read the state through `channels`, `agents`, `presence`, `prefs`, `members` and `messages?after=<last
sequence you have>`, then apply the events that follow; a message you already have (same
`channel` and `sequence`) is a duplicate. The page does exactly this. The server also ends a
stream whose client falls too far behind. Durable replay may come later.

Behind a proxy the stream must not be buffered and needs a long read timeout
([monitor.md](monitor.md#behind-a-reverse-proxy), `deploy/proxy/`).
