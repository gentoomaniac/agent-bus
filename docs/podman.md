# Running the central components with Podman

The same image as [docker.md](docker.md), started with plain `podman` commands. Use this when you
have Podman but no compose provider (`podman compose` / `docker compose` not installed). Every step
below was run as written on Podman 6.1 (rootless).

Placeholders: `<owner>` is your owner name in agent IDs (e.g. `gentoomaniac`), `<broker-host>` is
the name or IP other machines use to reach this one.

## 1. Build or pull the image

```sh
make image                        # builds agent-messenger:dev with podman (docker if its daemon runs)
IMG=agent-messenger:dev           # or ghcr.io/gentoomaniac/agent-messenger:latest after a release
```

## 2. Volumes and one-time setup

```sh
podman volume create agent-bus-config
podman volume create agent-bus-data
podman run --rm -v agent-bus-config:/config --entrypoint agent-busctl "${IMG:?set IMG first, see step 1}" tls init --san localhost --san <broker-host>
podman run --rm -v agent-bus-config:/config --entrypoint agent-busctl "${IMG:?set IMG first, see step 1}" admin init
```

`tls init` must name every host name agents use to reach the broker (`--san`, repeatable).

## 3. Start the broker

```sh
podman run -d --name agent-bus --network pasta \
  -p 4222:4222 -p 4223:4223 -p 127.0.0.1:8080:8080 \
  -v agent-bus-config:/config -v agent-bus-data:/data \
  "${IMG:?set IMG first, see step 1}"
podman logs agent-bus             # "Server is ready"
```

Rootless Podman's default network may fail on some systems (`netavark: create veth pair`);
`--network pasta` avoids that. Port 8080 is published on the host's loopback only, for the monitor.

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

