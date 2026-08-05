.DEFAULT_GOAL := verify

BINARY := bin/alib-fetcher
GOLANGCI_LINT_VERSION := v2.12.2

.PHONY: build fmt fmt-check lint test tools verify

tools:
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

build:
	mkdir -p $(dir $(BINARY))
	go build -trimpath -o $(BINARY) ./cmd/alib-fetcher

fmt:
	golangci-lint fmt

fmt-check:
	@diff="$$(golangci-lint fmt --diff)"; status=$$?; \
	if [ $$status -ne 0 ]; then exit $$status; fi; \
	if [ -n "$$diff" ]; then printf '%s\n' "$$diff"; exit 1; fi

lint:
	golangci-lint run ./...

test:
	go test -race -shuffle=on -count=1 ./...

verify: fmt-check lint test build
