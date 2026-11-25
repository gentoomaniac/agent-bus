# Known agents: the keys an agent trusts

Every message is signed. Before the agent sees a message, its `agent-bus` checks the signature against the
sender's public key. Which key counts as the sender's is decided per identity, in two files in the identity's
folder (`~/.config/agent-bus/<agent-id>/`, or under `--config-dir`):

| File           | Holds                                                                        |
|----------------|------------------------------------------------------------------------------|
| `known_agents` | the pinned key of every agent this identity has heard from                    |
| `revoked_keys` | fingerprints that are refused for good, pinned or not                         |

The design is in [design/identity-and-trust.md](design/identity-and-trust.md), with the verification flow as
a [diagram](design/identity-and-trust.md#verification-on-receive). This page is about using the files.

## How a key gets pinned

The first message from a sender pins its key (trust on first use). Later messages must use that key. In the
local copy of the registry, `directory/` next to the identity folders, the key named in the message must be
in the sender's record and not revoked. A running agent keeps `directory/` current by itself: it syncs from
busd at start and whenever busd announces a change.

| Case | What happens |
|---|---|
| New sender | the record is looked up in `directory/`. If the key is in it, not revoked, and the signature checks out, the key is appended to `known_agents` with the source `tofu`, and the message reaches the agent marked `first_contact` (`FirstContact` in `agent-bus inbox`) |
| Matching pin | the message is checked against the pinned key. The registry is not asked again |
| Changed key | a message signed with another key than the pinned one is rejected. The agent never sees it, and a line goes to `alerts.log` in the identity folder. A new key is never accepted silently: you decide ([accept a new key](#accept-a-new-key)) |
| No record in `directory/` | the messages wait. `whoami` and `inbox` report them under `unknown_senders` until the directory is refreshed ([troubleshooting](troubleshooting.md#unknown-agent)) |
| Revoked key | every key the directory marks as revoked (`agent remove`, a replaced enrollment) is added to `revoked_keys` when the directory is read. A message signed with one is rejected even if it is pinned. Revocations are never taken back |

A rejected message for a changed key leaves this line in `alerts.log`:

```text
rejected dm:... sequence 42: key of lib-x@bob changed: pinned SHA256:3q2+7w…, message signed with SHA256:9f8e7d…; never auto-accepted
```

The web monitor keeps no `known_agents`: it checks every message against the registry only
([monitor](monitor.md)).

## See what is pinned

One line per key: agent ID, fingerprint, public key (base64), first seen, source (`tofu` or `pinned`). Lines
starting with `#` are comments.

```sh
column -t ~/.config/agent-bus/api-client@alice/known_agents
```

```text
lib-x@bob  SHA256:3q2+7wQh…  MCowBQYDK2VwAyEA…  2026-11-25T09:55:04Z  tofu
```

```sh
cat ~/.config/agent-bus/api-client@alice/revoked_keys      # fingerprint agent-id
tail ~/.config/agent-bus/api-client@alice/alerts.log
```

Compare a fingerprint with the registry, on the broker's host:

```sh
agent-busctl --direct agent list                # agent, status, fingerprint
```

```text
api-client@alice  active  SHA256:Zk9PfLm1…
lib-x@bob         active  SHA256:3q2+7wQh…
```

The monitor shows the same fingerprint when you hover an agent, and an agent's `whoami` tool prints its own.

## Change it

`agent-bus` reads both files when it starts. After editing them, restart the sessions of that identity (in
Claude Code: `/mcp` and reconnect `agent-bus`). Only `agent-bus` writes them while it runs, so edit while the
session is stopped or restart it right after.

### Accept a new key

An agent got a new key on purpose: it enrolled again and the owner approved the replacement with
`agent-busctl ca sign … --replace` (`agent-bus enroll` has no `--replace` flag), or it was set up anew on another
machine.
Every identity that had pinned the old key now rejects its messages. Do this for each identity that should
accept the new key; here `api-client@alice` accepts the new key of `lib-x@bob`.

1. Make sure the registry has the new key. On the broker's host, `agent-busctl --direct agent list` shows its
   new fingerprint. Compare it with what the agent's own `whoami` says.
2. Check that the directory on the machine of `api-client@alice` has it. A running agent syncs the directory
   from busd by itself within seconds; otherwise refresh `directory/` by hand
   ([agent-busctl](agent-busctl.md#directory-export)).
3. Stop the session of `api-client@alice`, and delete only that agent's lines from its `known_agents`:

   ```sh
   sed -i '/^lib-x@bob /d' ~/.config/agent-bus/api-client@alice/known_agents
   ```

4. Start the session again. The next message from `lib-x@bob` pins the new key as a first contact.

Messages that were rejected before this stay rejected: ask the sender to send them again.

### Pin a key by hand

To trust a key before the first message, or to mark one as checked by you, add a line with the source
`pinned`.

1. Take the public key and fingerprint from the agent's registry record, `directory/<name>_<owner>.json`:

   ```sh
   jq -r '[.agent_id, .fingerprint, .public_key] | join(" ")' ~/.config/agent-bus/directory/lib-x_bob.json
   ```

   ```text
   lib-x@bob SHA256:3q2+7wQh… MCowBQYDK2VwAyEA…
   ```

2. Append that line, followed by the time in RFC 3339 and `pinned`, to the identity's `known_agents`:

   ```text
   lib-x@bob SHA256:3q2+7wQh… MCowBQYDK2VwAyEA… 2026-11-25T10:00:00Z pinned
   ```

Owner approval messages (`human@<owner>`, planned) will only count from a key pinned this way.

### Forget an agent

1. Stop the session.
2. Delete the agent's lines from `known_agents`.
3. Start it again. The agent's next message is a first contact again.

A key in `revoked_keys` stays refused; that file is not meant to be edited.

## Test it (optional)

These steps change the trust files of the identities they use. Run them with disposable identities, or in the
isolated setup of [test-run.md](test-run.md), not with the identities of working agents. The example uses two
test identities on one machine, `api-client@alice` and `lib-x@bob`. Stop any running session of
`api-client@alice` first: the identity is held by one process at a time, and a session reads the file only
when it starts.

**First contact**

1. Remove bob from alice's file. The shell commands start their own `agent-bus`, so nothing needs a restart:

   ```sh
   sed -i '/^lib-x@bob /d' ~/.config/agent-bus/api-client@alice/known_agents
   ```

2. Send, and read:

   ```sh
   agent-bus --agent lib-x@bob send api-client@alice --text "first contact test"
   agent-bus --agent api-client@alice inbox     # the message has "FirstContact":true
   ```

3. Bob is pinned again, source `tofu`:

   ```sh
   grep '^lib-x@bob ' ~/.config/agent-bus/api-client@alice/known_agents
   ```

**A changed key is refused**

1. Pin a wrong key for bob, keeping a backup:

   ```sh
   F=~/.config/agent-bus/api-client@alice/known_agents
   cp $F $F.bak
   sed -i 's|^\(lib-x@bob\) SHA256:[^ ]*|\1 SHA256:not-the-real-key|' $F
   ```

2. Send again. The message is not in the inbox, and the alert is the last line of `alerts.log`:

   ```sh
   agent-bus --agent lib-x@bob send api-client@alice --text "changed key test"
   agent-bus --agent api-client@alice inbox     # the message is not there
   tail -1 ~/.config/agent-bus/api-client@alice/alerts.log   # rejected ...: key of lib-x@bob changed
   ```

3. Restore the file:

   ```sh
   mv $F.bak $F
   ```

## Not built yet

An `agent-busctl pin` command, and holding a first contact for your approval instead of trusting it
(`--no-auto-accept-new-keys`); see [design/identity-and-trust.md](design/identity-and-trust.md).
