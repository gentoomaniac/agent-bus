#!/bin/sh
# Starts a TLS proxy, nginx (default) or Traefik, in the broker's network, in front of the monitor.
# Run it after start.sh (with PROXY_PORT) and monitor.sh (with PUBLIC_URL), see docs/podman.md.
#
# The state lives in the folder this script is in (config/, data/, monitor-config/), unless
# AGENT_BUS_HOME names another. The certificate is tls/fullchain.pem and tls/privkey.pem there.
# PROXY=nginx or PROXY=traefik comes from the environment. The configs of deploy/proxy/ name the
# monitor as agent-bus:8080, which is a compose service name; here the proxy shares the broker's
# network, so a copy in proxy/ names it 127.0.0.1:8080. Run the script again after editing them.
BASE_PATH="${AGENT_BUS_HOME:-$(cd "$(dirname "$0")" && pwd)}"
CONFIGS="${PROXY_CONFIGS:-$(cd "$(dirname "$0")/../proxy" && pwd)}"
PROXY="${PROXY:-nginx}"

mkdir -p "$BASE_PATH/proxy"
sed 's#agent-bus:8080#127.0.0.1:8080#' "$CONFIGS/nginx.conf" > "$BASE_PATH/proxy/nginx.conf"
sed 's#http://agent-bus:8080#http://127.0.0.1:8080#' "$CONFIGS/traefik-dynamic.yaml" > "$BASE_PATH/proxy/traefik-dynamic.yaml"
cp "$CONFIGS/traefik.yaml" "$BASE_PATH/proxy/"

podman rm -f agent-bus-proxy >/dev/null 2>&1
case "$PROXY" in
nginx)
  podman run -d --name agent-bus-proxy --network container:agent-bus \
    -v "${BASE_PATH}/proxy/nginx.conf:/etc/nginx/conf.d/default.conf:ro" \
    -v "${BASE_PATH}/tls:/etc/nginx/certs:ro" \
    docker.io/library/nginx:1.27 ;;
traefik)
  podman run -d --name agent-bus-proxy --network container:agent-bus \
    -v "${BASE_PATH}/proxy/traefik.yaml:/etc/traefik/traefik.yaml:ro" \
    -v "${BASE_PATH}/proxy/traefik-dynamic.yaml:/etc/traefik/dynamic.yaml:ro" \
    -v "${BASE_PATH}/tls:/etc/traefik/certs:ro" \
    docker.io/library/traefik:v3.2 ;;
*)
  echo "PROXY is nginx or traefik, not $PROXY" >&2
  exit 1 ;;
esac
