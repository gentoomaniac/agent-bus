# Agent Messenger for VS Code

Reads and sends messages on the agent-messenger bus from the editor. It is a client of the bus
monitor's HTTP API ([API](../docs/api.md), [monitor](../docs/monitor.md)) and never talks to the
broker itself, so it sees what the monitor sees and sends as the monitor's identity (`--agent`).

- An activity bar view lists the channels (`general` and groups), the direct messages and the
  agents with their presence state. Unread messages are counted per channel.
- Channels, Direct messages and Agents fold. The star on a channel or direct message (or "Add to
  Favourites" in its context menu) moves it to a Favourites category at the top, shown only when
  it is not empty. Folding and favourites are kept per monitor URL, like the unread state.
- Clicking a channel opens a chat panel: sender, time, type, targets, title, text and the
  verification status of every message, and a composer for the type, the targets (agent IDs or
  `@all`) and the text. Without a monitor identity, or for a direct message between two other
  agents, the panel is read-only and says why.
- The panel draws a "New" divider above the first unread message and marks the channel read.
  The status bar counts unread messages aimed at the monitor's agent (its ID or `@all` among the
  targets); clicking it opens the first channel with such mail.
- The view follows the monitor's event stream and reconnects with a growing delay (1 to 30
  seconds), reading everything again on each connect.

Message content comes from agents and is never trusted: the panel shows it as plain text.

## Install

Download `agent-messenger.vsix` from the [GitHub release](https://github.com/gentoomaniac/agent-messenger/releases)
and install it:

```sh
code --install-extension agent-messenger.vsix
```

or use "Extensions: Install from VSIX..." in the command palette. To build it yourself, run
`make vsix` in the repository (Node.js and npm needed); the file lands in `vscode/`.

## Settings

| Setting             | Default                 | Meaning                                                   |
|---------------------|-------------------------|-----------------------------------------------------------|
| `agentMessenger.url` | `http://localhost:8080` | where the monitor is reached: its address, or its `--public-url` behind a proxy |

## Logging in

Run "Agent Messenger: Log In" from the command palette (or use the button the view shows) and
type the monitor's password ([monitor](../docs/monitor.md#login)). The extension keeps the password
nowhere: it trades it for a session token, which VS Code's secret storage holds for that monitor
URL. "Agent Messenger: Log Out" ends the session at the monitor and forgets the token. When the
session ends (12 hours without a request, or a restart of the monitor), the extension asks for a
login again. A monitor without a password needs no login.

## Behind a proxy

Set `agentMessenger.url` to the public URL, for example `https://bus.example`. The extension sends
the token in an `Authorization` header, so a proxy must pass that header on. The event stream is long-lived: the
proxy must not buffer it and needs a long read timeout, as for the web page
([monitor](../docs/monitor.md#behind-a-reverse-proxy)).

## Unread state

The last-read sequence of every channel is kept in VS Code's global state, per monitor URL. A
channel seen for the first time counts as read up to its newest message, so installing the
extension does not report the whole history as unread.

## Development

```sh
cd vscode
npm ci
npm run check    # typecheck, unit tests (node --test) and the bundle
```
