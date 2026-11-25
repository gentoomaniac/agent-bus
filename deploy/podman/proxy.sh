#!/bin/sh
# Starts a TLS proxy, nginx (default) or Traefik, in the broker's network, in front of the monitor.
# Run it after start.sh (with PROXY_PORT) and monitor.sh (with PUBLIC_URL), see docs/podman.md.
#
# The state lives in the folder this script is in (config/, data/, monitor-config/), unless
# AGENT_BUS_HOME names another. The certificate is tls/fullchain.pem and tls/privkey.pem there.
# Settings come from the environment or from an optional file "env" in that folder (shell syntax):
# PROXY (nginx or traefik) and PROXY_CONFIGS (the folder with the configs of deploy/proxy/, by
# default the one next to this script's repository folder). The configs name the monitor as
# agent-bus:8080, a compose service name; here the proxy shares the broker's network, so the copies
# in proxy/ name it 127.0.0.1:8080. Run the script again after editing the originals.
# Nothing is replaced until the configs and the certificate checked out.
set -e
BASE_PATH="${AGENT_BUS_HOME:-$(cd "$(dirname "$0")" && pwd)}"
# The environment wins over the file.
envPROXY="${PROXY:-}" envCONFIGS="${PROXY_CONFIGS:-}"
[ -f "$BASE_PATH/env" ] && . "$BASE_PATH/env"
PROXY="${envPROXY:-${PROXY:-nginx}}"
CONFIGS="${envCONFIGS:-${PROXY_CONFIGS:-$(cd "$(dirname "$0")/../proxy" 2>/dev/null && pwd || true)}}"

case "$PROXY" in
nginx | traefik) ;;
*) echo "PROXY is nginx or traefik, not $PROXY" >&2; exit 1 ;;
esac
for file in nginx.conf traefik.yaml traefik-dynamic.yaml; do
  [ -f "$CONFIGS/$file" ] || { echo "no $file in $CONFIGS (set PROXY_CONFIGS to the deploy/proxy folder)" >&2; exit 1; }
done
for file in fullchain.pem privkey.pem; do
  [ -f "$BASE_PATH/tls/$file" ] || { echo "no $BASE_PATH/tls/$file" >&2; exit 1; }
done

mkdir -p "$BASE_PATH/proxy"
sed 's#agent-bus:8080#127.0.0.1:8080#' "$CONFIGS/nginx.conf" > "$BASE_PATH/proxy/nginx.conf.new"
sed 's#http://agent-bus:8080#http://127.0.0.1:8080#' "$CONFIGS/traefik-dynamic.yaml" > "$BASE_PATH/proxy/traefik-dynamic.yaml.new"
mv "$BASE_PATH/proxy/nginx.conf.new" "$BASE_PATH/proxy/nginx.conf"
mv "$BASE_PATH/proxy/traefik-dynamic.yaml.new" "$BASE_PATH/proxy/traefik-dynamic.yaml"
cp "$CONFIGS/traefik.yaml" "$BASE_PATH/proxy/"

podman rm -f agent-bus-proxy >/dev/null 2>&1 || true
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
esac
