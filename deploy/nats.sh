#!/usr/bin/env bash
# Installs, starts and stops nats-server as a plain process. This script and nats-server.conf are
# the only runtime-specific files of the project (design.md §2).
#
#   deploy/nats.sh install         build the pinned nats-server into $BUS_NATS_SERVER
#   deploy/nats.sh up              start the server and wait until it is ready
#   deploy/nats.sh down            stop it, keeping its data
#   deploy/nats.sh down --purge    stop it and delete its data too
#
# Settings, as environment variables:
#   BUS_CONFIG_DIR     default ~/.config/agent-bus
#   BUS_NATS_SERVER    default $BUS_CONFIG_DIR/bin/nats-server
#   BUS_NATS_PORT      default 4222
#   BUS_MONITOR_PORT   default 8222, served on 127.0.0.1 only
#   BUS_LISTEN_IP      default 0.0.0.0, the LAN; 127.0.0.1 keeps the broker on this machine
#   BUS_DATA_DIR       default $BUS_CONFIG_DIR/nats/data, the JetStream storage
set -euo pipefail

export BUS_CONFIG_DIR="${BUS_CONFIG_DIR:-$HOME/.config/agent-bus}"
export BUS_NATS_PORT="${BUS_NATS_PORT:-4222}"
export BUS_MONITOR_PORT="${BUS_MONITOR_PORT:-8222}"
export BUS_LISTEN_IP="${BUS_LISTEN_IP:-0.0.0.0}"
export BUS_DATA_DIR="${BUS_DATA_DIR:-$BUS_CONFIG_DIR/nats/data}"
readonly natsServer="${BUS_NATS_SERVER:-$BUS_CONFIG_DIR/bin/nats-server}"

readonly natsServerVersion=v2.15.3
readonly template="$(dirname "$0")/nats-server.conf"
readonly natsDir="$BUS_CONFIG_DIR/nats"
readonly conf="$natsDir/nats-server.conf"
readonly pidFile="$natsDir/nats-server.pid"
readonly logFile="$natsDir/nats-server.log"
readonly readyTimeoutSeconds=30
readonly stopTimeoutSeconds=15

install() {
  # `go install` names the binary after the package, so the target must be called nats-server.
  [[ "$(basename "$natsServer")" == nats-server ]] || {
    echo "BUS_NATS_SERVER must end in /nats-server, got $natsServer" >&2
    exit 1
  }
  GOBIN="$(dirname "$natsServer")" go install "github.com/nats-io/nats-server/v2@$natsServerVersion"
  echo "installed $("$natsServer" --version)"
}

# livePid prints the pid in the pid file and succeeds if that process exists. A file that holds
# anything but a positive number is stale: `kill 0` or `kill -1` would signal far more than the server.
livePid() {
  local pid
  pid="$(cat "$pidFile" 2>/dev/null)" || return 1
  [[ "$pid" =~ ^[1-9][0-9]*$ ]] && kill -0 "$pid" 2>/dev/null && echo "$pid"
}

up() {
  for file in broker.pem broker.key nats/auth.conf nats/control-auth.conf; do
    [[ -f "$BUS_CONFIG_DIR/$file" ]] || {
      echo "missing $BUS_CONFIG_DIR/$file: run 'agent-busctl tls init' and 'agent-busctl admin init' first" >&2
      exit 1
    }
  done
  [[ -x "$natsServer" ]] || {
    echo "missing $natsServer: run '$0 install' first" >&2
    exit 1
  }
  local pid
  if pid="$(livePid)"; then
    echo "already running (pid $pid)" >&2
    exit 1
  fi

  mkdir -p "$BUS_DATA_DIR"
  envsubst '${BUS_LISTEN_IP} ${BUS_NATS_PORT} ${BUS_MONITOR_PORT} ${BUS_CONFIG_DIR} ${BUS_DATA_DIR}' <"$template" >"$conf"
  "$natsServer" -t -c "$conf" >/dev/null
  nohup "$natsServer" -c "$conf" -P "$pidFile" >>"$logFile" 2>&1 &
  local server=$!

  for ((waited = 0; waited < readyTimeoutSeconds; waited++)); do
    if curl -fs "http://127.0.0.1:$BUS_MONITOR_PORT/healthz" >/dev/null; then
      echo "broker ready on port $BUS_NATS_PORT"
      return
    fi
    sleep 1
  done
  echo "broker not ready after ${readyTimeoutSeconds}s; see $logFile" >&2
  kill "$server" 2>/dev/null || true
  wait "$server" 2>/dev/null || true
  exit 1
}

down() {
  local pid
  if pid="$(livePid)"; then
    kill "$pid"
    for ((waited = 0; waited < stopTimeoutSeconds; waited++)); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "$pid" 2>/dev/null; then
      echo "nats-server (pid $pid) did not stop after ${stopTimeoutSeconds}s" >&2
      exit 1
    fi
  fi
  rm -f "$pidFile"
  if [[ "${1:-}" == "--purge" ]]; then
    rm -rf "$BUS_DATA_DIR"
  fi
}

case "${1:-}" in
install) install ;;
up) up ;;
down) down "${2:-}" ;;
*)
  echo "usage: $0 install | up | down [--purge]" >&2
  exit 2
  ;;
esac
