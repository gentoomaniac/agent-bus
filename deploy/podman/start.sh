#!/bin/sh
# Starts the broker (with busd) on the current image. The monitor must be started after it.
#
# The state lives in the folder this script is in (config/, data/, monitor-config/), unless
# AGENT_BUS_HOME names another. Settings come from the environment or from an optional file "env"
# in that folder (shell syntax): IMG (default agent-messenger:dev) and, for the monitor,
# MONITOR_AGENT (default monitor@$USER) and, to publish a TLS proxy (proxy.sh), PROXY_PORT: the host
# port for the proxy's 443. The init below applies the message log's limits: set BUS_MAX_AGE and
# BUS_MAX_BYTES the same way (for example 720h and 536870912; -1 for no size limit) to keep other
# limits than 90 days and 1 GiB. Example: AGENT_BUS_HOME=/data/bus ./start.sh
# Recreating the broker also removes the containers that share its network: start monitor.sh and
# proxy.sh again afterwards.
BASE_PATH="${AGENT_BUS_HOME:-$(cd "$(dirname "$0")" && pwd)}"
# The environment wins over the file.
envIMG="${IMG:-}" envAGENT="${MONITOR_AGENT:-}" envPROXY_PORT="${PROXY_PORT:-}"
envMAX_AGE="${BUS_MAX_AGE:-}" envMAX_BYTES="${BUS_MAX_BYTES:-}"
[ -f "$BASE_PATH/env" ] && . "$BASE_PATH/env"
IMG="${envIMG:-${IMG:-agent-messenger:dev}}"
MONITOR_AGENT="${envAGENT:-${MONITOR_AGENT:-monitor@$USER}}"
PROXY_PORT="${envPROXY_PORT:-${PROXY_PORT:-}}"
export BUS_MAX_AGE="${envMAX_AGE:-${BUS_MAX_AGE:-}}" BUS_MAX_BYTES="${envMAX_BYTES:-${BUS_MAX_BYTES:-}}"
[ -n "$BUS_MAX_AGE" ] || unset BUS_MAX_AGE
[ -n "$BUS_MAX_BYTES" ] || unset BUS_MAX_BYTES
U="--userns=keep-id:uid=10001,gid=10001"

podman rm -f --depend agent-bus 2>/dev/null; podman rm -f agent-bus-monitor 2>/dev/null
# The monitor's identity is the bus owner for busd's channel requests (docs/podman.md).
podman run -d --name agent-bus $U --network pasta -e BUSD_BUS_OWNER="$MONITOR_AGENT" \
  -p 4222:4222 -p 4223:4223 -p 127.0.0.1:8080:8080 ${PROXY_PORT:+-p $PROXY_PORT:443} \
  -v "${BASE_PATH}/config:/config" -v "${BASE_PATH}/data:/data" $IMG

# Wait for the broker, then create or refresh the streams and grants.
for i in $(seq 30); do
  podman exec -e BUS_MAX_AGE -e BUS_MAX_BYTES agent-bus agent-busctl --direct init && exit 0
  sleep 1
done
echo "the broker did not come up: podman logs agent-bus" >&2
exit 1
