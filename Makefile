BINARY := bin/ai-workflow
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
LDFLAGS := -s -w -X github.com/Taki7980/ai-workflow-v3/internal/version.Version=$(VERSION)

.PHONY: build test race vet fmt check clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/ai-workflow

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -type f)

check: fmt vet test

clean:
	rm -rf bin dist
