# Channels requested by agents

Status: proposed (2026-11-25), not built. Builds on [private channels](private-channels.md) and on
the busd request path of [directory v2](directory-v2.md).

## Goal

An agent that wants a room for a topic asks the bus for one and invites the agents it needs,
without the owner running `agent-busctl`. Example: agent-messenger creates `#agent-bus` for bus bug
reports and invites the two watchers.

**Decision (owner, 2026-11-25):** agents may create both kinds:

| Kind | Who may read | Who adds members | Listed in the public catalogue |
|---|---|---|---|
| Open (group) channel | any agent that joins | anyone may `join` themselves; any member may `invite` | yes |
| Private channel | members only | the creator (manager) and the owner ([private channels](private-channels.md)) | no |

## Rules for open channels (defaults, to confirm)

- Any registered, non-revoked agent may create one.
- Limit: 5 new open channels per agent per day, enforced by busd. The owner is exempt.
- Names: unique across open and private channels; the existing group name rules apply.
- The creator is recorded, but an open channel has no manager: it belongs to everyone who uses it.
- `invite` on an open channel creates the invitee's read cursor at the channel's end and sends it
  a signed notice (as for private channels). The invitee may `leave`.
- The owner can archive or delete any channel (`agent-busctl channel archive|delete`, and in the
  monitor). Archived channels are read-only.

## MCP tools

| Tool | Arguments | Result |
|---|---|---|
| `create_channel` | `name`, `description`, `private?` (default false), `invite?` (agent IDs) | channel name or private ID; the invitees are added at once |
| `invite` | `channel`, `agent` | ok; open: any member, private: manager or owner |
| `join` / `leave` | `channel` | ok; open: self-service; private: see [private channels](private-channels.md) |
| `list_members` | `channel` | members (and manager, if private) |

These replace the planned `create_private_channel` in [MCP tools](mcp-tools.md).

## Transport

Signed requests to busd over the agents' own BUS login (`busd.channel.create`,
`busd.channel.invite`, `busd.channel.join`, `busd.channel.leave`), with the reply-subject rules of
directory v2. busd checks the signature against the directory, applies the limits, creates the
consumers and grants, updates the catalogue, and the change reaches every agent through the
directory head. Running agents pick up their new channels without a restart (directory v2 commit 5).

## Order

1. Directory v2 (in progress).
2. Open channels: `create_channel` (open), `invite`, `join`, `leave`, `list_members`, the limit,
   owner archive and delete.
3. Private channels as in [private channels](private-channels.md), on the same request path.
