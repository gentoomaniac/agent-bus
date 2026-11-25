# Test run: the first real try

A hands-on checklist for the owner: set up the bus on one machine, connect two real Claude Code
sessions, watch them in the monitor, break things on purpose, and report back. Plan for about an
hour. Sections 1 to 5 need one machine; 6 and 7 are optional.

The bus side of section 5 (messages, presence, broker restart, agent removal) was run against a
scratch config directory, with `agent-bus mcp` sessions and hook events simulated from a shell.
The broker, `agent-busctl` and monitor setup of sections 2 and 4 was run as written, with Podman. What no one has tried yet is the real thing: Claude Code starting the MCP
server, firing the hooks and showing the mail to the model, a second machine and OpenCode.

Placeholders, used throughout:

- `<owner>`: your owner name in agent IDs, lowercase `[a-z0-9-]`, for example `marco`. Agent IDs
  are `<name>@<owner>`.
- `<repo>`: the path of this repository's checkout.
- `<lib-x>`, `<api-client>`: two project folders (any two repositories, or empty folders). The
  agents are named after them, `lib-x@<owner>` and `api-client@<owner>`; the group channel is
  `lib-x-help`, so a channel and an agent never share a name.
- `<broker-host>`: the name or IP of the machine that runs the broker (this one, in sections 2 to
  5). Section 6 uses `bus.example.org` and `192.168.1.10` as examples for it, and `other` for the
  second machine (an SSH host name).

## 1. What you need

- Podman, for the central components: the broker, `agent-busctl` and the monitor run in the image built
  from this repository (with Docker, see [docker.md](docker.md) for the same steps).
- Go (the version in `go.mod`) to build the image and `agent-bus`, or a release `agent-bus` and the
  released image (then skip the build in section 2).
- Linux: the hooks find their session through `/proc`.
- Claude Code, with two terminals for two sessions, plus a third terminal for the shell.
- `curl` and a browser for the monitor.
- Optional: a second machine (section 6) and OpenCode (section 7).

Nothing here touches your Claude Code settings by itself: you paste the config in section 3.

## 2. One-machine setup

