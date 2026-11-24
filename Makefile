.PHONY: check fmt-check vet test

check: fmt-check vet test

fmt-check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

vet:
	go vet ./...

test:
	go test ./...
