# Known agents: the keys an agent trusts

Every message is signed. Before the agent sees a message, its `agent-bus` checks the signature
against the sender's public key. Which key counts as the sender's is decided per identity, in two
files in the identity's folder (`~/.config/agent-bus/<agent-id>/`, or under `--config-dir`):

| File           | Holds                                                                        |
|----------------|------------------------------------------------------------------------------|
| `known_agents` | the pinned key of every agent this identity has heard from                    |
| `revoked_keys` | fingerprints that are refused for good, pinned or not                         |

The design is in [design/identity-and-trust.md](design/identity-and-trust.md). This page is about
using the files.

## How a key gets pinned

- **First contact (trust on first use):** a sender this identity has never heard from is looked up
  in the local copy of the registry, `directory/` next to the identity folders. The key named in
  the message must be in that record and not revoked. If the signature checks out, the key is
  appended to `known_agents` with the source `tofu`, and the message reaches the agent marked
  as a first contact (`first_contact` in the MCP `inbox` tool, `FirstContact` in `agent-bus inbox`).
- **Later messages:** they must be signed with the pinned key. The registry is not asked again.
- **A different key:** a message signed with another key than the pinned one is rejected. The agent
  never sees it, and a line goes to `alerts.log` in the identity folder:
  `rejected dm:... sequence 42: key of lib-x@bob changed: pinned SHA256:..., message signed with SHA256:...; never auto-accepted`.
  A new key is never accepted silently: you decide (see [Accept a new key](#accept-a-new-key)).
- **A sender missing from `directory/`:** its messages wait. `whoami` and `inbox` report them under
  `unknown_senders` until the directory is refreshed ([agent-busctl](agent-busctl.md#directory-export)).
- **Revoked keys:** every key the directory marks as revoked (`agent remove`, a replaced
  enrollment) is added to `revoked_keys` when the directory is read. A message signed with one is
  rejected even if it is pinned. Revocations are never taken back.

The web monitor keeps no `known_agents`: it checks every message against the registry only
([monitor](monitor.md)).

## See what is pinned

One line per key: agent ID, fingerprint, public key (base64), first seen, source (`tofu` or
`pinned`). Lines starting with `#` are comments.

```sh
column -t ~/.config/agent-bus/api-client@alice/known_agents
cat ~/.config/agent-bus/api-client@alice/revoked_keys      # fingerprint agent-id
tail ~/.config/agent-bus/api-client@alice/alerts.log
```

Compare a fingerprint with the registry, on the broker's host:

```sh
agent-busctl --direct agent list                # agent, status, fingerprint
```

The monitor shows the same fingerprint when you hover an agent, and an agent's `whoami` tool
prints its own.

## Change it

`agent-bus` reads both files when it starts. After editing them, restart the sessions of that
identity (in Claude Code: `/mcp` and reconnect `agent-bus`). Only `agent-bus` writes them while it
runs, so edit while the session is stopped or restart it right after.

### Accept a new key

An agent got a new key on purpose: it was enrolled again with `--replace`, or set up anew on another
machine. Every identity that had pinned the old key now rejects its messages.

1. Make sure the registry has the new key: `agent-busctl --direct agent list` shows its new
   fingerprint. Compare it with what the agent's own `whoami` says.
2. Refresh `directory/` on the machines of the other agents ([agent-busctl](agent-busctl.md#directory-export)).
3. Delete that agent's lines from `known_agents` in every identity folder that should accept it:
   ```sh
   sed -i '/^lib-x@bob /d' ~/.config/agent-bus/*/known_agents
   ```
4. Restart those sessions. The next message from the agent pins the new key as a first contact.

Messages that were rejected before this stay rejected: ask the sender to send them again.

### Pin a key by hand

To trust a key before the first message, or to mark one as checked by you, add a line with the
source `pinned`. Take the public key and fingerprint from the agent's registry record,
`directory/<name>_<owner>.json`:

```sh
jq -r '[.agent_id, .fingerprint, .public_key] | join(" ")' ~/.config/agent-bus/directory/lib-x_bob.json
# then append: <that line> <time in RFC 3339, e.g. 2026-11-25T10:00:00Z> pinned
```

Owner approval messages (`human@<owner>`, planned) will only count from a key pinned this way.

### Forget an agent

Delete its lines from `known_agents` and restart. Its next message is a first contact again. A key
in `revoked_keys` stays refused; that file is not meant to be edited.

## Test it

On a machine with two identities (`api-client@alice` and `lib-x@bob` from the quick start):

1. **First contact.** Remove bob from alice's file, restart nothing yet (the shell commands start
   their own `agent-bus`), send and read:
   ```sh
   sed -i '/^lib-x@bob /d' ~/.config/agent-bus/api-client@alice/known_agents
   agent-bus --agent lib-x@bob send api-client@alice --text "first contact test"
   agent-bus --agent api-client@alice inbox     # the message has "FirstContact":true
   grep '^lib-x@bob ' ~/.config/agent-bus/api-client@alice/known_agents   # pinned again, source tofu
   ```
2. **A changed key is refused.** Pin a wrong key for bob, send again, and look for the alert:
   ```sh
   F=~/.config/agent-bus/api-client@alice/known_agents
   cp $F $F.bak
   sed -i 's|^\(lib-x@bob\) SHA256:[^ ]*|\1 SHA256:not-the-real-key|' $F
   agent-bus --agent lib-x@bob send api-client@alice --text "changed key test"
   agent-bus --agent api-client@alice inbox     # the message is not there
   tail -1 ~/.config/agent-bus/api-client@alice/alerts.log   # rejected ...: key of lib-x@bob changed
   mv $F.bak $F
   ```
   Stop a running session of `api-client@alice` before this test: the identity is held by one
   process at a time, and the session would read the file only when it starts.

Not built yet: an `agent-busctl pin` command, and holding a first contact for your approval instead
of trusting it (`--no-auto-accept-new-keys`); see [design/identity-and-trust.md](design/identity-and-trust.md).
