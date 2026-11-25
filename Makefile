.PHONY: check fmt-check vet test itest image

check: fmt-check vet test

fmt-check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

vet:
	go vet ./...

test:
	go test ./...

# The pinned nats-server, built once into bin/ by the deploy script.
bin/nats-server: deploy/nats.sh
	BUS_NATS_SERVER=$(CURDIR)/bin/nats-server deploy/nats.sh install

# Starts and stops its own nats-server (see internal/itest).
itest: bin/nats-server
	BUS_NATS_SERVER=$(CURDIR)/bin/nats-server nice go test -tags integration -timeout 10m -count=1 ./internal/itest/

# The central image for this machine's architecture, tagged agent-messenger:dev (docs/docker.md).
# goreleaser builds the release image from a context of <os>/<arch>/ binaries; this assembles the
# same layout in bin/image. CONTAINER is docker if its daemon answers, else podman.
CONTAINER ?= $(shell docker info >/dev/null 2>&1 && command -v docker || command -v podman || command -v docker)
IMAGE_PLATFORM := linux/$(shell go env GOARCH)

image:
	@test -n "$(CONTAINER)" || { echo "make image needs docker or podman" >&2; exit 1; }
	rm -rf bin/image
	mkdir -p bin/image/$(IMAGE_PLATFORM) bin/image/deploy
	CGO_ENABLED=0 GOOS=linux go build -trimpath -o bin/image/$(IMAGE_PLATFORM)/ ./cmd/busd ./cmd/agent-busctl ./cmd/agent-bus ./cmd/bus-monitor
	cp deploy/entrypoint.sh deploy/nats-server.conf bin/image/deploy/
	$(CONTAINER) build --platform $(IMAGE_PLATFORM) --build-arg TARGETPLATFORM=$(IMAGE_PLATFORM) -f deploy/Dockerfile -t agent-messenger:dev bin/image
