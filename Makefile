.PHONY: build fmt lint test verify

build:
	go build -trimpath -o bin/alib-fetcher ./cmd/alib-fetcher

fmt:
	golangci-lint fmt

lint:
	golangci-lint run ./...

test:
	go test -race -shuffle=on -count=1 ./...

verify: fmt lint test build
