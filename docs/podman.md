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
podman run --rm -v agent-bus-config:/config --entrypoint busctl $IMG tls init --san localhost --san <broker-host>
podman run --rm -v agent-bus-config:/config --entrypoint busctl $IMG admin init
```

`tls init` must name every host name agents use to reach the broker (`--san`, repeatable).

## 3. Start the broker

```sh
podman run -d --name agent-bus --network pasta \
  -p 4222:4222 -p 127.0.0.1:8080:8080 \
  -v agent-bus-config:/config -v agent-bus-data:/data \
  $IMG
podman logs agent-bus             # "Server is ready"
```

Rootless Podman's default network may fail on some systems (`netavark: create veth pair`);
`--network pasta` avoids that. Port 8080 is published on the host's loopback only, for the monitor.

## 4. Set up the bus

A short alias for `busctl` inside the container:

```sh
alias busctl='podman exec agent-bus busctl'
```

```sh
busctl --direct init
busctl --direct register lib-x@<owner>
busctl --direct register api-client@<owner>
busctl --direct register monitor@<owner>        # the monitor's identity, to send from the page
busctl --direct channel create lib-x-help --description "questions about lib-x"
busctl --direct channel join lib-x@<owner> lib-x-help
busctl --direct channel join api-client@<owner> lib-x-help
busctl --direct directory export /config/directory
busctl --direct agent list
```

## 5. Give an agent machine its files

Each agent machine needs `ca.pem`, `directory/` and its own agent folder from the config volume
(this copies the agent's key: the current MVP way until enrollment exists, see design.md §4c).

```sh
mkdir -p ~/.config/agent-bus
podman run --rm -v agent-bus-config:/config:ro --entrypoint tar $IMG \
  -C /config -cf - ca.pem directory lib-x@<owner> | tar -xf - -C ~/.config/agent-bus
```

On another machine, run the same `podman run … tar -cf -` on the broker host and pipe it over SSH:
`… | ssh <machine> 'mkdir -p ~/.config/agent-bus && tar -xf - -C ~/.config/agent-bus'`.
Repeat the `directory` part after every `register`, `channel join` or `agent remove`.

Agents on this machine reach the broker at the default `localhost:4222`; on other machines set
`BUS_BROKER=<broker-host>:4222`.

## 6. The monitor

First set the monitor's password. It is stored as a hash on the config volume (use `-it` to be
asked, or pipe it in):

```sh
podman exec -it agent-bus busctl monitor password
# or: printf '%s\n' 'a long password' | podman exec -i agent-bus busctl monitor password
```

The monitor shares the broker's network namespace, so it reaches the broker as `localhost`. Inside
the container it must listen on `0.0.0.0`, which is not a loopback address: that is what
`--behind-proxy` is for, and it insists on the password. The host still publishes the port on its
loopback only (step 3), so no proxy is needed here:

```sh
podman run -d --name agent-bus-monitor --network container:agent-bus \
  -v agent-bus-config:/config --entrypoint bus-monitor \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 -e BUS_WEB_BEHIND_PROXY=true -e BUS_AGENT=monitor@<owner> \
  $IMG
```

Open <http://localhost:8080> (or `127.0.0.1`, on any published port) and log in. Without the
password the monitor refuses to start (`--behind-proxy needs a password`; see
`podman logs agent-bus-monitor`). Restart it after changing the password. To reach it from other
machines, put a TLS proxy in front instead ([monitor.md](monitor.md#behind-a-reverse-proxy)).

## 7. Day to day

```sh
podman restart agent-bus                      # reload after an upgrade (agents reconnect on their own)
podman logs -f agent-bus                      # broker log
podman stop agent-bus-monitor agent-bus       # stop
podman rm -f agent-bus-monitor agent-bus      # remove containers (volumes keep all state)
```
