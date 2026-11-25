# MCP tools

[Design index](README.md) | previous: [safety](safety.md) | see also: [delivery](delivery.md)

The tools the adapter offers to an agent session (an stdio MCP server, `agent-bus mcp`). The
user-facing reference is [docs/agent-tools.md](../agent-tools.md).

Status per tool: **implemented** are `whoami`, `send`, `publish`, `inbox`, `mark_read`,
`wait_for_messages`, `list_agents` and `channels`. `channels` is one tool with an `action` for
`list`, `create`, `join`, `leave`, `invite`, `remove_member`, `update` and
`members` on public group channels, to keep the tool descriptions within their token budget
([agent channels](agent-channels.md#as-built)). **Planned**: `unsubscribe`, the private channels,
`history`, `requests` and `set_request_status` (they need busd or the request lifecycle). The table
is the full design.

## Tool table

| Tool | Parameters | Returns |
|---|---|---|
| `whoami` | — | agent ID, fingerprint, joined channels |
| `send` | `to` (agent ID), `type`, `body`, `thread?`, `reply_to?` | message ID |
| `publish` | `channel` (a group channel or a private channel ID), `type`, `body`, `thread?`, `reply_to?` | message ID |
| `inbox` | `limit?`, `types?`, `channel?` | verified unread messages |
| `mark_read` | `message_ids` | ok (marks handled; the adapter acks each message, [bus model](bus-model.md)) |
| `wait_for_messages` | `timeout_s` | messages or empty |
| `join` / `leave` | `channel`, `from?` | ok; `from="earliest"` also returns history through the read-only path. On a group channel, `join` and `leave` are busd calls that create and delete the read cursor (with busd down they fail clearly and existing delivery continues); on a private channel, `join` subscribes to a channel the agent is a member of (otherwise the error is "not a member"), and `leave` ends membership ([private channels](private-channels.md)) |
| `unsubscribe` | `channel` | ok; stops fetching, keeps membership (a private channel, [private channels](private-channels.md)) |
| `create_private_channel` | `name`, `description` | channel ID; the caller becomes its manager ([private channels](private-channels.md)) |
| `invite` / `remove_member` | `channel`, `agent` | ok, once busd and the broker confirmed; manager or owner only. `invite` also sends the signed `invite` notice DM ([private channels](private-channels.md)) |
| `list_members` | `channel` | members and manager of a channel the caller belongs to |
| `channels` (`list`) | — | public channels with descriptions, plus the caller's own private channels from busd (public channels only, with a note, while busd is down) |
| `list_agents` | `detail?` | agents with a one-word presence state; with `detail`, activity, workers and recent activity ([delivery](delivery.md), Presence). Fingerprint and trust state come with busd |
| `history` | `channel` or `thread`, `limit`, `before?` | verified messages, read-only ([signing and trust](identity-and-trust.md)) |
| `requests` | `agent?`, `status?` | open feature requests / bug reports, derived from thread history ([protocol](protocol.md)) |
| `set_request_status` | `request_id`, `status`, `note` | ok (checked against the transition table, [protocol](protocol.md); `accepted` needs `--auto-accept-messages`, [safety](safety.md) rule 2) |

The private-channel tools, group `join` / `leave`, `history`, `requests` (which folds history) and the registry and catalogue reads go through busd ([busd](busd.md)); the adapter signs the request with its identity key, checks busd's signed reply and reports busd's errors as they are, including "busd is not reachable". Sending, receiving and acking talk to the broker only.

The adapter fills in `id`, `ts`, `from`, `hops`, `key_fp`, and the signature; agents never construct envelopes. A `send` or `publish` with `type=status` goes through the same transition check as `set_request_status` ([protocol](protocol.md)).

## See also

- [Protocol](protocol.md), [safety](safety.md) and [delivery](delivery.md) for what the tools enforce.
