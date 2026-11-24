.PHONY: check fmt-check vet test itest

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
