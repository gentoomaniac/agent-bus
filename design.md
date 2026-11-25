# Agent Bus design

The design specification moved to [docs/design/](docs/design/README.md), split into one document per
area (architecture, identity and trust, protocol, delivery, safety, access control, busd, and more)
with diagrams and the implementation status. This file stays so that old links keep working.

Start at [docs/design/README.md](docs/design/README.md). Old section numbers (§4a and so on) map to
the documents as follows:

| Old section | Now |
|---|---|
| Intro, 1 (goals) | [README](docs/design/README.md) |
| 2 Architecture | [architecture](docs/design/architecture.md) |
| 3 Identity, 6 Signing and trust | [identity and trust](docs/design/identity-and-trust.md) |
| 4 Bus model | [bus model](docs/design/bus-model.md) |
| 4a NATS JetStream binding | [JetStream binding](docs/design/jetstream-binding.md) (grants and TLS: [access control](docs/design/access-control.md), reload: [operations](docs/design/operations.md)) |
| 4b Private channels | [private channels](docs/design/private-channels.md) |
| 4c mTLS enrollment | [access control](docs/design/access-control.md) |
| 5 Message envelope | [protocol](docs/design/protocol.md) |
| 7 Delivery | [delivery](docs/design/delivery.md), [presence](docs/design/presence.md) |
| 8 Safety rules | [safety](docs/design/safety.md) |
| 9 MCP tools | [MCP tools](docs/design/mcp-tools.md) |
| 10 agent-busctl | [admin CLI](docs/design/admin-cli.md) |
| 10a Bus monitor | [monitor and API](docs/design/monitor-and-api.md) |
| 10b busd | [busd](docs/design/busd.md) |
| 11 Milestones, 12 Acceptance tests | [milestones](docs/design/milestones.md), [acceptance tests](docs/design/acceptance-tests.md) |
| 13 Decisions | [decisions log](docs/design/decisions.md) |
