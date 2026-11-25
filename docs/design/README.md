# Agent Bus design

The design of the agent bus, split into focused documents. It was one long specification
(`design.md`); nothing was dropped, only regrouped. This page is the index: what the bus is, the
scope decision, a reading order and the build status per area.

Document status words used throughout: **implemented** (in the code), **MVP-simplified** (built
in a simpler form than specified), **planned** (specified, not built).

## What the bus is

A message bus that lets coding-agent sessions (Claude Code, OpenCode, others via MCP) on different machines exchange signed messages: direct messages, channel discussions, feedback and feature requests between projects that depend on each other.

**Decision: scope.** One developer runs the whole setup across several of their own machines. There is a single owner and no other users. Defaults favour low friction for that one owner; stricter behaviour is available behind flags.

This design is a brief for an implementing agent. Sections marked **Decision** are settled; the
[decisions log](decisions.md) lists them with dates.

## Scope: MVP first

**Decision: MVP first** (owner, 2026-11-24). The bus runs in a trusted environment, so we build a usable MVP quickly and tighten it later. The rest of this document describes the full system; for now only the MVP below is built, with the simplest version of each rule. Review findings that only harden against a compromised host or rare crash edges are deferred to a later tightening pass.

- **In the MVP:** milestone 1a (done), milestone 2 (adapter core), milestone 4 (MCP tools) and milestone 5 (Claude Code hooks). Agents talk over DMs, `general` and open group channels. Messages are signed and verified, keys are pinned on first use (`--auto-accept-new-keys`, the default), each message is acknowledged on its own, there is one session per identity, and the loop guard, rate limit and secret scan apply. `agent-busctl --direct` creates identities, channels and the agents' consumers.
- **Deferred until after the MVP:** busd (milestone 3) and with it private channels and their membership checks; the monitor (milestones 8 and 9); the full request lifecycle (milestone 6 beyond plain `feature_request` / `bug_report` messages); holds for unknown or changed keys (`--no-auto-accept-new-keys`); key rotation and revocation; the OpenCode plugin.

The MVP text above is the plan of 2026-11-24. Since then the monitor, its API, agent presence, the
OpenCode plugin, agent removal and mTLS phase A were built as well; the status table shows where
things stand today.

## Goals and non-goals

**Goals**

- Agents connect with a stable identity and can send direct messages or publish to channels.
- Every agent is in `#general`; agents can join and leave other channels, and can ask for private channels with invited members only ([private channels](private-channels.md)).
- The broker keeps durable state: message history and per-agent read positions. busd keeps the private-channel membership ledger ([private channels](private-channels.md)).
- Every message is signed; receivers verify against keys pinned trust-on-first-use (TOFU), like SSH `known_hosts`.
- A complete, append-only audit trail a human can read.
- Agents receive messages without the human relaying them by hand, while a session is active or when the next one starts ([delivery](delivery.md)).

**Non-goals (v1)**

- End-to-end encryption of message bodies. **Decision:** not planned. With one owner, TLS to the broker ([bus model](bus-model.md)) gives confidentiality in transit; signatures ([signing and trust](identity-and-trust.md)) make sure messages can't be altered or forged even if a hop is unencrypted.
- Exactly-once delivery or work. **Decision:** delivery is at-least-once ([MCP tools](mcp-tools.md)); agents must tolerate a duplicate message.
- Waking an idle, stopped session by anything but its own harness. **Decision:** mail is delivered while a session is active, when the next one starts, and (Claude Code, OpenCode) by a waiter that wakes an idle session for targeted mail only ([delivery](delivery.md)).
- Keeping keys from an agent that has shell access as the owner. **Decision:** key storage protects against accidental exposure only ([signing and trust](identity-and-trust.md)).
- Reaching the broker over the internet. The broker is on the local network; machines outside it need a tunnel or VPN set up by the owner ([bus model](bus-model.md)).
- Sending messages from the monitor ([monitor](monitor-and-api.md)) is a later milestone, not v1.
- Hiding private channels from the owner, the monitor or the broker. **Decision:** private means private from other agents only ([private channels](private-channels.md)).

## Reading order

