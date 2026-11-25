# agent-busctl, the admin CLI

[Design index](README.md) | previous: [busd](busd.md) | next: [monitor and API](monitor-and-api.md)

The human and admin command line. The user-facing reference is
[docs/agent-busctl.md](../agent-busctl.md); this page keeps the design: which commands exist, how
they reach the broker, and the recovery path.

## Status per command

| Command (design) | Status |
|---|---|
| `tls init`, `admin init`, `init`, `register`, `channel create`, `channel join` | implemented, always `--direct` |
| `agent list`, `agent remove`, `directory export` | implemented (added for the MVP: removal and the copied directory) |
| `cert issue`, `cert sign`, `cert switch` | implemented (mTLS phase A, [access control](access-control.md)) |
| `monitor password` | implemented ([monitor](monitor-and-api.md)) |
| `rotate`, `revoke`, `revoke --incident`, `pin`, `known`, `alerts`, `post`, `approve`, `tail`, `thread`, `requests`, `tls reissue`, `register --monitor`, `channel show\|invite\|remove\|manager` | **planned**; alerts go to `<agent-dir>/alerts.log` only |
| Going through busd (signed requests, master password for `human@<owner>`) | **planned**; every command runs as `--direct` today |

## The design

`register <agent-id>` (`register --monitor` creates `monitor@<owner>`, [monitor](monitor-and-api.md)), `rotate <agent-id>`, `revoke <agent-id>` (`revoke --incident`, below), `channel create|list|show|invite|remove|manager` (`create` makes a group channel; the rest also cover private channels, [private channels](private-channels.md)), `tls init` (also creates busd's service signing key and copies its public key next to the CA), `tls reissue` (a new broker certificate from the existing CA, for an expired certificate or a moved host; `tls init --force` replaces the CA too), `tail [channel|--all]`, `thread <id>`, `requests` (open requests derived from thread history, [protocol](protocol.md)), `pin <agent-id> <fingerprint>`, `known`, `alerts` (key mismatches, rejected messages, unknown senders), `post` (as `human@<owner>`; `post --channel <id>` for a private channel), `approve <request-id>`.

**Decision: agent-busctl goes through busd.** Commands that need admin rights (register, revoke, channel create, channel management) are requests to busd ([busd](busd.md)), signed by `human@<owner>`; agent-busctl asks for the master password to unlock that key for the command ([monitor](monitor-and-api.md)). `known`, `alerts` and `pin` are local. `tail`, `thread` and `requests` need no master password: they read through `busd.history` as `monitor@<owner>` (`tail` polls). With busd down, use `--direct`.

## Direct admin access

**Direct admin access is the explicit recovery path.** `--direct` (admin credentials from the owner's config) skips busd. It is how the system is set up (`tls init`, the first `register` call for `human@<owner>`, channels) and how the owner acts when busd is down. Direct commands never change private-channel membership, except `revoke --incident --direct`, which removes the agent's broker access and account directly and leaves the ledger update pending for busd (below): the ledger has one writer ([private channels](private-channels.md)), and busd's reconciliation would undo the change. `busd reconcile --adopt` ([private channels](private-channels.md)) is the recovery for a lost ledger.

## Incident revocation

**Incident revocation.** `agent-busctl revoke <agent-id>` marks the agent's current key revoked, as before ([signing and trust](identity-and-trust.md)). `agent-busctl revoke <agent-id> --incident` is for a compromised agent and does all three: it revokes the agent's access and deletes its account, revokes its signing key, and removes its busd authority (busd ends its memberships; a channel it managed has no manager until the owner names one, [private channels](private-channels.md)). With `--direct` (busd down) it does the first two and reports the busd part as pending; repeat the command without `--direct` to finish. To bring the identity back, the owner registers it again, rotates its key ([signing and trust](identity-and-trust.md)), and the channels' managers invite it again.

## See also

- [busd](busd.md) for the service these commands call.
- [Identity and trust](identity-and-trust.md) for rotation and revocation.
