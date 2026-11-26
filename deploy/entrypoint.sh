#!/bin/sh
# Container entrypoint: renders the nats-server configuration (what deploy/nats.sh up does), starts
# busd beside it and runs nats-server as this process, so signals reach it and `agent-busctl
# --direct` can reload it. Extra arguments go to nats-server. Settings: BUS_CONFIG_DIR,
# BUS_DATA_DIR, BUS_NATS_PORT, BUS_MAPPED_PORT, BUS_MONITOR_PORT, BUS_LISTEN_IP (see
# docs/docker.md), BUS_BUSD=off to run the broker without busd (docs/podman.md).
set -eu

export BUS_NATS_PORT="${BUS_NATS_PORT:-4222}"
export BUS_MAPPED_PORT="${BUS_MAPPED_PORT:-4223}"
export BUS_MONITOR_PORT="${BUS_MONITOR_PORT:-8222}"
export BUS_LISTEN_IP="${BUS_LISTEN_IP:-0.0.0.0}"

natsDir="$BUS_CONFIG_DIR/nats"
conf="$natsDir/nats-server.conf"

for file in ca.pem broker.pem broker.key nats/auth.conf nats/control-auth.conf; do
  [ -f "$BUS_CONFIG_DIR/$file" ] || {
    echo "missing $BUS_CONFIG_DIR/$file: run 'agent-busctl tls init' and 'agent-busctl admin init' in the container first (docs/docker.md)" >&2
    exit 1
  }
done

mkdir -p "$BUS_DATA_DIR"
envsubst '${BUS_LISTEN_IP} ${BUS_NATS_PORT} ${BUS_MAPPED_PORT} ${BUS_MONITOR_PORT} ${BUS_CONFIG_DIR} ${BUS_DATA_DIR}' \
  </usr/local/share/agent-bus/nats-server.conf >"$conf"
nats-server -t -c "$conf" >/dev/null

# busd (enrollment and approval, docs/podman.md) holds the CA key, so it starts only where
# `agent-busctl tls init` made one. It reaches the broker as localhost, like agent-busctl in this
# container, and is started again if it stops: after `agent-busctl cert issue --admin` it must
# (`pkill busd`) to read its new login.
if [ -f "$BUS_CONFIG_DIR/ca.key" ] && [ "${BUS_BUSD:-on}" != "off" ]; then
  (
    while :; do
      busd -vv --broker "localhost:$BUS_NATS_PORT" --mapped-port "$BUS_MAPPED_PORT" \
        --monitor-url "http://127.0.0.1:$BUS_MONITOR_PORT" || true
      sleep 2
    done
  ) &
fi

# The pid file is where agent-busctl looks for the server to signal a reload.
exec nats-server -c "$conf" -P "$natsDir/nats-server.pid" "$@"
