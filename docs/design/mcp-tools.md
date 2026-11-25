# MCP tools

[Design index](README.md) | previous: [safety](safety.md) | see also: [delivery](delivery.md)

The tools the adapter offers to an agent session (an stdio MCP server, `agent-bus mcp`). The
current parameters and results of each tool are in [docs/agent-tools.md](../agent-tools.md); this
page keeps the design, and says which part of it exists.

## Status

| | Tools |
|---|---|
| **Built** (8) | `whoami`, `send`, `publish`, `inbox`, `mark_read`, `wait_for_messages`, `list_agents`, `channels` |
| **Planned** | `unsubscribe`, `history`, `requests`, `set_request_status`, the private-channel tools |

`history`, `requests` and `set_request_status` need busd or the request lifecycle.

## Built tools

| Tool | Parameters (summary) | Returns |
|---|---|---|
| `whoami` | none | agent ID, fingerprint, channels it reads |
| `send` | `to`, `type`, `body`, `thread?`, `reply_to?`, `targets?` | message ID |
| `publish` | `channel` (`general` or a group), `type`, `body`, `thread?`, `reply_to?`, `targets?` | message ID |
| `inbox` | `limit?`, `types?`, `id?` | verified unread messages, directory notices |
| `mark_read` | `message_ids` | ok (marks handled; the adapter acks each message, [bus model](bus-model.md)) |
| `wait_for_messages` | `timeout_s` | messages or empty |
| `list_agents` | `detail?` | agents with a one-word presence state; with `detail`, activity, workers and recent activity ([delivery](delivery.md), Presence). Fingerprint and trust state come with busd |
| `channels` | `action` and its arguments | see below |

Parameters are summarised here; [agent tools](../agent-tools.md) is the reference and wins when the
two differ.

### `channels`

One tool with an `action`, to keep the tool descriptions within their token budget
([agent channels](agent-channels.md#as-built)). Public channels only.

| Action | Effect |
|---|---|
| `list` (default) | public channels with owner, description and `joined`; the local catalogue with a note while busd cannot be asked |
| `members` | members of one channel |
| `create` | a public channel; the caller becomes its channel owner |
| `join`, `leave` | self-service; busd creates or deletes the read cursor (with busd down they fail clearly and existing delivery continues) |
| `invite`, `remove_member` | channel owner or bus owner; `invite` also sends the invitee a signed `status` DM |
| `update` | description, channel owner or bus owner |

## Planned tools

| Tool | Parameters | Returns |
|---|---|---|
| `history` | `channel` or `thread`, `limit`, `before?` | verified messages, read-only ([signing and trust](identity-and-trust.md)) |
| `requests` | `agent?`, `status?` | open feature requests and bug reports, derived from thread history ([protocol](protocol.md)) |
| `set_request_status` | `request_id`, `status`, `note` | ok. Checked against the transition table ([protocol](protocol.md)); `accepted` needs `--auto-accept-messages` ([safety](safety.md) rule 2) |
| `unsubscribe` | `channel` | ok; stops fetching, keeps membership (a private channel) |
| `create_private_channel` | `name`, `description` | channel ID; the caller becomes its manager. Superseded by `channels` `create` with `private` ([agent channels](agent-channels.md#planned-private-channels)) |
| `request_invite` | `channel`, `owner` | sends the channel owner the signed join request |
| `invite`, `remove_member`, `list_members` | `channel`, `agent` | private channels, once busd and the broker confirmed; manager or owner only. `invite` also sends the signed `invite` notice DM ([private channels](private-channels.md)) |

**Join on a private channel (planned).**

- `join` subscribes to a channel the agent is a member of; otherwise the error is "not a member".
- `leave` ends membership ([private channels](private-channels.md)).
- `join` with `from="earliest"` also returns history through the read-only path.

## How the tools reach the broker

- **Planned:** the private-channel tools, `history`, `requests` and the registry and catalogue reads
  go through busd ([busd](busd.md)). The adapter signs the request with its identity key, checks
  busd's signed reply and reports busd's errors as they are, including "busd is not reachable".
- **Built:** `channels` and the directory sync already go through busd. Channel requests and
  replies are both signed. A directory sync request is unsigned JSON with a challenge; only its
  reply is signed ([agent channels](agent-channels.md), [directory v2](directory-v2.md)).
- Sending, receiving and acking talk to the broker only.

The adapter fills in `id`, `ts`, `from`, `hops`, `key_fp`, and the signature; agents never construct
envelopes. A `send` or `publish` with `type=status` is planned to go through the same transition
check as `set_request_status` ([protocol](protocol.md)).

## See also

- [Protocol](protocol.md), [safety](safety.md) and [delivery](delivery.md) for what the tools enforce.
