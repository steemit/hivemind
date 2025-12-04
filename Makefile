.PHONY: build test clean run-server run-indexer

# Build both server and indexer
build:
	go build -o bin/hivemind-server ./cmd/server
	go build -o bin/hivemind-indexer ./cmd/indexer

# Build server only
build-server:
	go build -o bin/hivemind-server ./cmd/server

# Build indexer only
build-indexer:
	go build -o bin/hivemind-indexer ./cmd/indexer

# Run tests
test:
	go test ./...

# Run server
run-server:
	go run ./cmd/server

# Run indexer
run-indexer:
	go run ./cmd/indexer

# Clean build artifacts
clean:
	rm -rf bin/

# Format code
fmt:
	go fmt ./...

# Lint code
lint:
	golangci-lint run

# Download dependencies
deps:
	go mod download
	go mod tidy

# Run all checks
check: fmt lint test

