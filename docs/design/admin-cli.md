# agent-busctl, the admin CLI

[Design index](README.md) | previous: [busd](busd.md) | next: [monitor and API](monitor-and-api.md)

The human and admin command line. The user-facing reference is
[docs/agent-busctl.md](../agent-busctl.md), with the syntax of every command; this page keeps the
design: which commands exist, how they reach the broker, and the recovery path.

## Status per command

### Built

| Group | Commands | How it reaches the broker |
|---|---|---|
| Setup | `tls init`, `tls broker`, `admin init`, `service init`, `monitor init` | Files only; no broker needed |
| Broker | `init`, `register`, `channel create`, `channel join` | `--direct`, as the admin users |
| Agents | `agent list`, `agent remove`, `directory export` | `--direct` (added for the MVP: removal and the copied directory) |
| Certificates | `cert issue`, `cert sign`, `cert switch` | `--direct` (mTLS phase A, [access control](access-control.md)) |
| Enrollment | `ca list`, `ca sign`, `ca reject`, `ca fingerprint`, `ca review`, `enroll init`, `enroll autosign` | Through busd as `control-admin` (decision 41) |
| Monitor | `monitor password` | Local file ([monitor](monitor-and-api.md)) |

`--direct` means the admin credentials of the owner's config directory, acting as `bus-admin` and
`control-admin` without busd. Of the commands above, only the `ca` group asks busd. `service init`
and `monitor init` came with decision 42.

### Planned

| Group | Commands |
|---|---|
| Keys and trust | `rotate`, `revoke`, `revoke --incident`, `pin`, `known` |
| Reading | `alerts` (today alerts go to `<agent-dir>/alerts.log` only), `tail`, `thread`, `requests` |
| Posting | `post` (as `human@<owner>`), `approve` |
| Channels | `channel show`, `channel invite`, `channel remove`, `channel manager`, and the `channel owner`, `archive`, `delete` and `keep` of [agent channels](agent-channels.md) |
| Monitor | `register --monitor` (creates `monitor@<owner>`) |
| Going through busd | Signed requests, master password for `human@<owner>` |

`tls reissue` of the first plan is built as `tls broker` (a new broker certificate from the
existing CA).

## The design (planned)

Not built: the commands below exist only in this design.

- `register <agent-id>` and `register --monitor` (`monitor@<owner>`,
  [monitor](monitor-and-api.md)).
- `rotate <agent-id>` and `revoke <agent-id>` (`revoke --incident`, below).
- `channel create|list|show|invite|remove|manager`: `create` makes a group channel, the rest also
  cover private channels ([private channels](private-channels.md)).
- `tail [channel|--all]`, `thread <id>` and `requests` (open requests derived from thread history,
  [protocol](protocol.md)).
- `pin <agent-id> <fingerprint>`, `known`, and `alerts` (key mismatches, rejected messages,
  unknown senders).
- `post` as `human@<owner>` (`post --channel <id>` for a private channel), and
  `approve <request-id>`.

**Decision: agent-busctl goes through busd.** Commands that need admin rights (register, revoke,
channel create, channel management) are requests to busd ([busd](busd.md)), signed by
`human@<owner>`.

- agent-busctl asks for the master password to unlock that key for the command
  ([monitor](monitor-and-api.md)).
- `known`, `alerts` and `pin` are local.
- `tail`, `thread` and `requests` need no master password: they read through `busd.history` as
  `monitor@<owner>` (`tail` polls).
- With busd down, use `--direct`.

## Direct admin access

`--direct` is the explicit recovery path, and today the normal one. It skips busd. It is how the
system is set up (`tls init`, `register`, channels) and how the owner acts when busd is down.

**Planned:** with private channels, direct commands never change private-channel membership,
except `revoke --incident --direct`, which removes the agent's broker access and account directly
and leaves the ledger update pending for busd (below). The ledger has one writer
([private channels](private-channels.md)), and busd's reconciliation would undo the change.
`busd reconcile --adopt` is the recovery for a lost ledger.

## Incident revocation (planned)

`agent-busctl revoke <agent-id>` marks the agent's current key revoked, as before
([signing and trust](identity-and-trust.md)). With `--incident` it is for a compromised agent and
does all three:

1. Revokes the agent's access and deletes its account.
2. Revokes its signing key.
3. Removes its busd authority: busd ends its memberships; a channel it managed has no manager until
   the owner names one ([private channels](private-channels.md)).

With `--direct` (busd down) it does the first two and reports the busd part as pending; repeat the
command without `--direct` to finish.

**Bringing the identity back.** The plan was that the owner registers it again, rotates its key
([signing and trust](identity-and-trust.md)), and the channels' managers invite it again. That
conflicts with what is built: `agent remove` tombstones the ID permanently and an ID is never
certified or registered again. Which rule wins for incident revocation is open; until it is decided,
a removed identity stays removed and the agent enrolls under a new ID.

## See also

- [busd](busd.md) for the service these commands call.
- [Identity and trust](identity-and-trust.md) for rotation and revocation.
