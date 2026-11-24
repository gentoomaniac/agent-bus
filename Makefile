.PHONY: check fmt-check vet test itest

check: fmt-check vet test

fmt-check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

vet:
	go vet ./...

test:
	go test ./...

# Needs podman and starts and stops its own pod (see internal/itest).
itest:
	nice go test -tags integration -timeout 10m -count=1 ./internal/itest/
