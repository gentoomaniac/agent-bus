# Upgrading a live test run

Step by step from a bind-mount test run (docs/podman.md, step 7) started before busd
(November 2026) to the current version. The folder here is `~/srv/agent-bus`, holding `config/`,
`data/`, `start.sh` and `monitor.sh`; change `BASE` if yours is elsewhere. Plan for 15 minutes.

What changes:
- `busd` starts in the broker container.
- busd and the monitor get logins of their own.
- The monitor gets a config folder of its own, without the CA key.
- The broker opens the certificate port 4223.
- `busctl` is now called `agent-busctl`.

## 1. Set the variables

Do this in every new shell:

```sh
IMG=agent-messenger:dev
BASE=~/srv/agent-bus
CFG=$BASE/config
U="--userns=keep-id:uid=10001,gid=10001"
```

## 2. Keep the old image, build the new one

Run in your checkout. The tag lets you roll back.

```sh
podman tag agent-messenger:dev agent-messenger:pre-upgrade
git pull
make image
go build -o bin/ ./cmd/agent-bus ./cmd/agent-busctl
```

Put `bin/agent-bus` wherever the old `agent-bus` is on your `PATH`.

## 3. Stop the monitor and the broker

The agents wait and reconnect on their own later.

```sh
podman stop agent-bus-monitor agent-bus
```

Both containers ran with `--rm`, so they are now gone.

## 4. Back up the folder

```sh
cp -a $BASE $BASE.bak-$(date +%F)
```

## 5. Give busd and the monitor their own logins

```sh
podman run --rm $U -v $CFG:/config --entrypoint agent-busctl $IMG service init
```

## 6. Add the CA to the broker certificate (optional)

New machines then check the CA by its fingerprint when they enroll.

```sh
grep -c BEGIN $CFG/broker.pem
```

If it printed `1`, add the CA. If it printed `2`, skip this.

```sh
cat $CFG/ca.pem >> $CFG/broker.pem
```

## 7. Replace start.sh and monitor.sh

The old `start.sh` ran `tls init` and `admin init` on every start. Those only failed because the
files exist. It also calls `busctl`, which the image no longer has. One-time setup does not belong
in a start script. If the old `monitor.sh` sets the password, it asks for one on every start. The
new scripts only start things:

- `start.sh` replaces the broker container and refreshes the streams and grants.
- `monitor.sh` refreshes the monitor's own config folder (`monitor init`), then starts the monitor.

The scripts are in the repo, in `deploy/podman/`. Copy them into the folder; they use the folder
they are in. Your monitor identity goes into a small settings file next to them:

```sh
cp deploy/podman/start.sh deploy/podman/monitor.sh $BASE/
echo 'MONITOR_AGENT=monitor@gentoomaniac' > $BASE/env
```

- **Location:** by default the scripts use the folder they are in, holding `config/`, `data/` and
  `monitor-config/`. Set `AGENT_BUS_HOME` to use another folder.
- **Settings:** `IMG` defaults to `agent-messenger:dev` and `MONITOR_AGENT` to `monitor@$USER`.
  Set them in the `env` file, or per call in the environment. The environment wins over the file:
  ```sh
  AGENT_BUS_HOME=/data/agent-bus IMG=ghcr.io/gentoomaniac/agent-messenger:latest ./start.sh
  ```

`--depend` matters. The monitor shares the broker's network (`--network container:agent-bus`). A
leftover monitor container blocks removing the broker, even one that `podman ps -a` does not list.
The error is `has dependent containers which must be removed before it`, and the broker keeps
running on the old image. `start.sh` removes both.

## 8. Start the broker

```sh
$BASE/start.sh
podman exec agent-bus ps
```

`ps` must list `busd` next to `nats-server`. If it does not, the container is running an old
image. Compare `podman inspect agent-bus --format '{{.Image}}'` with `podman images agent-messenger`.

The shorter command for the future:

```sh
alias agent-busctl='podman exec agent-bus agent-busctl'
```

## 9. Set the web password, once

```sh
mkdir -p $BASE/monitor-config
podman exec -it agent-bus agent-busctl monitor password
```

You may skip this if you already set one, in `config/monitor/` or `config/monitor-web/`.
`monitor init` copies it from either place; images older than `8da82ca` copy it only from
`monitor-web/`. The monitor never sees `ca.key` or the admin passwords. Its folder,
`monitor-config`, holds only what it needs.

## 10. Start the monitor

```sh
$BASE/monitor.sh
podman logs agent-bus-monitor
```

`monitor.sh` asks for nothing. The log shows `connected to the bus` and no "busd is not running".
Open <http://localhost:8080> and log in.

## 11. After every image update

```sh
$BASE/start.sh && $BASE/monitor.sh
```

## 12. Restart the agent sessions

Running sessions still use the old `agent-bus`. In each project, restart Claude Code, or run
`/mcp` and reconnect. The hooks in `.claude/settings.json` stay as they are.

## 13. Check it

- Both agents show in the monitor with a state.
- `@all hello` from the monitor reaches both sessions.
- New in the page:
  - the "New" line above unseen messages;
  - bold channels;
  - folding a section by clicking its heading;
  - the ☆ that moves a channel to Favourites.

## 14. The VS Code extension (optional)

```sh
make vsix
code --install-extension vscode/agent-messenger-*.vsix
```

In VS Code, run "Agent Messenger: Log In" from the command palette, with the same password.

## If something breaks

```sh
podman rm -f --depend agent-bus; podman rm -f agent-bus-monitor
rm -rf $BASE && cp -a $BASE.bak-<date> $BASE
podman tag agent-messenger:pre-upgrade agent-messenger:dev
$BASE/start.sh && $BASE/monitor.sh
```

The backup holds the old `start.sh` and `monitor.sh` too. With the old image, the restored
`start.sh` works as before. Send `podman logs agent-bus` and `podman logs agent-bus-monitor`
along with the problem.
