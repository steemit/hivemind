# Hivemind

Developer-friendly microservice powering social networks on the Steem blockchain.

## Overview

Hivemind is a "consensus interpretation" layer for the Steem blockchain, maintaining the state of social features such as post feeds, follows, and communities. Written in Go, it synchronizes an SQL database with chain state, providing developers with a more flexible/extensible alternative to the raw steemd API.

## Architecture

Hivemind consists of two main components:

1. **Indexer**: Synchronizes blockchain data into PostgreSQL
2. **API Server**: Provides JSON-RPC endpoints for querying indexed data

## Project Structure

```
hivemind/
├── cmd/
│   ├── server/          # API server
│   └── indexer/         # Indexer service
├── internal/
│   ├── api/             # API layer
│   │   ├── bridge/      # Bridge API
│   │   ├── condenser/   # Condenser API
│   │   └── hive/        # Hive API
│   ├── db/              # Database access layer
│   ├── middleware/      # HTTP middleware
│   ├── steem/           # Steem SDK wrapper
│   ├── cache/           # Cache layer (Redis)
│   └── models/          # Data models
├── pkg/
│   ├── telemetry/       # OpenTelemetry integration
│   ├── config/          # Configuration management
│   └── logging/         # Logging system
├── tools/
│   └── db-checksum/     # 20M sync checksum comparison tooling (Go vs
│                        # legacy Python fresh sync; see tools/db-checksum/README.md)
├── migrations/          # Database migrations
└── docs/                # Documentation
```

## Documentation

See the [docs/](docs/) directory for detailed documentation:

- [API Reference](docs/api-reference.md) - Complete API documentation
- [Architecture](docs/architecture.md) - System architecture overview
- [Business Logic](docs/business-logic.md) - Core business logic
- [Database Schema](docs/database-schema.md) - Database structure
- [Indexer Flow](docs/indexer-flow.md) - Indexer workflow
- [Golang Rewrite Plan](.cursor/plans/hivemind-golang-rewrite-plan.md) - Implementation plan
- [AI-Driver Docs](docs/AI-driver/README.md) - Engineering reference for AI agents: architecture rules, module playbooks, known pitfalls

## Development Status

### Completed

- [x] Infrastructure (Go modules, config, logging, telemetry)
- [x] Database layer (models, repository pattern)
- [x] API layer - Condenser API (follow, content, discussions, blog, tags)
- [x] API layer - Bridge API (post, profile, ranked posts, stats)
- [x] API layer - Hive API (public, community, notifications)
- [x] OpenTelemetry integration (OTLP exporter, tracing, metrics)
- [x] Prometheus metrics

### In Progress

- [ ] Indexer core (using legacy Python indexer temporarily)

### Pending

- [ ] Database migrations
- [ ] Tests and documentation

## Development

### Prerequisites

- Go 1.23 or later
- PostgreSQL 10+
- Redis (optional, for caching)
- Steemd node access

### Configuration

Configuration is managed via environment variables or config file:

