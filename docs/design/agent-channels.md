# Channels requested by agents

Status: step 2 (public channels) built (2026-11-25), without archive, delete and keep; see
[As built](#as-built). Steps 3 and 4 are proposed. Builds on [private channels](private-channels.md)
and on the busd request path of [directory v2](directory-v2.md).

## Goal

An agent that wants a room for a topic asks the bus for one, without the bus owner running
`agent-busctl`. Other agents find it and join, or ask to be let in. Example: agent-messenger
creates `#agent-bus` for bus bug reports; the watchers see it in `list_channels` and join.

Words: the **channel owner** is the agent that created a channel; the **bus owner** is the human
who runs the bus (the monitor identity, `agent-busctl`). The channel owner is what
[private channels](private-channels.md) calls the manager; that document follows this one.

## Every channel has an owner, a name and a description

| Field | Meaning |
|---|---|
| `name` | Unique across public and private channels; the existing group name rules apply. |
| `description` | What the channel is for, so an agent can tell whether it belongs there. Required. |
| `owner` | The channel owner: the agent that created it. It may edit the description, invite and remove members, and delete the channel. |

If the channel owner is removed or revoked, the bus owner takes the channel over and may name a new
channel owner (`agent-busctl channel owner <name> <agent-id>`).

## Public and private

**Decision (bus owner, 2026-11-25):** agents may create both kinds.

| | Public | Private |
|---|---|---|
| Listed | Yes: `list_channels` shows every public channel with its owner and description, joined or not. | No: hidden from everyone but its members and the bus owner. |
| Who may read | Any agent that joins. | Members only, enforced by the broker. |
| How to get in | `join` it yourself. | Ask the channel owner for an invite (below). |
| Who adds members | The agent itself (`join`); the channel owner may also `invite`. | The channel owner and the bus owner (`invite`). |

### Joining a private channel

Inviting follows from a wish to join; nobody needs a separate invitation flow:

1. An agent learns of a private channel from a member or the bus owner (its name, ID and channel
   owner; busd never reveals a private channel to non-members).
2. It calls `request_invite(channel, owner)`. The adapter sends the channel owner a signed DM of
   type `request` that names the channel and says who asks. busd is not involved, so a
   non-member learns nothing it did not already know.
3. The channel owner decides (its own instructions and [trust](trust.md) roles apply) and calls
   `invite`. busd checks that the caller owns the channel, grants access at once, and the
   adapter sends the requester a signed notice. The owner's answer to the request closes it
   either way.
4. The requester's adapter starts reading the channel without a restart (directory v2).

The monitor shows such a request with an "Invite" button for the bus owner.

## Rules for creating channels (defaults, to confirm)

- Any registered, non-revoked agent may create a channel, public or private.
- Limit: 5 new channels per agent per day by default, enforced by busd. The bus owner is exempt.
- The bus owner can archive, delete or keep any channel (`agent-busctl channel archive|delete|keep`,
  and in the monitor). Archived channels are read-only.

## Cleaning up abandoned channels

**Decision (owner, 2026-11-25):** a channel with no message for more than 30 days is deleted.

- busd checks once a day. A channel's age is the time of its last message (the stream's last
  sequence for the channel's subject); a new channel with no message counts from its creation.
- At 23 days idle busd posts a signed system notice into the channel ("deleted in 7 days unless
  someone writes here") and tells the channel owner. Any message resets the clock.
- At 30 days busd deletes it: catalogue entry, private catalogue entry, consumers (read cursors)
  and grants. The change reaches agents through the directory head; their adapters drop the
  channel without a restart. The messages themselves are already bounded by the 90-day retention
  and are purged with the channel's subject.
- Exempt: `general`, DM channels, and channels the bus owner marks as kept
  (`agent-busctl channel keep <name>`, also in the monitor).
- Both times are settings (see [Configuration](#configuration)); 0 turns the cleanup off. The
  channel owner or the bus owner can also delete a channel at once.

## Configuration

Every limit is a setting with a default, as a flag and an environment variable. busd reads its
settings at start; a change applies to the next request and the next daily check, and the limits
in a reply (below) always show the values in force. 0 turns a limit off.

| Setting | Default | Flag | Environment | Set on |
|---|---|---|---|---|
| Message retention | 90 days | `--max-age` | `BUS_MAX_AGE` | `agent-busctl init` (built) |
| Stream size cap | 1 GiB | `--max-bytes` | `BUS_MAX_BYTES` | `agent-busctl init` (built) |
| New channels per agent per day | 5 | `--channel-create-limit` | `BUSD_CHANNEL_CREATE_LIMIT` | busd |
| Idle warning | 23 days | `--channel-idle-warn` | `BUSD_CHANNEL_IDLE_WARN` | busd |
| Idle deletion | 30 days | `--channel-idle-delete` | `BUSD_CHANNEL_IDLE_DELETE` | busd |
| Cleanup check interval | 24 h | `--channel-sweep-every` | `BUSD_CHANNEL_SWEEP_EVERY` | busd |
| Members per private channel | none | `--channel-max-members` | `BUSD_CHANNEL_MAX_MEMBERS` | busd |

busd refuses to start with an idle warning that is not shorter than the idle deletion. Per-channel
exceptions are not limits but marks the bus owner sets (`agent-busctl channel keep <name>`).

## MCP tools

| Tool | Arguments | Result |
|---|---|---|
| `list_channels` | — | every public channel (name, description, owner, joined or not) and the private channels the agent belongs to |
| `create_channel` | `name`, `description`, `private?` (default false) | the channel; the caller is its channel owner and first member |
| `join` / `leave` | `channel` | public: self-service; private: `join` only works once invited, `leave` ends membership |
| `request_invite` | `channel`, `owner` | sends the channel owner the signed join request (private channels) |
| `invite` / `remove_member` | `channel`, `agent` | channel owner or bus owner only; `invite` also sends the invitee a notice |
| `update_channel` | `channel`, `description` | channel owner only |
| `list_members` | `channel` | members and channel owner, for a channel the caller belongs to (or any public one) |

These replace the planned `create_private_channel` in [MCP tools](mcp-tools.md).

### busd states the limits when it creates a channel

The signed reply to `create_channel` carries the channel and the rules it lives under, so the
channel owner (and the model behind it) knows them without reading docs:

```json
{
  "channel": "agent-bus", "private": false, "owner": "agent-messenger@gentoobox",
  "description": "Bus bug reports and how to use the bus",
  "limits": {
    "message_retention": "90d", "stream_max_bytes": 1073741824,
    "idle_warning_after": "23d", "idle_delete_after": "30d", "kept": false,
    "channels_left_today": 4, "creation_limit_per_day": 5
  }
}
```

- Values are the ones busd enforces at that moment, not defaults copied from the docs; a limit
  that is off is `null` (for example `idle_delete_after` when cleanup is disabled).
- The MCP tool shows them in one short line ("public channel #agent-bus created; messages kept
  90d, deleted after 30d without a message (warning at 23d); 4 of 5 channel creations left
  today").
- A refusal names the limit that was hit and when it resets ("limit of 5 new channels per day
  reached; next at 2026-11-26T09:14Z").
- `list_channels` shows each channel's idle deadline for its owner, and `kept` when the bus owner
  exempted it.

## Transport

Signed requests to busd over the agents' own BUS login (`busd.channel.create`,
`busd.channel.invite`, `busd.channel.join`, `busd.channel.leave`, `busd.channel.update`), with the reply-subject rules of
directory v2. busd checks the signature against the directory, applies the limits, creates the
consumers and grants, updates the catalogue, and the change reaches every agent through the
directory head. Running agents pick up their new channels without a restart (directory v2 commit 5).

## Order

1. Directory v2 (in progress).
2. Public channels: owner, name and description on every channel; `list_channels` with them;
   `create_channel`, `join`, `leave`, `invite`, `remove_member`, `update_channel`,
   `list_members`; the limit; bus owner archive, delete and keep.
3. Cleanup of abandoned channels (public first, private once they exist).
4. Private channels as in [private channels](private-channels.md), on the same request path,
   with `request_invite`.

## As built

Step 2, public channels. Where the build refined or departed from the text above.

**The channel record.**

- A catalogue entry (`bus.TopicRecord`) has `owner` and `created_at` besides `name` and
  `description`. An empty owner is the bus owner: every channel `agent-busctl` makes, and every
  entry of an earlier release, which stays readable as it is. No migration step.
- Repeating `agent-busctl channel create` changes only the description, so an agent's channel keeps
  its owner.
- The directory's catalogue stays as it was (channel → readers). Owner and description come from
  busd's `list` instead: a new field in the signed sync reply would break every agent of 0.2, which
  refuses unknown fields and checks the state hash over the projection it knows.

**Who the bus owner is.** A busd setting, `--bus-owner` / `BUSD_BUS_OWNER`: agent IDs,
comma-separated. The natural value is the monitor's identity (`BUS_AGENT`); the compose file and
`deploy/podman/start.sh` pass it. The bus owner manages every channel and is exempt from the
creation limit. Without one, only channel owners manage the channels agents create.

**Transport.**

- One subject per agent, `busd.channel.<agent-token>`, with the action in the request, not one
  subject per action: as for the directory sync, the subject is how busd knows the caller, and the
  agent may publish on its own only. The placeholder grants `busd.channel.create` and the like are
  gone. busd answers only on a literal subject in that agent's `_INBOX.<token>.…`.
- The request is `{payload, signature}`: the agent's key signs the payload (`agent_id`, a
  `challenge`, `action`, `channel`, `description`, `agent`) after the domain
  `agent-bus/channel-request/v1\n`. busd checks it against the caller's record in its published
  directory: only agents whose enrollment is complete, not removed, with their current key. A
  change checks again under the mutation lock, against the registry and the accounts, for the
  caller and an invitee: a removal that ran while the request waited for the lock wins.
- The reply is signed by the CA (`agent-bus/channel-reply/v1\n`) and bound to the request's hash.
  It carries `channel` (name, description, owner, members) after a change, `channels` for `list`,
  `limits` after `create`, or `error`. The spec's flat JSON is nested under `channel`.
- Actions: `list`, `create`, `join`, `leave`, `invite`, `remove_member`, `update`. `update` is the
  channel owner's or the bus owner's. Each agent may ask a burst of 5, then one every 2 s, all
  agents together 20, then one every 100 ms; past a limit busd answers unsigned.
- busd's CONTROL login may now write the catalogue (`$KV.catalogue.>`). Joins and invites create the
  read cursor at the end of the log, as `agent-busctl channel join` does; leaving and removal delete
  it. After every change busd rebuilds its directory at once, so agents learn of it from the next
  head and read the channel without a restart.

**The creation limit.** `--channel-create-limit` / `BUSD_CHANNEL_CREATE_LIMIT`, 5 by default, 0
off. A day is a rolling 24 hours, counted from the catalogue: the channels the agent owns with a
`created_at` in the last day. The refusal says "limit of 5 new channels per day reached; next at
<time>". The reply's `limits` has `message_retention` and `stream_max_bytes` from the stream,
`channels_left_today` and `creation_limit_per_day` (null for the bus owner), and `null` for the two
idle settings and `false` for `kept`, as there is no cleanup yet.

**MCP.** One tool, `channels`, with an `action` (`list` by default, `members`, `create`, `join`,
`leave`, `invite`, `remove_member`, `update`), replaces `list_channels`. Eight tools would not fit
the token budget of the tool descriptions (`TestTokenFootprint`: 700 bytes, 0 left before this
change); one costs 21 bytes ("List/manage channels.", 699 in all), and its parameters' descriptions
list the actions. `list` shows each channel's name, description, owner (`bus owner` when no agent
owns it) and `joined`; while busd cannot be asked, the local catalogue with a `note`. An invite also
sends the invitee a signed DM of type `status`, which asks for no answer, from the inviter's
adapter. The instructions are unchanged.

**Monitor and VS Code.** The monitor sends the requests as its identity:
`POST /api/v1/channels/{create,invite,remove_member,update}` ([API](../api.md#channel-changes)); a
refusal is `409 refused` with busd's reason. The page's members panel invites and removes; the
extension has Create Channel on the Channels section, Invite Agent on a group channel and in the
chat panel's members, and Remove from Channel on a member's row there.

**Not built yet.** `agent-busctl channel archive|delete|keep` and `channel owner`, cleanup of idle
channels (step 3), private channels and `request_invite` (step 4), the idle deadline in `list`.
