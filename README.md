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
| `HIVE_STEEMD_URL` | Steemd node URL | Required |
| `HIVE_REDIS_URL` | Redis connection | Optional |
| `HIVE_HTTP_SERVER_PORT` | API server port | 8080 |
| `HIVE_LOG_LEVEL` | Logging level | INFO |
| `HIVE_TRACES_ENDPOINT` | OTLP traces endpoint | localhost:4318 |
| `HIVE_PROMETHEUS_ENABLED` | Enable Prometheus metrics | true |
| `HIVE_PROMETHEUS_PORT` | Prometheus metrics port | 9090 |

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

## Docker

### Quick Start

The easiest way to run Hivemind is using Docker Compose:

```bash
# Start all services (PostgreSQL, Redis, Server, Indexer)
docker-compose up -d

# View logs
docker-compose logs -f

# Stop all services
docker-compose down
```

This will start:
- **PostgreSQL** on port 5432
- **Redis** on port 6379
- **API Server** on port 8080
- **Indexer** (background service)
- **Jaeger** (tracing UI) on port 16686

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
  -e HIVE_STEEM_URL=https://api.steemit.com \
  hivemind-server:latest

# Run indexer only
docker run \
  -e HIVE_DATABASE_URL=postgresql://user:pass@host:5432/db \
  -e HIVE_STEEM_URL=https://api.steemit.com \
  hivemind-indexer:latest
```

### Accessing Services

- **API Server**: http://localhost:8080
- **Prometheus Metrics (Server)**: http://localhost:9090/metrics
- **Prometheus Metrics (Indexer)**: http://localhost:9091/metrics
- **Jaeger UI**: http://localhost:16686

## License

MIT