| Variable | Description | Default |
|----------|-------------|---------|
| `HIVE_DATABASE_URL` | PostgreSQL connection string | Required |
| `HIVE_DB_MIGRATE` | Run schema migrations on startup | true |
| `HIVE_DB_MIGRATE_FORCE` | Stamp baseline version without DDL (first run on a legacy-provisioned DB) | false |
| `HIVE_DB_MAX_OPEN_CONNS` | Max open DB connections | 25 |
| `HIVE_DB_MAX_IDLE_CONNS` | Max idle DB connections | 10 |
| `HIVE_DB_CONN_MAX_LIFETIME` | Max connection lifetime | 1h |
| `HIVE_DB_CONN_MAX_IDLE_TIME` | Max idle connection time | 10m |
| `HIVE_DB_STATEMENT_TIMEOUT` | Per-session statement timeout (0 = disabled) | 30s |
| `HIVE_STEEMD_URL` | Steemd node URL | Required |
| `HIVE_STEEMD_MAX_RETRIES` | Transport-level RPC retries (0 = SDK default; retries cannot be fully disabled) | 3 |
| `HIVE_STEEMD_RPC_TIMEOUT` | Per-attempt RPC timeout against steemd | 15s |
| `HIVE_REDIS_URL` | Redis connection | Optional |
| `HIVE_HTTP_SERVER_PORT` | API server port | 8080 |
| `HIVE_HTTP_READ_TIMEOUT` | HTTP read timeout | 30s |
| `HIVE_HTTP_WRITE_TIMEOUT` | HTTP write timeout | 30s |
| `HIVE_HTTP_IDLE_TIMEOUT` | HTTP idle timeout | 120s |
| `HIVE_HTTP_MAX_BODY_BYTES` | JSON-RPC request body cap; larger bodies get HTTP 413 before parsing (0 = default, max 1073741824) | 10485760 (10MB) |
| `HIVE_HTTP_SHUTDOWN_GRACE` | Graceful-shutdown window on SIGTERM; must be >= `HIVE_DB_STATEMENT_TIMEOUT` so in-flight statements complete (0 = default) | 45s |
| `HIVE_LOG_LEVEL` | Logging level | INFO |
| `HIVE_TRACES_ENDPOINT` | OTLP traces endpoint | localhost:4318 |
| `HIVE_TRACES_TLS` | Use TLS to the OTLP traces endpoint; false keeps insecure OTLP/HTTP (local compose / in-VPC collector), true for TLS endpoints | false |
| `HIVE_TELEMETRY_SERVICE_NAME` | Service name for traces/metrics (e.g. `hivemind-go` during the gray rollout) | hivemind |
| `HIVE_PROMETHEUS_ENABLED` | Enable the observability HTTP listener (`/metrics`, pprof, and the indexer's `/health`) | true |
| `HIVE_PROMETHEUS_PORT` | Observability HTTP port (server default 9090; the indexer is typically set to 9091) | 9090 |
| `HIVE_PPROF_ENABLED` | Mount `/debug/pprof/*` on the observability port (keep the port internal-only) | true |
| `HIVE_HEALTH_MAX_HEAD_AGE` | `/health` fails (500) for reader processes when the indexed head block is older than this (legacy `max_head_age`; 0 = disabled) | 1h |
| `HIVE_CACHE_SYNC_CATCHUP_BLOCKS` | CacheSync gate: pause the hive_posts_cache_temp 90-day prune while the indexer trails the chain head by more than this many blocks (catch-up); resume in live mode | 100 |
| `HIVE_INDEXER_TX_BATCH_BLOCKS` | Blocks sharing one database transaction (fetch, process, follow-count flush, COMMIT); a failure rolls back the whole batch (legacy chunk_size; 1 = per-block transactions) | 1000 |

### Building

```bash
go build -o bin/hivemind-server ./cmd/server
go build -o bin/hivemind-indexer ./cmd/indexer
```

### Running

```bash
# Start API server
./bin/hivemind-server

# Start indexer (requires implementation)
./bin/hivemind-indexer
```

## OpenTelemetry Integration

Hivemind integrates with OpenTelemetry for distributed tracing and metrics:

- **Tracing**: Uses OTLP exporter for Jaeger/Grafana Tempo compatibility
- **Metrics**: Prometheus-compatible metrics endpoint
- **Propagation**: W3C TraceContext for distributed tracing

All JSON-RPC methods are automatically traced with:
- Method name and namespace
- Request parameters (as span events)
- Response status and duration
- Error details (if any)

## Observability Endpoints

Both binaries expose an observability HTTP listener on `HIVE_PROMETHEUS_PORT`
(server default 9090; the indexer is typically set to 9091 in compose):

- **`/metrics`** — Prometheus scrape endpoint. One unified registry carries
  the `hivemind_*` metrics (requests, DB queries, indexer batches, DB
  connection pool in_use/idle/wait gauges), the Go runtime family
  (`go_goroutines`, `go_memstats_*`, `go_gc_duration_seconds`), and the
  OpenTelemetry bridge metrics.
- **`/debug/pprof/`** — Go pprof index (plus `cmdline`, `profile`,
  `symbol`, `trace`), enabled by default via `HIVE_PPROF_ENABLED`. Keep
  this listener internal-only; do not expose it on a public load balancer.

Health endpoints (legacy serve.py semantics):

- **`/health`** (also `/.well-known/healthcheck.json`, server main port)
  — answers 500 when the database is unreachable or, for reader processes
  (the API server), when the indexed head block is older than
  `HIVE_HEALTH_MAX_HEAD_AGE` (1h by default: lagging that far is a writer
  problem, not a reader problem — legacy issue #174 rationale). The
  indexer answers on its observability port and is exempt from the head
  lag criterion (legacy `is_syncer` branch): a writer lagging during a
  multi-hour catch-up is working as intended and must not be recycled by
  a health check. The head query runs on a dedicated single-connection
  pool (the legacy isolated health engine) so a saturated main pool
  cannot starve the probe.
- **`/head_age`** — the `db_head_age` value in seconds as plain text
  (server main port; indexer observability port). Answers 200 whenever
  the database is reachable — including while badly lagged — so external
  monitors can sample sync lag continuously; 500 only on database
  failure.

The API server shuts down gracefully on SIGTERM within
`HIVE_HTTP_SHUTDOWN_GRACE` (45s default, >= the 30s statement timeout);
requests still in flight when the grace expires have their contexts
canceled. The indexer waits for the in-flight block batch (WS1 2.9).

## Docker

### Quick Start

The easiest way to run Hivemind is using Docker Compose:

```bash
# Start the stack (PostgreSQL, Server, Indexer, Jaeger; Redis is defined
# but optional - enable it by uncommenting HIVE_REDIS_URL in compose)
docker-compose up -d

# View logs
docker-compose logs -f

# Stop all services
docker-compose down
```

This will start:
- **PostgreSQL** on port 5432
- **Redis** on port 6379 (optional cache; not a dependency)
- **API Server** on port 8080
- **Indexer** (background service)
- **Jaeger** (tracing UI) on 127.0.0.1:16686; services ship OTLP traces
  to the in-network collector at `http://jaeger:4318`

Port policy: the observability listeners (`/metrics` + `/debug/pprof`:
server 9090, indexer 9091) and the Jaeger UI publish to **127.0.0.1 only** -
keep pprof off public interfaces. Only port 8080 (JSON-RPC) publishes on
all interfaces. Container images carry a `HEALTHCHECK` on the real
`/health` endpoint (server: API port; indexer: observability port).

### Building Docker Images

```bash
# Build server image
docker build -f Dockerfile.server -t hivemind-server:latest .

# Build indexer image
docker build -f Dockerfile.indexer -t hivemind-indexer:latest .

# Build both (using main Dockerfile)
docker build -t hivemind:latest .
```

### Running Individual Services

```bash
# Run server only
docker run -p 8080:8080 \
  -e HIVE_DATABASE_URL=postgresql://user:pass@host:5432/db \
  -e HIVE_STEEMD_URL=https://api.steemit.com \
  hivemind-server:latest

# Run indexer only
docker run \
  -e HIVE_DATABASE_URL=postgresql://user:pass@host:5432/db \
  -e HIVE_STEEMD_URL=https://api.steemit.com \
  hivemind-indexer:latest
```

### Accessing Services

- **API Server**: http://localhost:8080
- **Prometheus Metrics (Server)**: http://localhost:9090/metrics
- **Prometheus Metrics (Indexer)**: http://localhost:9091/metrics
- **Jaeger UI**: http://localhost:16686

## License

MIT
