MODULE   := github.com/pipozzz/goliash
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w \
	-X $(MODULE)/pkg/buildinfo.Version=$(VERSION) \
	-X $(MODULE)/pkg/buildinfo.Commit=$(COMMIT) \
	-X $(MODULE)/pkg/buildinfo.Date=$(DATE)

.PHONY: all build test lint fmt license-check tidy clean

all: lint test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/goliash ./cmd/goliash
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/goliash-agent ./cmd/goliash-agent

test:
	go test -race -coverprofile=coverage.out ./...

lint: license-check
	golangci-lint run

fmt:
	golangci-lint fmt

license-check:
	./scripts/check-licenses.sh

tidy:
	go mod tidy

clean:
	rm -rf bin dist coverage.out
