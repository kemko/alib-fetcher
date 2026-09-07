.DEFAULT_GOAL := verify

BINARY := bin/alib-fetcher
COVERAGE_FILE := coverage.out
COVERAGE_THRESHOLD := 80
GOLANGCI_LINT_VERSION := v2.12.2
GOVULNCHECK_VERSION := v1.7.0
GO_VERSION := $(shell go env GOVERSION)
TOOLS_DIR := $(CURDIR)/bin/tools/$(GO_VERSION)/golangci-lint-$(GOLANGCI_LINT_VERSION)
GOLANGCI_LINT := $(TOOLS_DIR)/golangci-lint
GOVULNCHECK_DIR := $(CURDIR)/bin/tools/$(GO_VERSION)/govulncheck-$(GOVULNCHECK_VERSION)
GOVULNCHECK := $(GOVULNCHECK_DIR)/govulncheck

.PHONY: build coverage fmt fmt-check govulncheck lint test tools verify

tools: $(GOLANGCI_LINT) $(GOVULNCHECK)

$(GOLANGCI_LINT):
	mkdir -p "$(TOOLS_DIR)"
	GOBIN="$(TOOLS_DIR)" go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(GOVULNCHECK):
	mkdir -p "$(GOVULNCHECK_DIR)"
	GOBIN="$(GOVULNCHECK_DIR)" go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

govulncheck: $(GOVULNCHECK)
	"$(GOVULNCHECK)" ./...

build:
	mkdir -p $(dir $(BINARY))
	go build -trimpath -o $(BINARY) ./cmd/alib-fetcher

fmt: tools
	"$(GOLANGCI_LINT)" fmt

fmt-check: tools
	@diff="$$("$(GOLANGCI_LINT)" fmt --diff)"; status=$$?; \
	if [ $$status -ne 0 ]; then exit $$status; fi; \
	if [ -n "$$diff" ]; then printf '%s\n' "$$diff"; exit 1; fi

lint: tools
	"$(GOLANGCI_LINT)" run ./...

test:
	go test -race -shuffle=on -count=1 ./...

coverage:
	go test -coverpkg=./... -covermode=atomic -coverprofile="$(COVERAGE_FILE)" -count=1 ./...
	@coverage="$$(go tool cover -func="$(COVERAGE_FILE)" | awk '/^total:/ { sub(/%/, "", $$3); print $$3 }')"; \
	if [ -z "$$coverage" ]; then printf '%s\n' 'coverage total unavailable'; exit 1; fi; \
	awk -v coverage="$$coverage" -v threshold="$(COVERAGE_THRESHOLD)" 'BEGIN { \
		if (coverage < threshold) { \
			printf "coverage %.1f%% is below %.1f%% threshold\n", coverage, threshold; \
			exit 1; \
		} \
		printf "coverage %.1f%% meets %.1f%% threshold\n", coverage, threshold; \
	}'

verify: fmt-check lint test govulncheck build
