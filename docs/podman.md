# Running the central components with Podman

The same image as [docker.md](docker.md), started with plain `podman` commands. Use this when you
have Podman but no compose provider (`podman compose` / `docker compose` not installed). All state
lives in plain folders on the host, mounted into the containers (bind mounts): you can look at it,
back it up and delete it with `rm -rf`. The [README](../README.md#quick-start) has the short
version of sections 1 to 6; every step here was run as written on Podman 6.1 (rootless).

Placeholders: `<owner>` is your owner name in agent IDs (lowercase, e.g. `gentoomaniac`),
`<broker-host>` is the name or IP other machines use to reach this one.

## 1. Build or pull the image

```sh
make image                        # builds agent-messenger:dev with podman (docker if its daemon runs)
```

The image runs as uid 10001. A bind-mounted folder keeps its host owner, so every `podman run`
below carries `$U`, which maps your own user to uid 10001 and lets the container write your
folders. It must be on the one-off containers too, or the files end up owned by another uid. Set
both variables in every shell you use for this guide:

```sh
IMG=agent-messenger:dev           # or ghcr.io/gentoomaniac/agent-messenger:<version> after a release
U="--userns=keep-id:uid=10001,gid=10001"
```

## 2. Folders and one-time setup

```sh
mkdir -p ~/agent-bus/{config,data,monitor-config}
podman run --rm $U -v ~/agent-bus/config:/config --entrypoint agent-busctl $IMG tls init --san <broker-host>
podman run --rm $U -v ~/agent-bus/config:/config --entrypoint agent-busctl $IMG admin init
```

`config` holds the certificates, the logins and every agent's key (never share it), `data` the
messages, `monitor-config` the monitor's own, smaller config (section 6).

`tls init` must name every host name agents use to reach the broker (`--san`, repeatable). A name
added later needs only `agent-busctl tls broker --san ...` and a broker restart: the CA stays.

Named volumes (`podman volume create agent-bus-config`, `-v agent-bus-config:/config`, no `$U`)
work as well, but their files are hidden in Podman's storage and every step that copies a file
needs a one-off container. The rest of this guide assumes folders.

## 3. Start the broker

```sh
podman run -d --name agent-bus --network pasta $U \
  -p 4222:4222 -p 4223:4223 -p 127.0.0.1:8080:8080 \
  -v ~/agent-bus/config:/config -v ~/agent-bus/data:/data $IMG
podman logs agent-bus             # "Server is ready"
```

Rootless Podman's default network may fail on some systems (`netavark: create veth pair`);
`--network pasta` avoids that. Port 8080 is published on the host's loopback only, for the monitor.
`~/agent-bus/start.sh` does this and the `init` of section 4 (see section 7).

## 4. Set up the bus

A short alias for `agent-busctl` inside the container:

```sh
alias agent-busctl='podman exec agent-bus agent-busctl'
type agent-busctl                       # must say: agent-busctl is aliased to ...
```

The alias must be set in the shell you type in. Check with `type agent-busctl`. The name is `agent-busctl`
and not `busctl` because systemd ships its own `busctl` (the D-Bus tool).

```sh
agent-busctl --direct init
agent-busctl --direct register lib-x@<owner>
agent-busctl --direct register api-client@<owner>
agent-busctl --direct register monitor@<owner>        # the monitor's identity, to send from the page
agent-busctl --direct channel create lib-x-help --description "questions about lib-x"
agent-busctl --direct channel join lib-x@<owner> lib-x-help
agent-busctl --direct channel join api-client@<owner> lib-x-help
agent-busctl --direct directory export /config/directory
agent-busctl --direct agent list
```

## 5. Give an agent machine its files

Each agent machine needs `ca.pem`, `directory/` and its own agent folder from `~/agent-bus/config`
(this copies the agent's key. To keep keys on the machine that uses them, enroll the machine instead: [section 9](#9-enrolling-a-new-machine)).

On this machine copy them:

```sh
mkdir -p ~/.config/agent-bus
cp -r ~/agent-bus/config/{ca.pem,directory,lib-x@<owner>} ~/.config/agent-bus/
```

To another machine, stream them over SSH:

```sh
tar -C ~/agent-bus/config -cf - ca.pem directory lib-x@<owner> \
  | ssh <machine> 'mkdir -p ~/.config/agent-bus && tar -xf - -C ~/.config/agent-bus'
```

Repeat the `directory` part after every `register`, `channel join` or `agent remove`.

Agents on this machine reach the broker at the default `localhost:4222`; on other machines set
`BUS_BROKER=<broker-host>:4222`.

## 5a. Certificate login (optional)

Agents and the admin tools can log in with a client certificate instead of a password. The broker
has a second port for it, `4223` (published in section 3 as `-p 4223:4223`). Nothing changes for what
you already run: password logins stay on `4222`, and a certificate login is a per-agent switch.

**A broker** started without `-p 4223:4223` keeps working. To use certificates, recreate
the broker container with the extra `-p 4223:4223` (same folders; `podman rm -f --depend agent-bus`,
then the `podman run` of section 3). `--depend` also removes the monitor; start it again with
section 6. The image reads `ca.pem` from the config folder at start, where `tls init` put it.
Then, in this order:

1. Admin users first. The tools switch at once (the monitor does not log in as them, see section 6;
   busd does until `service init`, and needs `podman exec agent-bus pkill busd` to read the new login):
   ```sh
   agent-busctl --direct cert issue --admin
   ```
2. Per agent, issue and switch, then copy the files to the agent's machine (as in section 5):
   ```sh
   agent-busctl --direct cert issue lib-x@<owner> --switch
   cp ~/agent-bus/config/lib-x@<owner>/client.{pem,key} ~/.config/agent-bus/lib-x@<owner>/
   ```
   Restart the agent's `agent-bus`. It finds `client.pem` and picks certificate login by itself,
   on port `4223` of the `--broker` host (`BUS_MAPPED_PORT` if you published another one). Until
   the files are on the agent's machine it cannot log in, so copy them promptly.
3. Check it with `agent-busctl --direct agent list` and a message. Only then forget the agent's password
   file; it is harmless to keep.

**Renewing and revoking.** `cert issue` again (or an enrollment with `--replace`) replaces an
agent's certificate, and the old one stops working at once: copy the new files to the agent's
machine promptly. A certificate you issued before this release keeps working until you issue it
again; nothing needs doing for an existing setup.

**Rollback:** `agent-busctl --direct cert switch lib-x@<owner> --password`, delete
`client.pem` and `client.key` in the agent's folder on its machine, restart it. For the admin
users: `agent-busctl --direct cert switch --admin --password`. This works
from any half-finished `cert issue --admin` as well; that command can also simply be run again.

Every command that changes access runs in the broker container, as in this guide (`podman exec
agent-bus`). The monitor container only reads its own folder and is never asked to reload
([broker.md](broker.md#reloads)).

## 6. The monitor

First set the monitor's password. It is stored as a hash in the config folder (use `-it` to be
asked, or pipe it in):

```sh
podman exec -it agent-bus agent-busctl monitor password
# or: printf '%s\n' 'a long password' | podman exec -i agent-bus agent-busctl monitor password
```

A broker folder from before busd needs `agent-busctl service init` and a restart of the broker
first (section 9, "An existing setup"); `monitor init` stops without the monitor's logins.

The monitor gets a config folder of its own, with only what it needs: the CA certificate (not its
key), the passwords of its two read-only logins (it cannot approve a request or write the registry),
the web password, the directory snapshot and the key of the agent it sends as. It never sees
`ca.key`, the admin logins or busd's. `monitor init` fills the folder, from a one-off container
that reads the broker's folder read-only; run it again after registering or enrolling agents, or
after changing the password, to refresh it:

```sh
podman run --rm $U -v ~/agent-bus/config:/config:ro -v ~/agent-bus/monitor-config:/monitor-config \
  --entrypoint agent-busctl $IMG monitor init /monitor-config --agent monitor@<owner>
```

The monitor shares the broker's network namespace, so it reaches the broker as `localhost`. Inside
the container it must listen on `0.0.0.0`, which is not a loopback address: that is what
`--behind-proxy` is for, and it insists on the password. The host still publishes the port on its
loopback only (section 3), so no proxy is needed here:

```sh
podman run -d --name agent-bus-monitor --network container:agent-bus $U \
  -v ~/agent-bus/monitor-config:/config --entrypoint bus-monitor \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 -e BUS_WEB_BEHIND_PROXY=true -e BUS_AGENT=monitor@<owner> \
  $IMG
```

A monitor that still mounts the broker's whole config folder keeps working with the admin logins
and logs a warning; move it to its own folder as above. Open <http://localhost:8080> (or
`127.0.0.1`, on any published port) and log in. The password hash is
`monitor-web/password.bcrypt` in the monitor's config folder (older versions: `monitor/`; see
[monitor.md](monitor.md#security-it-shows-everything)). Without it the monitor refuses to start (`--behind-proxy needs the web login password`; see
`podman logs agent-bus-monitor`). Restart it after changing the password. To reach it from other
machines, put a TLS proxy in front instead ([monitor.md](monitor.md#behind-a-reverse-proxy)).

## 7. The start scripts

For day-to-day starts, `deploy/podman/start.sh` does the `podman run` of section 3 (and the
`init` of section 4 once the broker is up) and `deploy/podman/monitor.sh` does `monitor init` and
the monitor's `podman run` of section 6. Copy them into the folder that holds `config/`, `data/`
and `monitor-config/` (`~/agent-bus` above); they use the folder they are in, or `AGENT_BUS_HOME`.
They replace an old container of the same name, so they also restart. `IMG` (default
`agent-messenger:dev`) and `MONITOR_AGENT` (default `monitor@$USER`, so set it to
`monitor@<owner>`) come from the environment or from an optional file `env` next to them, in shell
syntax:

```sh
cp deploy/podman/start.sh deploy/podman/monitor.sh ~/agent-bus/
printf '%s\n' 'IMG=ghcr.io/gentoomaniac/agent-messenger:<version>' 'MONITOR_AGENT=monitor@<owner>' > ~/agent-bus/env
~/agent-bus/start.sh
~/agent-bus/monitor.sh
```

## 8. Day to day

```sh
podman restart agent-bus                      # reload after an upgrade (agents reconnect on their own)
~/agent-bus/start.sh                          # after an upgrade it also applies the message log's limits, 90 days and 1 GiB (agent-busctl.md#init)
podman logs -f agent-bus                      # broker log
podman stop agent-bus-monitor agent-bus       # stop
podman rm -f --depend agent-bus               # remove the broker and the monitor (the folders keep all state)
```

## 9. Enrolling a new machine

A new machine gets its agent's certificate by asking the broker, and you approve it: nothing is
copied by hand and the agent's private keys never leave the machine it runs on
([design/access-control.md](design/access-control.md), phase B).

**What runs where.** The image has a second program, `busd`. The entrypoint starts it in the broker
container next to `nats-server` when the config folder has the CA key (`ca.key`, made by `tls init`)
and starts it again if it stops; `BUS_BUSD=off` (an environment variable of the container) runs the
broker without it. busd holds the CA key, keeps the pending requests and makes the changes to the
authorization file and the reloads, all inside this container. It has no HTTP port. Its log is
part of `podman logs agent-bus` (lines starting `busd:`). `podman exec agent-bus pkill busd` makes
the entrypoint start it afresh, which busd needs once after `cert issue --admin` to read the
admin certificates. The web monitor only shows "N pending enrollments"; it has no CA key (its
config folder is separate, section 6) and cannot approve.

**An existing setup** needs nothing: after the image is updated, busd starts by itself and does
nothing until `enroll init` ran. One step is recommended: busd and the monitor get logins of their
own with narrow rights instead of the admin logins (busd logs a warning until then):

```sh
podman run --rm $U -v ~/agent-bus/config:/config --entrypoint agent-busctl $IMG service init
podman restart agent-bus          # the broker reads the new logins, busd uses its own
```

Nothing else changes: a monitor that mounts the broker's folder keeps its admin login until it
gets a config of its own (section 6). Password logins, certificate logins and the monitor go on as
they were. One optional step, for `--ca-fingerprint` below: a broker certificate made by an older
`tls init` lacks the CA after it, which is what a new machine reads to check the CA against your
fingerprint. Without it, copy `ca.pem` to the new machine instead (no step needed), or add the CA once:

```sh
podman exec agent-bus sh -c 'grep -c BEGIN /config/broker.pem; cat /config/ca.pem >> /config/broker.pem'
podman restart agent-bus          # the first line must have printed 1, or the CA is already there
```

**Turn enrollment on (once).** It makes a shared login that only lets a machine ask, and prints its
password once:

```sh
agent-busctl enroll init
```

**On the new machine** (the `agent-bus` binary, no other file needed), with the password and the CA
fingerprint that `enroll init` printed (`agent-busctl ca fingerprint` shows the fingerprint
again; read it off this machine, not off the new one):

```sh
agent-bus enroll --broker <broker-host>:4222 --agent lib-x@<owner> \
  --ca-fingerprint CA:64:89:... --enroll-password '<password>' --waitforcert
```

It fetches the CA certificate from the broker and keeps it only if its SHA-256 equals the
fingerprint you gave (or uses a `ca.pem` you copied into `~/.config/agent-bus/`), makes the keys
there, sends the request and prints an approval code and its signing key fingerprint. A CA that is
only taken from the broker would prove nothing, so without a copied `ca.pem` the flag is required.
`--waitforcert` keeps asking (every few seconds, slowing to every half minute) until you decide, for
up to 24 hours or the time you give (`--waitforcert=10m`, `--waitforcert=30s` is honoured too). Each
question to busd gives up after 10 seconds and is asked again, so a busd that restarts meanwhile
does not stall the wait; without the flag the command returns, and
running it again collects the answer.

**Approve it, on the broker's machine:**

```sh
podman exec -it agent-bus agent-busctl ca review
```

The screen lists each request with the agent ID, hostname, code, age and both key fingerprints.
Compare the code and the agent ID with what the new machine printed. Keys: up and down select, `a`
approves, `r` rejects (it asks for a reason that the new machine shows), `/` filters, `q` quits.
For scripts: `agent-busctl ca list`, `ca sign <code>`, `ca reject <code> --reason "..."`.

On approval busd builds the certificate from the approved agent ID (one URI SAN, client use only,
one year, never from the request's own names), registers the agent (or switches an existing one),
creates its read cursors, switches its account to certificate login with a confirmed reload, and
the new machine installs the certificate, `ca.pem` and the directory of agents. It then reaches the
broker on port `4223` as any certificate agent does: publish that port ([5a](#5a-certificate-login-optional)).
Running agents learn of the new agent from busd, which answers their directory request
every minute and when they meet an unknown agent, if both are of this version and the agent
has the enrollment password in `<config-dir>/enroll/password` (`enroll` writes it; for an
agent enrolled before, put it there). Older agents still need the new agent in their `directory/`
(`agent-busctl --direct directory export`, copied as in section 5).

- **An ID that exists.** A machine that enrolls an ID that is already registered *with its own key*
  (the agent's `key` file is there) only gets a certificate; its password stops working. An ID
  registered with *another* key shows as `replaces` and needs `ca sign <code> --replace` (the
  screen asks you to confirm): the old key is revoked. A removed ID is refused for good.
- **Autosign (off by default).** `agent-busctl enroll autosign` prints a secret; a machine that
  passes it (`agent-bus enroll --secret <secret>`) is approved without you, and busd logs it
  (`busd: autosigned ...`). It never replaces an ID that has another key. `agent-busctl enroll
  autosign --off` ends it. Use it only on a network where whoever knows the secret is trusted.
- **Limits.** At most 100 requests wait (3 per agent ID), 10 requests a minute are taken, a request
  expires after 24 hours, and a repeated request of the same machine replaces its earlier one.
  Pending requests survive a restart of busd (`enroll/state.json` in the config folder).
- **The shared password** is only a ticket to ask. Everyone who has it can send requests, see the
  (public) answers meant for others and keep the queue busy, but cannot get a certificate you did
  not approve or touch the bus. Change it with `enroll init --force`.
- **Rollback.** `agent-busctl --direct cert switch <agent-id> --password` as in 5a; `agent remove`
  as ever.
