BINARY := bin/dsh
VERSION ?= 0.1.0-dev
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X deekseekharness-go/internal/version.Version=$(VERSION) \
	-X deekseekharness-go/internal/version.Commit=$(COMMIT) \
	-X deekseekharness-go/internal/version.BuildTime=$(BUILD_TIME)

.PHONY: build test vet fmt run web clean

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/dsh

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

run:
	go run ./cmd/dsh

web:
	go run ./cmd/dsh web

clean:
	go clean
	rm -rf bin
