BINARY := bin/ai-workflow

.PHONY: build test vet fmt check clean

build:
	go build -o $(BINARY) ./cmd/ai-workflow

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -type f)

check: fmt vet test

clean:
	rm -rf bin dist
