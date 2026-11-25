# Open questions for the owner

Decisions taken with a default while the owner was away. Confirm or change them; answered entries
move into design.md §13.

## 2026-11-25: new name for `busctl` (done with the default, still changeable)

`busctl` clashes with systemd's `busctl` (the D-Bus tool, `/usr/bin/busctl` on every systemd
machine): a shell without our alias runs systemd's tool ("unrecognized option '--direct'").

Default taken and done: renamed the admin CLI to **`agent-busctl`** (binary, image, release archives, docs, `cmd/agent-busctl`).
Alternatives: `abctl` (short), or a subcommand of the existing binary, `agent-bus admin …` (one
binary fewer). Tell us if you prefer one of those; the rename is mechanical either way.

## 2026-11-25: how long may the wake-up hook wait?

Idle sessions are woken by an `asyncRewake` hook that waits for mail aimed at the agent. Claude
Code enforces the hook's `timeout` (default 600 s, no documented maximum). The hook is configured
with a long timeout; whether Claude Code accepts it can only be confirmed in a real session. If
mail sent to an idle session for longer than ~10 minutes doesn't wake it, tell us: the waiter then
re-arms only at the next turn and we'll look for another way.