Each agent machine needs `ca.pem`, `directory/` and its own agent folder from the config volume
(this copies the agent's key. To keep keys on the machine that uses them, enroll the machine instead: [section 9](#9-enrolling-a-new-machine)).

```sh
mkdir -p ~/.config/agent-bus
podman run --rm -v agent-bus-config:/config:ro --entrypoint tar "${IMG:?set IMG first, see step 1}" \
  -C /config -cf - ca.pem directory lib-x@<owner> | tar -xf - -C ~/.config/agent-bus
```

On another machine, run the same `podman run … tar -cf -` on the broker host and pipe it over SSH:
`… | ssh <machine> 'mkdir -p ~/.config/agent-bus && tar -xf - -C ~/.config/agent-bus'`.
Repeat the `directory` part after every `register`, `channel join` or `agent remove`.

Agents on this machine reach the broker at the default `localhost:4222`; on other machines set
`BUS_BROKER=<broker-host>:4222`.

## 5a. Certificate login (optional)

Agents and the admin tools can log in with a client certificate instead of a password. The broker
has a second port for it, `4223` (published in step 3 as `-p 4223:4223`). Nothing changes for what
you already run: password logins stay on `4222`, and a certificate login is a per-agent switch.

**An existing setup** started without `-p 4223:4223` keeps working. To use certificates, recreate
the broker container with the extra `-p 4223:4223` (same volumes; `podman rm -f agent-bus`, then
the `podman run` of step 3). The image reads `ca.pem` from the config volume at start, where
`tls init` put it. Then, in this order:

1. Admin users and monitor first (the monitor logs in as them). The tools switch at once:
   ```sh
   agent-busctl --direct cert issue --admin
   podman restart agent-bus-monitor        # it read its login at start
   ```
2. Per agent, issue and switch, then copy the files to the agent's machine:
   ```sh
   agent-busctl --direct cert issue lib-x@<owner> --switch
   podman run --rm -v agent-bus-config:/config:ro --entrypoint tar "${IMG:?set IMG first, see step 1}" \
     -C /config -cf - lib-x@<owner>/client.pem lib-x@<owner>/client.key \
     | tar -xf - -C ~/.config/agent-bus
   ```
   Restart the agent's `agent-bus`. It finds `client.pem` and picks certificate login by itself,
   on port `4223` of the `--broker` host (`BUS_MAPPED_PORT` if you published another one). Until
   the files are on the agent's machine it cannot log in, so copy them promptly.
3. Check it with `agent-busctl --direct agent list` and a message. Only then forget the agent's password
   file; it is harmless to keep.

**Rollback:** `agent-busctl --direct cert switch lib-x@<owner> --password`, delete
`client.pem` and `client.key` in the agent's folder on its machine, restart it. For the admin
users: `agent-busctl --direct cert switch --admin --password` and restart the monitor. This works
from any half-finished `cert issue --admin` as well; that command can also simply be run again.

Every command that changes access runs in the broker container, as in this guide (`podman exec
agent-bus`). The monitor container only reads the volume and is never asked to reload
([broker.md](broker.md#reloads)).

## 6. The monitor

First set the monitor's password. It is stored as a hash on the config volume (use `-it` to be
asked, or pipe it in):

```sh
podman exec -it agent-bus agent-busctl monitor password
# or: printf '%s\n' 'a long password' | podman exec -i agent-bus agent-busctl monitor password
```

The monitor shares the broker's network namespace, so it reaches the broker as `localhost`. Inside
the container it must listen on `0.0.0.0`, which is not a loopback address: that is what
`--behind-proxy` is for, and it insists on the password. The host still publishes the port on its
loopback only (step 3), so no proxy is needed here:

```sh
podman run -d --name agent-bus-monitor --network container:agent-bus \
  -v agent-bus-config:/config --entrypoint bus-monitor \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 -e BUS_WEB_BEHIND_PROXY=true -e BUS_AGENT=monitor@<owner> \
  "${IMG:?set IMG first, see step 1}"
```

Open <http://localhost:8080> (or `127.0.0.1`, on any published port) and log in. Without the
password the monitor refuses to start (`--behind-proxy needs the web login password`; see
`podman logs agent-bus-monitor`). Restart it after changing the password. To reach it from other
machines, put a TLS proxy in front instead ([monitor.md](monitor.md#behind-a-reverse-proxy)).

## 7. A test setup with a bind mount

For a trial it is handier to keep the state in plain folders than in volumes: you can look at it,
copy it and delete it with `rm -rf`. The image runs as uid 10001, so every `podman run` below
maps your own user to it (`--userns=keep-id`), which lets the container write the folders you own.
Do steps 2, 3 and 6 like this instead (`IMG` as in step 1; the commands of steps 4, 5 and the
rest are unchanged, and the alias of step 4 stays):

```sh
mkdir -p ~/bus-trial/config ~/bus-trial/data
U="--userns=keep-id:uid=10001,gid=10001"

podman run --rm $U -v ~/bus-trial/config:/config --entrypoint agent-busctl "${IMG:?set IMG first, see step 1}" tls init --san localhost
podman run --rm $U -v ~/bus-trial/config:/config --entrypoint agent-busctl "${IMG:?set IMG first, see step 1}" admin init

podman run -d --name agent-bus --network pasta $U \
  -p 4222:4222 -p 127.0.0.1:8080:8080 \
  -v ~/bus-trial/config:/config -v ~/bus-trial/data:/data \
  "${IMG:?set IMG first, see step 1}"

podman run -d --name agent-bus-monitor --network container:agent-bus $U \
  -v ~/bus-trial/config:/config --entrypoint bus-monitor \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 -e BUS_WEB_BEHIND_PROXY=true -e BUS_AGENT=monitor@<owner> \
  "${IMG:?set IMG first, see step 1}"
```

`$U` must be on every one of them, the one-off containers too, or the files end up owned by
another uid. The config folder is then the one the agents read, so on this one machine skip the
copy of step 5 and point the agents at it:

```sh
export BUS_CONFIG_DIR=~/bus-trial/config
```

Only for this machine: other machines still get their files as in step 5, and the folder holds
every agent's key, so do not share it.

## 8. Day to day

```sh
podman restart agent-bus                      # reload after an upgrade (agents reconnect on their own)
podman logs -f agent-bus                      # broker log
podman stop agent-bus-monitor agent-bus       # stop
podman rm -f agent-bus-monitor agent-bus      # remove containers (volumes keep all state)
```

## 9. Enrolling a new machine

A new machine gets its agent's certificate by asking the broker, and you approve it: nothing is
copied by hand and the agent's private keys never leave the machine it runs on
([design/access-control.md](design/access-control.md), phase B).

**What runs where.** The image has a second program, `busd`. The entrypoint starts it in the broker
container next to `nats-server` when the config volume has the CA key (`ca.key`, made by `tls init`)
and starts it again if it stops; `BUS_BUSD=off` (an environment variable of the container) runs the
broker without it. busd holds the CA key, keeps the pending requests and makes the changes to the
authorization file and the reloads, all inside this container. It has no HTTP port. Its log is
part of `podman logs agent-bus` (lines starting `busd:`). `podman exec agent-bus pkill busd` makes
the entrypoint start it afresh, which busd needs once after `cert issue --admin` to read the
admin certificates. The web monitor only shows "N pending enrollments"; it holds no CA key and
cannot approve.

**An existing setup** needs nothing: after the image is updated, busd starts by itself and does
nothing until `enroll init` ran. Password logins, certificate logins and the monitor go on as
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
up to 24 hours or the time you give (`--waitforcert=10m`); without it the command returns, and
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
Agents that already run need the new agent in their `directory/` (`agent-busctl --direct
directory export`, copied as in step 5) until busd serves it over the bus.

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
  Pending requests survive a restart of busd (`enroll/state.json` in the config volume).
- **The shared password** is only a ticket to ask. Everyone who has it can send requests, see the
  (public) answers meant for others and keep the queue busy, but cannot get a certificate you did
  not approve or touch the bus. Change it with `enroll init --force`.
- **Rollback.** `agent-busctl --direct cert switch <agent-id> --password` as in 5a; `agent remove`
  as ever.
