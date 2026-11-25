#!/bin/sh
# Refreshes the monitor's own config from the broker's, then starts the monitor.
#
# The state lives in the folder this script is in (config/, data/, monitor-config/), unless
# AGENT_BUS_HOME names another. Settings come from the environment or from an optional file "env"
# in that folder (shell syntax): IMG (default agent-messenger:dev) and, for the monitor,
# MONITOR_AGENT (default monitor@$USER) and, behind a TLS proxy (proxy.sh), PUBLIC_URL: how clients
# reach it, e.g. https://bus.example.com. Example: AGENT_BUS_HOME=/data/bus ./monitor.sh
BASE_PATH="${AGENT_BUS_HOME:-$(cd "$(dirname "$0")" && pwd)}"
# The environment wins over the file.
envIMG="${IMG:-}" envAGENT="${MONITOR_AGENT:-}" envPUBLIC_URL="${PUBLIC_URL:-}"
[ -f "$BASE_PATH/env" ] && . "$BASE_PATH/env"
IMG="${envIMG:-${IMG:-agent-messenger:dev}}"
MONITOR_AGENT="${envAGENT:-${MONITOR_AGENT:-monitor@$USER}}"
PUBLIC_URL="${envPUBLIC_URL:-${PUBLIC_URL:-}}"
U="--userns=keep-id:uid=10001,gid=10001"

mkdir -p "$BASE_PATH/monitor-config"
podman rm -f agent-bus-monitor >/dev/null 2>&1
podman run --rm $U -v "${BASE_PATH}/config:/config:ro" -v "${BASE_PATH}/monitor-config:/monitor-config" \
  --entrypoint agent-busctl $IMG monitor init /monitor-config --agent "$MONITOR_AGENT" || exit 1
podman run -d --name agent-bus-monitor $U --network container:agent-bus \
  -v "${BASE_PATH}/monitor-config:/config" --entrypoint bus-monitor \
  -e BUS_WEB_LISTEN=0.0.0.0:8080 -e BUS_WEB_BEHIND_PROXY=true \
  -e BUS_AGENT="$MONITOR_AGENT" ${PUBLIC_URL:+-e BUS_WEB_PUBLIC_URL=$PUBLIC_URL} \
  $IMG
