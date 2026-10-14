.PHONY: build build-server build-indexer test test-integration clean run-server run-indexer fmt fmt-check vet lint deps check

# Integration test database conventions (see .github/workflows/check.yml and
# the Q4 quality plan): integration tests DROP/recreate schemas and databases,
# so they must NEVER point at the persistent dev stack (docker-compose.yml on
# :5432). Local convention is a disposable PostgreSQL on port 5433 with
# test:test credentials, same DSN shape as CI.
TEST_PG_PORT := 5433
TEST_DB_URL := postgresql://test:test@localhost:$(TEST_PG_PORT)/postgres
TEST_PG_IMAGE := postgres:15-alpine
TEST_PG_CONTAINER := hivemind-test-pg-ephemeral

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

# Run integration tests (DB-backed tests; unit tests still run).
#
# Database resolution order:
#   1. HIVE_TEST_DATABASE_URL if already exported (used as-is, no container
#      lifecycle management - suited for the long-lived shared
#      hivemind-test-pg container).
#   2. Any container already publishing $(TEST_PG_PORT) (the shared
#      hivemind-test-pg convention) - reused as-is.
#   3. Otherwise an ephemeral postgres:15 container started for this run and
#      removed afterwards (also removed on interrupt).
#
# -p 1 is mandatory: integration tests DROP SCHEMA and the internal/db +
# internal/indexer packages share the target database, so test packages must
# run serially (same rule as CI).
#
# Extra go test flags via TEST_FLAGS, e.g.:
#   make test-integration TEST_FLAGS='-race -run TestProcessBlock'
test-integration:
	@url="$${HIVE_TEST_DATABASE_URL:-}"; \
	started=""; \
	cleanup() { \
		if [ -n "$$started" ]; then \
			echo "Removing ephemeral container $(TEST_PG_CONTAINER)"; \
			docker rm -f "$(TEST_PG_CONTAINER)" >/dev/null 2>&1 || true; \
		fi; \
	}; \
	trap cleanup EXIT INT TERM; \
	if [ -z "$$url" ]; then \
		if [ -n "$$(docker ps --filter publish=$(TEST_PG_PORT) -q 2>/dev/null)" ]; then \
			echo "Reusing PostgreSQL already publishing :$(TEST_PG_PORT)"; \
		else \
			echo "Starting ephemeral $(TEST_PG_IMAGE) as $(TEST_PG_CONTAINER) on :$(TEST_PG_PORT)"; \
			docker run -d --name "$(TEST_PG_CONTAINER)" \
				-e POSTGRES_USER=test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=postgres \
				-p $(TEST_PG_PORT):5432 "$(TEST_PG_IMAGE)" >/dev/null || exit 1; \
			started=1; \
			echo -n "Waiting for Postgres "; \
			ok=""; \
			for i in $$(seq 1 30); do \
				if docker exec "$(TEST_PG_CONTAINER)" pg_isready -U test -d postgres >/dev/null 2>&1; then ok=1; break; fi; \
				echo -n "."; sleep 1; \
			done; \
			echo; \
			if [ -z "$$ok" ]; then echo "Postgres did not become ready in time" >&2; exit 1; fi; \
		fi; \
		url="$(TEST_DB_URL)"; \
	fi; \
	echo "HIVE_TEST_DATABASE_URL=$$url"; \
	HIVE_TEST_DATABASE_URL="$$url" go test -p 1 $(TEST_FLAGS) ./...

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

# Check formatting WITHOUT rewriting files (read-only; same gate as the CI
# check job's "Check gofmt" step). `gofmt -l` must list zero files.
fmt-check:
	@if [ -n "$$(gofmt -l .)" ]; then \
		echo "gofmt found unformatted files (run 'gofmt -w <file>' or 'make fmt'):"; \
		gofmt -l .; \
		exit 1; \
	fi

# Run go vet (same gate as the CI check job's "go vet" step)
vet:
	go vet ./...

# Lint code (requires golangci-lint; same rule set as the CI check job)
lint:
	@if ! command -v golangci-lint >/dev/null 2>&1; then \
		echo "golangci-lint is not installed."; \
		echo ""; \
		echo "Install the pinned CI version with:"; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0"; \
		echo ""; \
		echo "then make sure $$(go env GOPATH)/bin is on your PATH."; \
		exit 1; \
	fi
	golangci-lint run

# Download dependencies
deps:
	go mod download
	go mod tidy

# Run all checks - same gates as the CI check job, in the same order:
# fmt-check (read-only), vet, lint, tests. DB-backed tests are env-gated on
# HIVE_TEST_DATABASE_URL and skip here; run `make test-integration` for those.
check: fmt-check vet lint test
