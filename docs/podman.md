# Podman

Run the central components of Agent Bus with plain `podman` commands, from the released image. Use
this when you have Podman but no compose provider; with Docker use [docker.md](docker.md), which
does the same steps. All state lives in plain folders on the host, bind-mounted into the containers:
you can look at it, back it up and delete it with `rm -rf`. Every step was run as written on rootless
Podman 6.1.

| In this guide | |
|---|---|
| [Quick start](#quick-start) | image, folders, broker, streams: steps 1 to 4 |
| [Enrolling a new machine](#9-enrolling-a-new-machine) | the normal way to add an agent |
| [The monitor](#6-the-monitor), [TLS proxy](#publish-the-monitor-through-a-tls-proxy) | the web page and how to publish it |
| [Start scripts](#the-start-scripts), [day to day](#day-to-day) | restarts, upgrades |
| [Backup and restore](#backup-and-restore) | config, keys, messages, monitor preferences |
| [Copy the keys](#alternative-copy-the-keys), [certificate login](#5a-certificate-login-optional) | for copied identities and the admin tools |

**Variables used below.** `alice` is the owner name in agent IDs (lowercase), and `bus.example.com`
is the name or IP other machines use to reach this one. Set the next two in every shell you use
for this guide, together with `AGENT_BUS_HOME`:

```sh
export AGENT_BUS_HOME=~/agent-bus            # where the config, data and monitor state live
IMG=ghcr.io/gentoomaniac/agent-messenger:latest   # or a release tag; see "Build locally" for agent-messenger:dev
U="--userns=keep-id:uid=10001,gid=10001"
```

<a id="1-build-or-pull-the-image"></a>

## Quick start

### 1. The image and the folders

Podman pulls the released image by itself on the first `run`; `podman pull $IMG` does it now. The
image runs as uid 10001 and a bind-mounted folder keeps its host owner, so every `podman run` below
carries `$U`, which maps your own user to uid 10001 and lets the container write your folders. It
must be on the one-off containers too, or the files end up owned by another uid.

```sh
mkdir -p $AGENT_BUS_HOME/{config,data,monitor-config}
```

| Container path | Host folder | Holds | Secret |
|---|---|---|---|
| `/config` | `config/` | certificates, admin and service logins, agent keys, `nats/` auth files, `directory/`, `enroll/` | yes: `ca.key`, every `key` and `password` |
| `/data` | `data/` | JetStream storage: the messages | the messages |
| `/config` of the monitor | `monitor-config/` | the monitor's own, smaller config: `ca.pem`, two read-only logins, web password hash, `prefs.json`, the sending agent's key | yes, but no CA key, no admin login |

The folders belong to you (through `$U`); the secret files inside are mode 0600 and their folders
0700. Named volumes (`podman volume create
agent-bus-config`, `-v agent-bus-config:/config`, no `$U`) work as well, but their files are hidden
in Podman's storage and every step that copies a file needs a one-off container; this guide assumes
folders.

### 2. Certificates and admin credentials

`tls init` must name every host name or IP agents use to reach the broker (`--san`, repeatable).
`localhost`, `127.0.0.1` and `::1` are always in it.

```sh
podman run --rm $U -v $AGENT_BUS_HOME/config:/config --entrypoint agent-busctl $IMG tls init --san bus.example.com
podman run --rm $U -v $AGENT_BUS_HOME/config:/config --entrypoint agent-busctl $IMG admin init
```

```text
CA certificate:     /config/ca.pem
CA key:             /config/ca.key
Broker certificate: /config/broker.pem
Broker key:         /config/broker.key
Broker valid for:   [localhost 127.0.0.1 ::1 bus.example.com]
Admin credentials written to /config/bus-admin and /config/control-admin; the server's authorization files to /config/nats.
```

A name added later needs only `agent-busctl tls broker --san ...` and a broker restart: the CA stays
([agent-busctl.md](agent-busctl.md#tls-broker)).

### 3. Start the broker

```sh
podman run -d --name agent-bus --network pasta $U -e BUSD_BUS_OWNER=monitor@alice \
  -p 4222:4222 -p 4223:4223 -p 127.0.0.1:8080:8080 \
  -v $AGENT_BUS_HOME/config:/config -v $AGENT_BUS_HOME/data:/data $IMG
podman logs agent-bus | tail -4
```

**Checkpoint:** the log shows `Server is ready` and a line `busd ...: serving enrollment, approval,
the directory and channel requests on the bus`. A line saying `has agent-busctl init run?` is
expected until step 4.

Two details of this command:

- **Network.** Rootless Podman's default network can fail on some systems (`netavark: create veth
  pair`); `--network pasta` avoids that.
- **Bus owner.** `BUSD_BUS_OWNER` names the agent that manages every channel and creates channels
  without a limit ([bus owner](#6-the-monitor)). It is set when the container is created, so it is
  here and not at the monitor. Leave it out only if no agent should have that role.
- **Ports.** 4222 and 4223 are the broker (TLS, [broker.md](broker.md#settings-and-ports)). 8080 is
  the monitor's page, published on the host's loopback only.

[`deploy/podman/start.sh`](#the-start-scripts) does this and step 4's `init`.

### 4. Create the streams

An alias runs `agent-busctl` inside the broker container. It must be set in the shell you type in. It
has no `-it`, so it suits commands that print and exit; the interactive ones (`ca review`, `monitor
password` without a pipe) are written as `podman exec -it agent-bus agent-busctl ...` below:

```sh
alias agent-busctl='podman exec agent-bus agent-busctl'
type agent-busctl                  # must say: agent-busctl is aliased to ...
agent-busctl --direct init
```

```text
streams BUS (keeps 2160h0m0s, at most 1073741824 bytes), PRESENCE and LEDGER and buckets registry and catalogue are in place, agent grants are up to date
```

If it cannot connect yet, wait a second and run it again; it is safe to repeat. The name is
`agent-busctl` and not `busctl` because systemd ships its own `busctl` (the D-Bus tool).
**Checkpoint:** `agent-busctl --direct agent list` prints nothing and does not fail.

### 5. Turn on enrollment, add an agent

Turn enrollment on once. It makes a shared login that only lets a machine ask, and prints its
password once, with the CA fingerprint:

```sh
agent-busctl enroll init
```

Then follow [Enrolling a new machine](#9-enrolling-a-new-machine).

<a id="9-enrolling-a-new-machine"></a>

## Enrolling a new machine

A new machine gets its agent's certificate by asking the broker, and you approve it: nothing is
copied by hand and the agent's private keys never leave the machine it runs on
([design/access-control.md](design/access-control.md), phase B). It needs `enroll init` done and the
broker's port `4223` published (step 3 does).

**On the new machine** you need the `agent-bus` binary from the
[release](https://github.com/gentoomaniac/agent-bus/releases) and nothing else. Use the password and
the CA fingerprint that `enroll init` printed (`agent-busctl ca fingerprint` shows the fingerprint
again; read it off the broker's machine, not off the new one):

```sh
agent-bus enroll --broker bus.example.com:4222 --agent lib-x@bob \
  --ca-fingerprint 'E4:32:5C:83:...' --enroll-password 'gx-RjvBTTkJDbLpWYFgGlgsd_cuUd7ojyDOXF9WzQEA' \
  --waitforcert
```

```text
the CA certificate matches E4:32:5C:83:... and is saved to ~/.config/agent-bus/ca.pem
asking for a certificate for lib-x@bob
approval code CI2Q-ARSX, signing key SHA256:bSKbqd...: the owner compares them with what `agent-busctl ca review` shows
```

The command fetches the CA certificate from the broker and keeps it only if its SHA-256 equals the
fingerprint, makes the keys on the machine and sends the request. A CA taken from the broker alone
would prove nothing, so the flag is required unless you copied a `ca.pem` into
`~/.config/agent-bus/`. `--waitforcert` keeps asking until you decide, for up to 24 hours
(`--waitforcert=10m` sets another time); without it the command returns and running it again collects
the answer.

**Approve it, on the broker's machine:**

```sh
podman exec -it agent-bus agent-busctl ca review
```

The screen lists each request with the agent ID, hostname, code, age and both key fingerprints.
Compare the code and the agent ID with what the new machine printed. Keys: up and down select, `a`
approves, `r` rejects (with a reason the new machine shows), `/` filters, `q` quits. In a script use
`agent-busctl ca list`, `ca sign <code>` and `ca reject <code> --reason "..."`.

**Checkpoint:** the new machine prints

```text
enrolled lib-x@bob: certificate ~/.config/agent-bus/lib-x@bob/client.pem, key ~/.config/agent-bus/lib-x@bob/client.key; the agent syncs the directory from busd when it starts
```

and `agent-busctl --direct agent list` shows `lib-x@bob  active`. The agent then logs in with its
certificate on port 4223 of the broker. It syncs the directory from busd when it starts, and running
agents learn of it within seconds ([design/directory-v2.md](design/directory-v2.md)). Connect the
harness with [Claude Code](claude-code.md) or [OpenCode](opencode.md).

An ID that exists, autosigning, the request limits, the shared password and the rollback are in
[agent-busctl.md](agent-busctl.md#enrollment).

**What runs where.** The image's second program, `busd`, starts in the broker container next to
`nats-server` when the config folder has `ca.key`, and starts again if it stops (`BUS_BUSD=off`, an
environment variable of the container, runs the broker without it). It holds the CA key, keeps the
pending requests and makes the changes to the authorization file and the reloads, all inside this
container, with no HTTP port. Its log is part of `podman logs agent-bus` (lines starting `busd:`);
`podman exec agent-bus pkill busd` makes the entrypoint start it afresh. The web monitor only shows
"N pending enrollments": it has no CA key and cannot approve.

### An existing setup

An image update needs nothing: busd starts by itself and does nothing until `enroll init` ran. Two
optional steps, for a broker folder made by an older version:

| Step | When | Command |
|---|---|---|
| Logins of their own for busd and the monitor | busd logs a warning until then | `podman run --rm $U -v $AGENT_BUS_HOME/config:/config --entrypoint agent-busctl $IMG service init`, then `podman restart agent-bus` |
| The CA after the broker certificate | `--ca-fingerprint` needs it; otherwise copy `ca.pem` to the new machine | `podman exec agent-bus sh -c '[ "$(grep -c BEGIN /config/broker.pem)" = 1 ] && cat /config/ca.pem >> /config/broker.pem'`, then `podman restart agent-bus`; it appends only when the file holds one certificate, so the CA is not added twice |

Password logins, certificate logins and the monitor go on as they were; a monitor that mounts the
broker's whole folder keeps its admin login until it gets a config of its own
([The monitor](#6-the-monitor)).

## Alternative: copy the keys

Use this for an agent of an older version, a broker without busd, or a machine that cannot reach
the broker to enroll. It copies the agent's private key to the machine, so run that identity on
one machine only. Register it on the broker first:

```sh
agent-busctl --direct register lib-x@bob
agent-busctl --direct directory export /config/directory
```

On this machine copy what it needs from `$AGENT_BUS_HOME/config`; to another machine, stream it over
SSH:

```sh
mkdir -p ~/.config/agent-bus
cp -r $AGENT_BUS_HOME/config/{ca.pem,directory,lib-x@bob} ~/.config/agent-bus/
# or:
tar -C $AGENT_BUS_HOME/config -cf - ca.pem directory lib-x@bob \
  | ssh bob@laptop.example.com 'mkdir -p ~/.config/agent-bus && tar -xf - -C ~/.config/agent-bus'
```

Never copy `ca.key` or other identities' folders. A copied identity needs the broker's address too:
start `agent-bus mcp` with `--broker bus.example.com:4222` (or `BUS_BROKER`), or write it into
`~/.config/agent-bus/lib-x@bob/broker`; without either it dials `localhost:4222` and warns. Agents
of older versions need `directory/` copied again after every `register`, `channel join` or
`agent remove`; newer ones sync it themselves.

<a id="5a-certificate-login-optional"></a>

## Certificate login for copied identities and the admin tools

Enrolled agents already log in with a certificate. A copied identity logs in with its password
until you switch it, and so do the admin tools. The broker's second port, `4223`, is where the
certificate login happens; password logins stay on `4222`. Each switch is per agent and can be
rolled back:

| Who | Before | Action | Verify | Rollback |
|---|---|---|---|---|
| Admin tools | broker created with `-p 4223:4223` (recreate it otherwise: `podman rm -f --depend agent-bus`, the `run` of step 3, then the monitor) | `agent-busctl --direct cert issue --admin` | `agent-busctl --direct agent list` works. busd uses the old login until `podman exec agent-bus pkill busd` | `agent-busctl --direct cert switch --admin --password` (also from a half-finished `cert issue --admin`, which can simply be run again) |
| One agent | admin tools done | `agent-busctl --direct cert issue lib-x@bob --switch`, then copy `client.pem` and `client.key` from `config/lib-x@bob/` to the agent's folder on its machine, and restart its `agent-bus` | `agent list` and a message. Forget the password file only after that; it is harmless to keep | `agent-busctl --direct cert switch lib-x@bob --password`, delete `client.pem` and `client.key` on the machine, restart |

The agent finds `client.pem` and picks certificate login by itself, on port `4223` of the
`--broker` host (`BUS_MAPPED_PORT` if you published another one). Until the files are on its machine
it cannot log in, so copy them promptly. `cert issue` again replaces a certificate and the old one
stops working at once. Every command that changes access runs in the broker container (`podman exec
agent-bus`); the monitor container only reads its own folder and is never asked to reload
([broker.md](broker.md#reloads)).

<a id="6-the-monitor"></a>

## The monitor

`bus-monitor` runs in a second container, on the broker's network, with a config folder of its own.
What the page does is in [monitor.md](monitor.md). It shows all traffic, so set its password first.

**Password.** It is stored as a hash in the config folder. Use `-it` to be asked, or pipe it in:

```sh
podman exec -it agent-bus agent-busctl monitor password
# or: printf '%s\n' 'a long password' | podman exec -i agent-bus agent-busctl monitor password
```

**Its config folder.** `monitor init` fills `monitor-config/` from a one-off container that reads the
broker's folder read-only: the CA certificate (not its key), the passwords of its two read-only
logins (it cannot approve a request or write the registry), the web password hash, the directory
snapshot and the key of the agent it sends as. It never sees `ca.key`, the admin logins or busd's.
Register the sending identity first, then run `monitor init`. Agents you register or enroll later
show up by themselves, because the monitor reads the directory through busd. Run it again, and
restart the monitor, only after you change the web password, the sending identity's key or the CA:

```sh
agent-busctl --direct register monitor@alice
podman run --rm $U -v $AGENT_BUS_HOME/config:/config:ro -v $AGENT_BUS_HOME/monitor-config:/monitor-config \
  --entrypoint agent-busctl $IMG monitor init /monitor-config --agent monitor@alice
```

A broker folder from before busd needs `agent-busctl service init` and a restart of the broker
first ([An existing setup](#an-existing-setup)); `monitor init` stops without the monitor's logins.

**Start it.** The monitor shares the broker's network, so it reaches the broker as `localhost`.
Inside the container it listens on `0.0.0.0`, which is not a loopback address: that is what
`--behind-proxy` is for, and it insists on the password. The host still publishes the port on its
loopback only (step 3), so no proxy is needed for local use:

```sh
podman run -d --name agent-bus-monitor --network container:agent-bus $U \
  -v $AGENT_BUS_HOME/monitor-config:/config --entrypoint bus-monitor \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 -e BUS_WEB_BEHIND_PROXY=true -e BUS_AGENT=monitor@alice \
  $IMG
curl -s -u me:'a long password' http://127.0.0.1:8080/api/v1/status
```

```text
{"bus":"live","agent":"monitor@alice","can_send":true,"directory":{"version":"4687105b/2","head_at":"2026-11-25T19:20:32.209939671Z"},"password_set":true}
```

**Checkpoint:** open <http://localhost:8080> and log in. Without the password hash the monitor
refuses to start (`--behind-proxy needs the web login password`; see `podman logs agent-bus-monitor`);
restart it after changing the password. The hash is `monitor-web/password.bcrypt` in the monitor's
folder (older versions: `monitor/`).

**Bus owner.** The monitor's identity is also the bus owner for channels: busd lets it manage every
channel and create channels without a limit ([agent channels](design/agent-channels.md)). That comes
from the `-e BUSD_BUS_OWNER=monitor@alice` of step 3 (a comma-separated list of agent IDs), not from
the monitor sending as that identity. A broker started without it must be recreated with it: run
step 3's `podman run` again with the option (`podman rm -f --depend agent-bus` first, which also
removes the monitor and the proxy; start them again afterwards). `-e BUSD_CHANNEL_CREATE_LIMIT=<n>`
changes the 5 channels an agent may create a day (0: no limit).

## Publish the monitor through a TLS proxy

To reach the page from other machines, put nginx or Traefik in front. The proxy joins the broker's
network, like the monitor, so it reaches the monitor at `127.0.0.1:8080`, a loopback peer the
monitor trusts for the forwarding headers by default. The NATS ports 4222 and 4223 are TLS with the
bus's own CA and go straight to the broker, never through the HTTP proxy.

Three things differ from a normal start, and [`proxy.sh`](#the-start-scripts) does the last one:

1. **Publish the proxy's port on the broker container**, because the proxy shares its network: add
   `-p 8443:443` to the `podman run` of step 3 (`start.sh`: `PROXY_PORT=8443`). A rootless user
   cannot publish 443 unless `net.ipv4.ip_unprivileged_port_start` allows it, so this guide uses 8443.
2. **Tell the monitor its public address**: `-e BUS_WEB_PUBLIC_URL=https://bus.example.com:8443` on
   the monitor's `podman run` (`monitor.sh`: `PUBLIC_URL`). The URL carries the port you publish.
3. **Start the proxy** with a certificate in `$AGENT_BUS_HOME/tls/` (`fullchain.pem`, `privkey.pem`;
   [docker.md](docker.md#publish-the-monitor-through-a-tls-proxy) shows how to make a self-signed
   pair for a trial).

```sh
# from the checkout, which holds the configs
PROXY=nginx PROXY_CONFIGS=$PWD/deploy/proxy $AGENT_BUS_HOME/proxy.sh        # or PROXY=traefik
```

The script runs the commands below with copies of the configs in `$AGENT_BUS_HOME/proxy/` that
name the monitor as `127.0.0.1:8080` (in the configs of `deploy/proxy/` it is `agent-bus:8080`, a
compose service name):

| Proxy | Command | Config |
|---|---|---|
| nginx | `podman run -d --name agent-bus-proxy --network container:agent-bus -v $AGENT_BUS_HOME/proxy/nginx.conf:/etc/nginx/conf.d/default.conf:ro -v $AGENT_BUS_HOME/tls:/etc/nginx/certs:ro docker.io/library/nginx:1.27` | [nginx.conf](../deploy/proxy/nginx.conf) |
| Traefik | the same with `traefik.yaml` at `/etc/traefik/traefik.yaml`, `traefik-dynamic.yaml` at `/etc/traefik/dynamic.yaml`, the folder at `/etc/traefik/certs`, image `docker.io/library/traefik:v3.2` | [traefik.yaml](../deploy/proxy/traefik.yaml), [traefik-dynamic.yaml](../deploy/proxy/traefik-dynamic.yaml) |

**Checkpoint:** `curl -sk -u me:'a long password' https://bus.example.com:8443/api/v1/status` answers
the status line above, and in the browser a message sent from another agent appears without a
reload. If the page only updates after a reload, the proxy is buffering `/api/v1/events`.

## The start scripts

For day-to-day starts, three scripts in `deploy/podman/` do the `podman run` of the steps above:

| Script | Does | Settings |
|---|---|---|
| `start.sh` | the broker (step 3) and its `init` (step 4) | `IMG`, `MONITOR_AGENT` (the bus owner), `PROXY_PORT`, `BUS_MAX_AGE`, `BUS_MAX_BYTES` |
| `monitor.sh` | `monitor init` and the monitor's `run` | `IMG`, `MONITOR_AGENT`, `PUBLIC_URL` |
| `proxy.sh` | the proxy | `PROXY` (`nginx` or `traefik`), `PROXY_CONFIGS` |

Copy them into the folder that holds `config/`, `data/` and `monitor-config/` (`$AGENT_BUS_HOME`);
they use the folder they are in, or `AGENT_BUS_HOME`. All three read the same optional file `env`
next to them, in shell syntax; a variable in the environment wins over the file. `IMG` defaults to
`agent-messenger:dev`, so set it, and `MONITOR_AGENT` defaults to `monitor@$USER`:

```sh
cp deploy/podman/{start,monitor,proxy}.sh $AGENT_BUS_HOME/
printf '%s\n' "IMG=$IMG" 'MONITOR_AGENT=monitor@alice' > $AGENT_BUS_HOME/env
$AGENT_BUS_HOME/start.sh
$AGENT_BUS_HOME/monitor.sh
```

**Retention.** `start.sh` runs `init`, which applies the message log's limits, 90 days and 1 GiB
unless you say otherwise. To keep other limits, put them in `env` (`-1` is no size limit); the
script passes them to `init` every time it recreates the broker:

```sh
printf '%s\n' 'BUS_MAX_AGE=720h' 'BUS_MAX_BYTES=536870912' >> $AGENT_BUS_HOME/env
```

**Proxy.** `PROXY_PORT=8443` (publishes the proxy's 443), `PUBLIC_URL=https://bus.example.com:8443`
and `PROXY=traefik` for Traefik belong in `env` too. `proxy.sh` reads the configs from
`deploy/proxy/` of the repository; from a copy of the script, set `PROXY_CONFIGS` to that folder,
for example `PROXY_CONFIGS=/path/to/checkout/deploy/proxy` in `env`.

All three replace a container of the same name, so they recreate: use them after an upgrade or a
changed option. `start.sh` removes the broker with `podman rm --depend`, which also removes the
monitor and the proxy that share its network, so run `monitor.sh` and `proxy.sh` again after it.

## Day to day

| Task | Command |
|---|---|
| Restart, same image and options (reloads changed files in the folders) | `podman restart agent-bus` |
| Recreate: new image, changed ports or environment; also applies the message log's limits (the ones in `env`) | `podman pull $IMG`, then `start.sh`, `monitor.sh` and `proxy.sh` of `$AGENT_BUS_HOME`, in this order |
| Broker log | `podman logs -f agent-bus` |
| Stop | `podman stop agent-bus-proxy agent-bus-monitor agent-bus` |
| Remove the broker and what shares its network (the folders keep all state) | `podman rm -f --depend agent-bus` |

`podman restart` keeps the container's image: it does not pick up a pulled one. Agents reconnect on
their own after either.

## Backup and restore

The state is three folders, plus your settings and, with a proxy, its certificate and configs. One
copy taken while the containers are stopped is a consistent backup of everything:

| What | Where | What you lose without it |
|---|---|---|
| `config/` | `$AGENT_BUS_HOME` | the CA, every identity and login, the registry: the bus is gone |
| `data/` | `$AGENT_BUS_HOME` | the messages (JetStream storage) |
| `monitor-config/` | `$AGENT_BUS_HOME` | the monitor's logins, its web password hash and `monitor-web/prefs.json` (favourites, subscriptions, folded headings) |
| `env` and the three scripts | `$AGENT_BUS_HOME` | your settings: image, retention, proxy port and URL |
| `tls/`, `proxy/` | `$AGENT_BUS_HOME` | the proxy's certificate and key, and the copies of its configs |

The backup holds `ca.key`, every agent's key and password and the proxy's key. The commands below
create the archive readable by you only (`umask 077`); keep it that way, and store it encrypted off
the machine.

**Back up.** The brief stop lets agents reconnect on their own afterwards:

```sh
podman stop agent-bus-proxy agent-bus-monitor agent-bus
( umask 077; cd $AGENT_BUS_HOME && tar -cz $(ls -d config data monitor-config env tls proxy *.sh 2>/dev/null) \
    | cat > ~/agent-bus-$(date +%F).tar.gz )
podman start agent-bus agent-bus-monitor      # and agent-bus-proxy
```

**Restore** onto an empty setup (a new machine, or after losing the folders). The archive brings
the scripts and `env` with it. Containers that are left over keep their old mounts, so remove them
first. The scripts pass the retention of `env` to `init`, so the restored bus keeps its limits:

```sh
podman rm -f --depend agent-bus
mkdir -p $AGENT_BUS_HOME
tar -C $AGENT_BUS_HOME -xzf ~/agent-bus-2026-11-25.tar.gz
rm -f $AGENT_BUS_HOME/config/enroll/directory.json    # busd rebuilds it; see below
$AGENT_BUS_HOME/start.sh
$AGENT_BUS_HOME/monitor.sh
$AGENT_BUS_HOME/proxy.sh                               # if you use a proxy
```

```text
api-client@alice  active  SHA256:svOMoOFpqX1Eyk8GTKN5n5PxeZW4Hm+JBH0Gd+HEY5s
lib-x@bob         active  SHA256:bSKbqdcEuwIPWPMq0upl1QfjePu2aOAaqmtni7i1z3k
monitor@alice     active  SHA256:3QoowDXNm0f536tTYbUqDMqUv9lXggTiecWKuysP4cQ
```

**Checkpoint:** `agent-busctl --direct agent list` (the output above) shows the agents of the backup,
the monitor answers through the proxy, and agents reconnect without changes. Messages sent after
the backup are not in it.

- **`enroll/directory.json`** is busd's copy of the directory with its revision. Restoring an older
  copy makes agents refuse it as a rollback ([troubleshooting](troubleshooting.md#unknown-agent)),
  so delete it as above.
- **`nats/auth.conf.confirmed`** is the last `auth.conf` the server confirmed loading; restore it
  together with the rest of `nats/`, or delete it and `auth.conf` is used.
- **Agent machines** hold their own keys and certificates; back those up separately if you want to
  keep an identity across a lost disk.

### Restoring an older backup

A backup brings back the state of its day, including credentials you have since taken away.
Reconcile before agents reconnect: publish the ports on `127.0.0.1` only while you do it (edit the
`-p` options in your copy of `start.sh`), or use a firewall, and open them afterwards.

| Since the backup | Effect of the restore | What to do |
|---|---|---|
| an agent was removed | it is `active` again, with a working account and key | `agent-busctl --direct agent remove <agent-id>` again; agents that learned the revocation keep refusing its key, new ones do not |
| a key was replaced (`ca sign --replace`) | the old, revoked key works again; the machine with the new key cannot log in | enroll the machine again and approve it with `ca sign <code> --replace` |
| a certificate was renewed or switched | the old certificate works again; the one on the machine does not | `cert issue <agent-id> --switch` and copy the new pair, or enroll again |
| an agent was enrolled | it is missing and cannot log in | enroll it again |
| a channel was created or joined | it is missing | create or join it again |

`agent-busctl --direct agent list` against your own record of removals and replacements is the
check.

## Upgrade

1. Take a [backup](#backup-and-restore).
2. Set the retention you want in `env` (`BUS_MAX_AGE`, `BUS_MAX_BYTES`; see
   [the start scripts](#the-start-scripts)). `start.sh` runs `init`, and without them messages
   older than 90 days or beyond 1 GiB are deleted at once ([agent-busctl.md](agent-busctl.md#init)).
3. `podman pull $IMG`, then recreate: `start.sh`, `monitor.sh`, `proxy.sh`.

**Checkpoint:** `podman logs agent-bus` ends in `Server is ready`, and the page loads through the
proxy. Steps that only one release needs are in [upgrade-0.2.md](upgrade-0.2.md) (0.1 to 0.2) and
[upgrade-0.3.md](upgrade-0.3.md) (0.2 to 0.3, the next release, not tagged yet): apply them in order.

## Build locally

For development, or a platform without a release image. `make image` builds
`agent-messenger:dev` for this machine with podman (docker if its daemon answers):

```sh
make image
IMG=agent-messenger:dev
```

Every command above then uses the local image; the start scripts default to it.
