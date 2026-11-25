#!/bin/sh
# Container entrypoint: renders the nats-server configuration (what deploy/nats.sh up does) and
# runs nats-server as this process, so signals reach it and `agent-busctl --direct` can reload it.
# Extra arguments go to nats-server. Settings: BUS_CONFIG_DIR, BUS_DATA_DIR, BUS_NATS_PORT,
# BUS_MONITOR_PORT, BUS_LISTEN_IP (see docs/docker.md).
set -eu

export BUS_NATS_PORT="${BUS_NATS_PORT:-4222}"
export BUS_MONITOR_PORT="${BUS_MONITOR_PORT:-8222}"
export BUS_LISTEN_IP="${BUS_LISTEN_IP:-0.0.0.0}"

natsDir="$BUS_CONFIG_DIR/nats"
conf="$natsDir/nats-server.conf"

for file in broker.pem broker.key nats/auth.conf nats/control-auth.conf; do
  [ -f "$BUS_CONFIG_DIR/$file" ] || {
    echo "missing $BUS_CONFIG_DIR/$file: run 'agent-busctl tls init' and 'agent-busctl admin init' in the container first (docs/docker.md)" >&2
    exit 1
  }
done

mkdir -p "$BUS_DATA_DIR"
envsubst '${BUS_LISTEN_IP} ${BUS_NATS_PORT} ${BUS_MONITOR_PORT} ${BUS_CONFIG_DIR} ${BUS_DATA_DIR}' \
  </usr/local/share/agent-bus/nats-server.conf >"$conf"
nats-server -t -c "$conf" >/dev/null

# The pid file is where agent-busctl looks for the server to signal a reload.
exec nats-server -c "$conf" -P "$natsDir/nats-server.pid" "$@"
