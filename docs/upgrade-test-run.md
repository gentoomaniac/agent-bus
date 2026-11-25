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

## 7. Replace start.sh

The old one ran `tls init` and `admin init` on every start. Those only failed because the files
exist, and the `busctl` command no longer exists in the image. One-time setup does not belong
in a start script. The new one only starts the broker, with the certificate port added:

```sh
cat > $BASE/start.sh <<'EOF'
IMG=agent-messenger:dev
BASE_PATH=~/srv/agent-bus
U="--userns=keep-id:uid=10001,gid=10001"

podman rm -f agent-bus 2>/dev/null
podman run -d --rm --name agent-bus $U --network pasta \
  -p 4222:4222 -p 4223:4223 -p 127.0.0.1:8080:8080 \
  -v "${BASE_PATH}/config:/config" -v "${BASE_PATH}/data:/data" $IMG

sleep 2
podman exec agent-bus agent-busctl --direct init
EOF
chmod +x $BASE/start.sh
```

`init` is safe to repeat. It also refreshes the agents' rights for the new version.

## 8. Start the broker

```sh
$BASE/start.sh
podman logs agent-bus | tail -20
```

Look for `nats-server` starting and lines that begin with `busd:`. There should be no warning
that busd uses the admin login.

The shorter command for the future:

```sh
alias agent-busctl='podman exec agent-bus agent-busctl'
```

## 9. Give the monitor its own folder

From now on the monitor never sees `ca.key` or the admin passwords.

```sh
mkdir -p $BASE/monitor-config
podman run --rm $U -v $CFG:/config:ro -v $BASE/monitor-config:/monitor-config \
  --entrypoint agent-busctl $IMG monitor init /monitor-config --agent monitor@gentoomaniac
```

This copies your web password as well. Run it again after you register or enroll agents.

## 10. Point monitor.sh at the new folder

In `monitor.sh`, change this line:

```sh
  -v "${CFG}:/config" --entrypoint bus-monitor \
```

to this:

```sh
  -v "${BASE_PATH}/monitor-config:/config" --entrypoint bus-monitor \
```

## 11. Start the monitor

```sh
$BASE/monitor.sh
podman logs agent-bus-monitor
```

Open <http://localhost:8080> and log in with the same password as before.

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

## Later updates

Most updates need only these steps:

```sh
git pull && make image
$BASE/start.sh
$BASE/monitor.sh
```

Then rebuild `agent-bus` and restart the sessions if the agent side changed.

## If something breaks

```sh
podman rm -f agent-bus-monitor agent-bus
rm -rf $BASE && cp -a $BASE.bak-<date> $BASE
podman tag agent-messenger:pre-upgrade agent-messenger:dev
$BASE/start.sh && $BASE/monitor.sh
```

The backup holds the old `start.sh` and `monitor.sh` too. With the old image, the restored
`start.sh` works as before. Send `podman logs agent-bus` and `podman logs agent-bus-monitor`
along with the problem.
