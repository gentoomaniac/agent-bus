#!/usr/bin/env bash
# Starts and stops the broker pod on a machine with Podman. This script and redpanda.yaml are the
# only Podman-specific files of the project.
#
#   deploy/broker.sh up            start the broker and wait until it is ready
#   deploy/broker.sh down          stop it, keeping its data
#   deploy/broker.sh down --purge  stop it and delete its data
#
# Settings, as environment variables:
#   BUS_CONFIG_DIR      default ~/.config/agent-bus
#   BUS_POD_NAME        default agent-bus
#   BUS_KAFKA_PORT      default 9092
#   BUS_ADVERTISE_HOST  default localhost
set -euo pipefail

export BUS_CONFIG_DIR="${BUS_CONFIG_DIR:-$HOME/.config/agent-bus}"
export BUS_POD_NAME="${BUS_POD_NAME:-agent-bus}"
export BUS_KAFKA_PORT="${BUS_KAFKA_PORT:-9092}"
export BUS_ADVERTISE_HOST="${BUS_ADVERTISE_HOST:-localhost}"

readonly manifest="$(dirname "$0")/redpanda.yaml"
readonly readyTimeoutSeconds=90

up() {
  for file in broker.pem broker.key admin/password; do
    [[ -f "$BUS_CONFIG_DIR/$file" ]] || {
      echo "missing $BUS_CONFIG_DIR/$file: run 'busctl tls init' and 'busctl admin init' first" >&2
      exit 1
    }
  done

  # Rootless Podman's default bridge network needs veth devices, which not every kernel allows;
  # pasta needs nothing from the kernel and publishes the port the same way.
  envsubst '${BUS_POD_NAME} ${BUS_CONFIG_DIR} ${BUS_KAFKA_PORT} ${BUS_ADVERTISE_HOST}' <"$manifest" |
    podman kube play --quiet --network pasta -

  for ((waited = 0; waited < readyTimeoutSeconds; waited++)); do
    if podman logs "$BUS_POD_NAME-redpanda" 2>&1 | grep -q "Successfully started Redpanda"; then
      echo "broker ready on $BUS_ADVERTISE_HOST:$BUS_KAFKA_PORT"
      return
    fi
    sleep 1
  done
  echo "broker not ready after ${readyTimeoutSeconds}s; see: podman logs $BUS_POD_NAME-redpanda" >&2
  exit 1
}

down() {
  podman pod rm --force --ignore "$BUS_POD_NAME" >/dev/null
  # kube play keeps the ConfigMap as a volume; it is recreated from the manifest on every `up`.
  podman volume rm --force "$BUS_POD_NAME-config" >/dev/null
  if [[ "${1:-}" == "--purge" ]]; then
    podman volume rm --force "$BUS_POD_NAME-data" >/dev/null
  fi
}

case "${1:-}" in
up) up ;;
down) down "${2:-}" ;;
*)
  echo "usage: $0 up | down [--purge]" >&2
  exit 2
  ;;
esac