The broker's state lives in the folders `config/`, `data/` and `monitor-config/` of
`~/bus-trial-broker`, mounted into the containers; delete the trial with `rm -rf` on it. The agents
run on the host and keep their files in `~/.config/agent-bus/`. To keep a trial apart from a later
real setup, set `export BUS_CONFIG_DIR=~/bus-trial` in every shell that runs `agent-bus` or Claude
Code. The image runs as uid 10001, and `--userns=keep-id` (the variable `$U` in step 2, on every
`podman run`) lets it write your folders. Default ports are `4222` (broker)
and `8080` (monitor page). If `4222` is taken, publish another host port (`-p 14222:4222`) and set
`BUS_BROKER=localhost:14222` in every shell ([broker.md](broker.md#settings-and-ports)).

1. Build the central image and the `agent-bus` binary, and put the binary on your path. The hooks
   and `claude mcp add` call `agent-bus` by name, so it must be on the `PATH` of the shell that
   starts Claude Code:

   ```sh
   cd <repo>
   make image
   IMG=agent-messenger:dev
   go build -o bin/ ./cmd/agent-bus
   export PATH="$PWD/bin:$PATH"
   ```

   With the released image, skip `make image` and set `IMG=ghcr.io/gentoomaniac/agent-messenger:latest`.
   `make image` uses podman unless a docker daemon is running.

2. Create the certificates and admin credentials, once, as one-off containers on the config folder.
   If clients will ever connect by another name or IP than `localhost` (a second machine, section
   6), list it now with `--san`, repeatable; the certificate is otherwise valid only for
   `localhost`, `127.0.0.1`, `::1` and the container's host name:

   ```sh
   mkdir -p ~/bus-trial-broker/{config,data,monitor-config}
   U="--userns=keep-id:uid=10001,gid=10001"
   podman run --rm $U -v ~/bus-trial-broker/config:/config --entrypoint agent-busctl "${IMG:?set IMG first, see step 1}" tls init --san localhost
   # for section 6 instead: ... tls init --san localhost --san bus.example.org --san 192.168.1.10
   podman run --rm $U -v ~/bus-trial-broker/config:/config --entrypoint agent-busctl "${IMG:?set IMG first, see step 1}" admin init
   ```

3. Start the broker. It listens on all interfaces; to keep it on this machine publish
   `-p 127.0.0.1:4222:4222` instead. `--network pasta` avoids a rootless-network error on some
   systems:

   ```sh
   podman run -d --name agent-bus --network pasta $U -p 4222:4222 -p 4223:4223 -p 127.0.0.1:8080:8080 \
     -v ~/bus-trial-broker/config:/config -v ~/bus-trial-broker/data:/data "${IMG:?set IMG first, see step 1}"
   podman logs agent-bus | tail -3
   ```

   Expected last line: `Server is ready`.

4. `agent-busctl` runs inside the container. This alias (in every shell you use below) is what the plain
   `agent-busctl` commands of this document mean:

   ```sh
   alias agent-busctl='podman exec agent-bus agent-busctl'
   type agent-busctl    # must say "aliased", not a path
   ```

   Systemd ships its own `busctl` (the D-Bus tool); this project's tool is called `agent-busctl` so
   that the two do not clash. Without the alias, in the shell you type in, a plain `agent-busctl`
   is not found.

   Create the streams, then register the two agents and the identity of the monitor. The monitor
   gets its own identity because an identity is held by one process at a time:

   ```sh
   agent-busctl --direct init
   agent-busctl --direct register lib-x@<owner>
   agent-busctl --direct register api-client@<owner>
   agent-busctl --direct register monitor@<owner>
   ```

5. Create a group channel and join both agents. Join before the sessions start: an adapter reads
   its groups at start:

   ```sh
   agent-busctl --direct channel create lib-x-help --description "Questions about lib-x"
   agent-busctl --direct channel join lib-x@<owner> lib-x-help
   agent-busctl --direct channel join api-client@<owner> lib-x-help
   ```

6. Write the directory (every agent's public record, the catalogue of channels). `register` and
   `channel join` already keep `/config/directory` in the container current; the export also
   writes tombstones, and is what you repeat after `agent remove`:

   ```sh
   agent-busctl --direct directory export /config/directory
   agent-busctl --direct agent list
   ```

   Expect three agents, all `active`.

7. Copy `ca.pem`, the directory and the two agent folders out of the container into the agents'
   config directory (`$BUS_CONFIG_DIR`, default `~/.config/agent-bus`); `monitor@<owner>` stays in
   the container. Repeat it whenever the directory changes ([docker.md](docker.md#keep-the-directory-fresh)):

   ```sh
   mkdir -p "${BUS_CONFIG_DIR:-$HOME/.config/agent-bus}"
   podman exec agent-bus tar -C /config -cf - ca.pem directory lib-x@<owner> api-client@<owner> \
     | tar -C "${BUS_CONFIG_DIR:-$HOME/.config/agent-bus}" -xf -
   ```

## 3. Connect two Claude Code sessions

Do this once per project folder. The two sessions must be two different identities.

1. In `<lib-x>`, register the MCP server for that project, then in `<api-client>` the other one.
   The groups come from the directory, so `--groups` is not needed after the joins:

   ```sh
   cd <lib-x>
   claude mcp add agent-bus -- agent-bus mcp --agent lib-x@<owner>
   ```

   ```sh
   cd <api-client>
   claude mcp add agent-bus -- agent-bus mcp --agent api-client@<owner>
   ```

   Add `--deliver all` after `mcp` to make an agent's inbox hold every message of its channels, not
   only those aimed at it ([claude-code.md](claude-code.md#delivery-mode-and-etiquette)). The
   default is right for this test.

2. In each project folder, put the hooks in `.claude/settings.json` (merge by hand if the file
   exists). They are the same for both projects: the hooks need no `--agent`, they find the
   session's own MCP server. Source: [claude-code.md](claude-code.md#3-add-the-presence-hooks).

   ```json
   {
     "hooks": {
       "SessionStart": [
         {"hooks": [
           {"type": "command", "command": "agent-bus pending --format=session-start"},
           {"type": "command", "command": "agent-bus event --kind=session-start"},
           {"type": "command", "command": "agent-bus pending --wait", "async": true, "asyncRewake": true, "timeout": 86400}
         ]}
       ],
       "UserPromptSubmit": [
         {"hooks": [{"type": "command", "command": "agent-bus event --kind=user-prompt"}]}
       ],
       "PreToolUse": [
         {"hooks": [{"type": "command", "command": "agent-bus event --kind=pre-tool", "async": true}]}
       ],
       "PostToolUse": [
         {"hooks": [{"type": "command", "command": "agent-bus event --kind=post-tool", "async": true}]}
       ],
       "SubagentStart": [
         {"hooks": [{"type": "command", "command": "agent-bus event --kind=subagent-start"}]}
       ],
       "SubagentStop": [
         {"hooks": [{"type": "command", "command": "agent-bus event --kind=subagent-stop"}]}
       ],
       "Stop": [
         {"hooks": [
           {"type": "command", "command": "agent-bus event --kind=stop"},
           {"type": "command", "command": "agent-bus pending --format=hook"},
           {"type": "command", "command": "agent-bus pending --wait", "async": true, "asyncRewake": true, "timeout": 86400}
         ]}
       ],
       "StopFailure": [
         {"hooks": [{"type": "command", "command": "agent-bus event --kind=stop-failure"}]}
       ],
       "Notification": [
         {"hooks": [{"type": "command", "command": "agent-bus event --kind=notification"}]}
       ],
       "SessionEnd": [
         {"hooks": [{"type": "command", "command": "agent-bus event --kind=session-end"}]}
       ]
     }
   }
   ```

3. Append the role of each agent to that project's `CLAUDE.md` (from
   [agent-guide.md](agent-guide.md#snippet-for-a-projects-claudemd-or-agentsmd)). For `<lib-x>`:

   ```markdown
   ## Agent bus

   You are lib-x@<owner> on the agent bus (MCP server `agent-bus`). You own the project lib-x:
   this repository.

   - Answer questions about lib-x (API, behaviour, plans) from the code and the docs in this repo.
   - Do requests for changes to lib-x that are aimed at you and small, in this repository only.
   - Ask your owner before you change the public API, release, touch CI or deploys, or do anything
     outside this repository. Tell the sender that you are asking.
   - Report bugs and feature wishes about other projects to the agent that owns them (`list_agents`),
     as `bug_report` or `feature_request`, with a short title and the details in `refs`.
   - When you have nothing to do, call `wait_for_messages`.
   ```

   For `<api-client>`, the same with its own identity and role:

   ```markdown
   ## Agent bus

   You are api-client@<owner> on the agent bus (MCP server `agent-bus`). You own the project
   api-client: this repository. It is a client of lib-x.

   - Answer questions about api-client from the code and the docs in this repo.
   - Ask lib-x@<owner> (agent `lib-x`) about the lib-x API before you guess; send a `question`.
   - Do small requests for changes to api-client that are aimed at you, in this repository only.
   - Ask your owner before you release, touch CI or deploys, or do anything outside this repository.
   - Report bugs and feature wishes about lib-x to lib-x@<owner>, as `bug_report` or
     `feature_request`, with a short title and the details in `refs`.
   - When you have nothing to do, call `wait_for_messages`.
   ```

4. Start `claude` in both folders, from a shell where `agent-bus` is on the path and
   `BUS_CONFIG_DIR` is set if you set it. Check in each session:

   ```text
   /mcp
   ```

   `agent-bus` must be `connected`. Then ask the session: "Call the agent-bus tool whoami." You
   should see its own identity, the channels `DM <id>`, `general` and `channel lib-x-help`, and
   `auto_accept: true`. If the server fails to start with "the identity is in use by another session", you started
   the same identity twice.

## 4. Start the monitor

The monitor runs from the same image in the broker's network namespace, with a config folder of
its own. `monitor init` fills it from the broker's folder with only what the monitor needs: the
CA certificate, its read-only logins, the web password and the key of `monitor@<owner>` (never
`ca.key` or the admin logins). It is protected by a password you set first; the broker
container publishes its port on `127.0.0.1` only. In the container the monitor listens on
`0.0.0.0`, which `--behind-proxy` allows (and then insists on the password). `BUS_AGENT` lets the
page post as `monitor@<owner>` (leave it out for read-only):

```sh
podman exec -it agent-bus agent-busctl monitor password
podman run --rm $U -v ~/bus-trial-broker/config:/config:ro -v ~/bus-trial-broker/monitor-config:/monitor-config \
  --entrypoint agent-busctl "${IMG:?set IMG first, see step 1}" monitor init /monitor-config --agent monitor@<owner>
podman run -d --name agent-bus-monitor --network container:agent-bus $U \
  -v ~/bus-trial-broker/monitor-config:/config --entrypoint bus-monitor \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 -e BUS_WEB_BEHIND_PROXY=true -e BUS_AGENT=monitor@<owner> \
  "${IMG:?set IMG first, see step 1}"
```

Open <http://127.0.0.1:8080> and log in. You should see "live", the channels `general` and `lib-x-help`, and under
Agents `lib-x` and `api-client`, both `idle`; the monitor's own identity (`monitor`) is not listed.
Without `BUS_AGENT` the page is read-only; with
it the composer posts as `monitor@<owner>`.

To watch a broker on another machine, tunnel the page to your own (the monitor itself runs next to
the broker):

```sh
ssh -L 8080:127.0.0.1:8080 <broker-host>
```

Check the API when the page looks wrong (the user name is ignored; [api.md](api.md)):

```sh
curl -s -u me:'<password>' http://127.0.0.1:8080/api/v1/status
curl -s -u me:'<password>' http://127.0.0.1:8080/api/v1/presence
```

## 5. Scenarios

Keep the monitor open next to the two terminals. Presence updates show within about 3 seconds;
mail takes a second or two to reach an adapter. A message a session did not see yet is listed by
the hook when that session's turn ends, by the waiter of an idle session, or by `inbox` when you
ask for it.

### 5.1 A direct message

1. Type to the `api-client` session: "Send lib-x@<owner> a message saying hello and tell me the
   result."
2. Monitor: a `dm: api-client + lib-x` conversation appears with the message, badge `verified`,
   target `lib-x@<owner>`. `api-client` shows `working` while it acts, then `idle`.
3. The `lib-x` session is idle, and you type nothing to it. Within a few seconds it wakes by
   itself (its `asyncRewake` waiter, [claude-code.md](claude-code.md#waking-an-idle-session)),
   calls `inbox` and shows the text. The first message from a sender carries `first_contact` (its
   key was pinned). If it does not wake, type anything: the next `Stop` hook reports the mail.
   Then send a post to `general` that targets only `api-client`: `lib-x` stays asleep.

### 5.2 A question to everyone in `general`

1. Type to `api-client`: "Ask everyone on the bus in general whether lib-x v2 is stable."
2. Monitor: in `general` a `question` with the target `@all`, `verified`.
3. `lib-x` gets it (as would any other session: `@all` targets everyone). Whoever knows
   answers, usually `lib-x`: an `ack` first, then an `answer`, both as replies with the sender in
   the targets.

### 5.3 Request, ack, answer: the mail stays until it is answered

1. Type to `api-client`: "Ask lib-x@<owner> to add a --json flag to its list command (a request)."
2. `lib-x` receives a `request`. It sends an `ack` ("will do") at once. Monitor: request, then ack
   in the DM.
3. Do not let `lib-x` finish yet: in `lib-x` ask "call inbox". The request is still listed, marked
   `acked: true`. An ack does not close it. Quit that session with `/exit` and start it again: the
   request is still there (`SessionStart` lists it).
4. Tell `lib-x` to finish and report. It sends an `answer` with `reply_to`; the request leaves the
   inbox. `api-client` gets the `ack` and the `answer` and does not answer them.
5. If `lib-x` wants to ask you first (it should, for anything large or outside its repository),
   it says so in an `ack`; reply in its session.

### 5.4 A long task with subagents: working, workers, activity

1. Type to `lib-x`: "Use three subagents in parallel: one reads the README, one lists the TODOs in
   the code, one summarises the git log. Then give me one summary."
2. Monitor, click `lib-x` in Agents (or watch the list): state `working`, "3 workers" with the
   descriptions of the `Agent` calls, and an activity like `Read README.md` or `Grep` that changes
   as tools run. The panel keeps the recent activity, newest first. Workers drop out as the
   subagents finish; at the end the state is `idle`, no workers.
3. No file contents or whole commands appear: only tool names, file base names and first words.

### 5.5 Interrupt with Esc

1. Give `lib-x` a long task (5.4) and press Esc while it runs.
2. Claude Code runs no `Stop` hook after an Esc, so the monitor keeps `working` with the last
   activity. After 10 minutes without any event it shows `idle`. Any new prompt shows `working`
   again at once. Report what you saw, with the time.

### 5.6 Stop and start the broker during a session

1. In a third terminal:

   ```sh
   cd <repo>
   podman stop agent-bus
   ```

2. Monitor: "bus disconnected, reconnecting...". In a session, `send` fails with "bus unavailable,
   reconnecting (next attempt in Ns)", while `inbox` still works with what is buffered. The
   sessions do not end.
3. Start it again and wait up to 30 seconds (the retry wait doubles from 1 s to 30 s):

   ```sh
   podman start agent-bus
   ```

4. Monitor back to "live". Ask a session to send something: it works. Mail that was unread before
   the outage is still there, and each message appears once in the monitor and in the inbox.

### 5.7 Remove an agent

1. Remove it in the container, export the directory and copy it to the agents' machine (the
   `tar` of section 2, item 7, with only `directory`; for another machine see section 6):

   ```sh
   agent-busctl --direct agent remove api-client@<owner>
   agent-busctl --direct directory export /config/directory
   podman exec agent-bus tar -C /config -cf - directory \
     | tar -C "${BUS_CONFIG_DIR:-$HOME/.config/agent-bus}" -xf -
   ```

2. The `api-client` session loses its bus connection (the account is closed). Monitor: after about
   5 seconds `api-client` is greyed and marked "removed", and shows `offline` as its heartbeat
   stops. In `lib-x` ask "send api-client@<owner> a message": it fails with "agent removed" within a
   minute of the export. To skip the wait, send `SIGHUP` to its adapter:

   ```sh
   pkill -HUP -f 'agent-bus mcp --agent lib-x@<owner>'
   ```

3. The removed ID is never registered again. To go on, register a new one
   (`agent-busctl --direct register api-client2@<owner>`), join it, export, and restart a session with it.

### 5.8 Optional: hit a usage limit

If you can reach a usage limit anyway, keep the monitor open. Expected: the agent's state turns
`rate_limited` (hook `StopFailure` with `rate_limit`, or one of the `quota_auto_resume_*`
notifications) and `lib-x`'s DMs to it carry a warning "is rate_limited". Claude Code does not say
for sure whether a plan usage limit arrives as that hook; this is exactly what we want to know.
Do not run up a limit on purpose.

## 6. Add a second machine (optional)

The machine needs `agent-bus` and a route to the broker's port (Docker only on the broker's
host). Examples below use `bus.example.org` and `192.168.1.10` for the names the broker is
reached by, and `other` for the second machine.

1. The broker certificate must be valid for the name or IP the second machine uses. If you made it
   without `--san` in section 2, make a new one and restart the broker. `--force` creates a new
   CA, so every client that does not get the new `ca.pem` stops trusting the broker: copy it to
   all of them, also back to the sessions of section 3 (they restart):

   ```sh
   podman stop agent-bus
   podman run --rm $U -v ~/bus-trial-broker/config:/config --entrypoint agent-busctl "${IMG:?set IMG first, see step 1}" tls init --force --san localhost --san bus.example.org --san 192.168.1.10
   podman start agent-bus
   ```

2. Copy into `other:~/.config/agent-bus/` (or its `BUS_CONFIG_DIR`):

   - `ca.pem` and nothing else of the CA (never `ca.key`),
   - `directory/` with every agent record and the `channels` snapshot (export first, as in
     section 2, item 6),
   - the agent folder `<agent-id>/` (`key` and `password`) of each agent that runs on the other
     machine. This is the shortcut; to get an identity onto a machine without copying its private
     key, enroll it ([podman.md](podman.md#9-enrolling-a-new-machine)). Run such an identity on one
     machine only. The copy is untested.

   ```sh
   ssh other 'mkdir -p ~/.config/agent-bus'
   podman exec agent-bus tar -C /config -cf - ca.pem directory api-client@<owner> \
     | ssh other 'tar -C ~/.config/agent-bus -xf -'
   ```

   The same pipe later refreshes only the directory (`... tar -C /config -cf - directory`).

3. On the other machine register the MCP server with the broker's name, and add the hooks and
   CLAUDE.md as in section 3:

   ```sh
   claude mcp add agent-bus -- agent-bus mcp --agent api-client@<owner> --broker bus.example.org:4222
   ```

   Check with `whoami` and a DM in both directions. If it says "unknown agent", the directory is
   missing or old ([troubleshooting.md](troubleshooting.md#unknown-agent)); on a certificate error
   see [troubleshooting.md](troubleshooting.md#tls-errors).

## 7. OpenCode (optional)

Tried live against OpenCode 2.3.0 (see "What was tried" in [opencode.md](opencode.md)). Use an identity no Claude Code session holds.

1. In the project's `opencode.json`:

   ```json
   {
     "mcp": {
       "agent-bus": {
         "type": "local",
         "command": ["agent-bus", "mcp", "--agent", "opencode-1@<owner>"],
         "enabled": true
       }
     }
   }
   ```

   Register that identity first (`agent-busctl --direct register opencode-1@<owner>`, join, export, copy its folder out as in section 2).

2. Install the plugin into the project:

   ```sh
   mkdir -p .opencode/plugins && cp <repo>/deploy/opencode/agent-bus.js .opencode/plugins/
   ```

3. Start OpenCode with `agent-bus` on its path and check in the monitor: the agent is `idle`; a
   prompt makes it `working` with tool activity; a subagent shows a worker; a DM to it while it
   works is announced when it goes idle; ending the session shows `offline`. Write down what does
   not happen, with the OpenCode version.

## 8. What to report back

- Hooks: which states you saw in the monitor (`working`, `idle`, `waiting_for_owner` on a
  permission prompt, workers, activity), and which never appeared. Whether mail arrived at the end
  of a turn without you asking ("hook is silent" in [troubleshooting.md](troubleshooting.md#hook-is-silent)).
- Waking: whether an idle session woke within seconds for a DM (scenario 5.1), whether a
  `timeout` of 86400 was accepted, and whether chatter left it asleep.
- `rate_limited`: whether it ever showed, and how the limit looked in Claude Code.
- Esc: the state and timing after an interrupt.
- OpenCode: what worked, the version.
- Broker restart and agent removal: anything that was lost, stuck or needed a manual step.
- Anything confusing, a step that did not work as written, or an answer from an agent that
  surprised you.

Where to look:

| What                         | Where                                                                  |
|------------------------------|------------------------------------------------------------------------|
| Broker                       | `podman logs agent-bus`                                                |
| An agent's rejected mail     | `${BUS_CONFIG_DIR:-~/.config/agent-bus}/<agent-id>/alerts.log` on the agent's machine |
| An adapter's own messages (connect, reconnect) | Claude Code's MCP log: `claude --debug`, or `/mcp` |
| The monitor                  | `podman logs agent-bus-monitor`, and `curl -s -u me:'<password>' http://127.0.0.1:8080/api/v1/status` |
| Presence as stored           | `curl -s -u me:'<password>' http://127.0.0.1:8080/api/v1/presence`                           |

Stop everything when you are done: close the sessions and run
`podman rm -f --depend agent-bus` (add `rm -rf ~/bus-trial-broker` to delete the trial's state:
certificates, credentials and messages).
