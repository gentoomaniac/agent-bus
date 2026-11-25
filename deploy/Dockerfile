# The central components: nats-server with the bus configuration, busd, agent-busctl and the web monitor. Built by goreleaser
# (dockers_v2), whose build context holds the binaries in <os>/<arch>/. See docs/docker.md.
FROM docker.io/library/nats:2.15.3-alpine AS nats

FROM docker.io/library/alpine:3.22
ARG TARGETPLATFORM
RUN apk add --no-cache gettext-envsubst \
 && adduser -D -u 10001 bus \
 && mkdir /config /data /monitor-config \
 && chown bus /config /data /monitor-config

COPY --from=nats /usr/local/bin/nats-server /usr/local/bin/nats-server
COPY ${TARGETPLATFORM}/busd ${TARGETPLATFORM}/agent-busctl ${TARGETPLATFORM}/agent-bus ${TARGETPLATFORM}/bus-monitor /usr/local/bin/
COPY deploy/nats-server.conf /usr/local/share/agent-bus/nats-server.conf
COPY deploy/entrypoint.sh /usr/local/bin/entrypoint.sh

# agent-busctl --direct inside the container finds its config, the server binary and the data here.
ENV BUS_CONFIG_DIR=/config \
    BUS_DATA_DIR=/data \
    BUS_NATS_SERVER=/usr/local/bin/nats-server

USER bus
VOLUME ["/config", "/data"]
EXPOSE 4222 4223
ENTRYPOINT ["entrypoint.sh"]