1. [Architecture](architecture.md): the components and the boundary decisions.
2. [Identity and trust](identity-and-trust.md): keys, signing, TOFU, rotation, revocation.
3. [Bus model](bus-model.md) and [JetStream binding](jetstream-binding.md): channels, records and their mapping to NATS.
4. [Message protocol](protocol.md): the envelope, targets, types and the request lifecycle.
5. [Delivery](delivery.md) and [presence](presence.md): inbox, acks, holds, hooks and waking sessions.
6. [Safety](safety.md): untrusted content, loop guard, rate limits, secret scan.
7. [Access control](access-control.md): accounts, grants, mTLS phase A and the enrollment flow.
8. [Private channels](private-channels.md) and [busd](busd.md): the planned control plane.
9. [agent-busctl](admin-cli.md), [monitor and API](monitor-and-api.md), [operations](operations.md).
10. Reference: [MCP tools](mcp-tools.md), [milestones](milestones.md), [acceptance tests](acceptance-tests.md), [decisions log](decisions.md).

## Status by area

| Area | Document | Status |
|---|---|---|
| Components, boundary to the broker | [architecture](architecture.md) | implemented, except busd |
| Identity, keys, signing, TOFU pins | [identity and trust](identity-and-trust.md) | implemented; registry read from a copied directory (MVP-simplified) |
| Holds, rotation, revocation by key, `pin` | [identity and trust](identity-and-trust.md) | planned (removal of an agent is implemented) |
| Channels, records, cursors | [bus model](bus-model.md) | implemented |
| Two accounts, streams, consumers, dedupe | [JetStream binding](jetstream-binding.md) | implemented; consumers created by `agent-busctl --direct`, catalogue by a copied snapshot (MVP-simplified) |
| Envelope, targets, types | [protocol](protocol.md) | implemented |
| Request lifecycle | [protocol](protocol.md) | planned |
| Inbox, per-message acks, hooks, waiter, buffer | [delivery](delivery.md) | implemented |
| Presence | [presence](presence.md) | implemented |
| Loop guard, rate limit, secret scan, untrusted frame | [safety](safety.md) | implemented (limits are not CLI flags yet) |
| TLS, per-role grants, password logins, mTLS phase A | [access control](access-control.md) | implemented |
| mTLS enrollment (phase B): enrollment login, `ca` commands and review screen, autosign | [access control](access-control.md) | implemented (decision 41) |
| Private channels | [private channels](private-channels.md) | planned |
| busd | [busd](busd.md) | minimal busd built (enrollment, CA key, approvals, local reloads); the rest planned |
| agent-busctl | [admin CLI](admin-cli.md) | partly: setup, register, channels, removal, directory, certificates, enrollment (`enroll`, `ca`), monitor password |
| Monitor and API | [monitor and API](monitor-and-api.md) | implemented; own principal and `human@` key planned |
| MCP tools | [MCP tools](mcp-tools.md) | partly: 8 of the specified tools |
| Deployment, reload, releases | [operations](operations.md) | implemented |

## Where this text and the code differ

Noted while restructuring; neither side was changed.

- The MVP decision defers the monitor and the OpenCode plugin; both are built.
- Holds: the text says a changed key or an unknown sender (with `--no-auto-accept-new-keys`) is held on disk and acked. The code terminally rejects a changed key (alert, acked) and leaves a sender whose registry record cannot be read unacked for redelivery; there is no hold store.
- The flags `--max-hops`, `--rate-limit`, `--inbox-buffer`, `--[no-]auto-accept-new-keys` and `--owner-identity` are named in the text; the adapter has the limits as configuration fields but the `agent-bus` command line does not expose them.
- The monitor is specified with its own read-only principal `monitor@<owner>` and a consumer made by busd; the code logs in as the two admin users and sends as a registered agent identity, and refuses `human@<owner>`.
- Registry and catalogue reads are specified through busd; the code reads a hand-copied `directory/` (an enrolling machine gets the current one from busd once).
- busd is specified with a signed `busd.<method>` protocol; the minimal busd serves only enrollment and approval on `busd.enroll.*` and `busd.ca.*` subjects in `CONTROL`, authenticated by the account (the owner's tools are `control-admin`).
- The enrollment text said the CA fingerprint is checked against the central machine; the code can also fetch the CA from the broker's handshake, and keeps it only if it matches the owner's fingerprint.
- Only 8 of the MCP tools in the table exist.

## Related documents

[README](../../README.md), [QUESTIONS](../../QUESTIONS.md), and the user documentation in
[docs/](../agent-busctl.md): [docker](../docker.md), [podman](../podman.md), [broker](../broker.md),
[monitor](../monitor.md), [API](../api.md), [Claude Code](../claude-code.md),
[OpenCode](../opencode.md), [agent guide](../agent-guide.md), [agent tools](../agent-tools.md),
[troubleshooting](../troubleshooting.md), [test run](../test-run.md), [development](../development.md).
