.PHONY: build test cover lint shellcheck check-remote check

VERSION ?= dev

build:
	mkdir -p bin
	go build -ldflags "-X main.version=$(VERSION)" -o bin/agr ./cmd/agr

test:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

lint:
	golangci-lint run ./...

shellcheck:
	[ -f internal/remotescript/agr.sh ] || { echo "skip (not yet)"; exit 0; }; shellcheck -s sh internal/remotescript/agr.sh

check-remote:
	[ -f tests/remote/run.sh ] || { echo "skip (not yet)"; exit 0; }; tests/remote/run.sh

check: test lint shellcheck check-remote
