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
│   ├── server/          # API 服务器
│   └── indexer/         # 索引器服务
├── internal/
│   ├── api/            # API 层
│   ├── indexer/         # 索引器逻辑
│   ├── db/             # 数据库访问层
│   ├── steem/          # Steem SDK 封装
│   ├── cache/          # 缓存层（Redis）
│   ├── models/         # 数据模型
│   └── utils/          # 工具函数
├── pkg/
│   ├── telemetry/      # OpenTelemetry 集成
│   └── config/         # 配置管理
├── migrations/         # 数据库迁移
└── docs/              # 文档
```

## Documentation

See the [docs/](docs/) directory for detailed documentation:

- [API Reference](docs/api-reference.md) - Complete API documentation
- [Architecture](docs/architecture.md) - System architecture overview
- [Business Logic](docs/business-logic.md) - Core business logic
- [Database Schema](docs/database-schema.md) - Database structure
- [Indexer Flow](docs/indexer-flow.md) - Indexer workflow
- [Golang Rewrite Plan](.cursor/plans/hivemind-golang-rewrite-plan.md) - Implementation plan

## Development

### Prerequisites

- Go 1.21 or later
- PostgreSQL 10+
- Redis (optional, for caching)
- Steemd node access

### Configuration

Configuration is managed via environment variables or config file:

- `DATABASE_URL`: PostgreSQL connection string
- `STEEMD_URL`: Steemd node URL
- `REDIS_URL`: Redis connection (optional)
- `HTTP_SERVER_PORT`: API server port (default: 8080)
- `LOG_LEVEL`: Logging level (default: INFO)

### Building

```bash
go build -o bin/hivemind-server ./cmd/server
go build -o bin/hivemind-indexer ./cmd/indexer
```

### Running

```bash
# Start indexer
./bin/hivemind-indexer

# Start API server
./bin/hivemind-server
```

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

### Environment Variables

Key environment variables for Docker:

- `HIVE_DATABASE_URL`: PostgreSQL connection string
- `HIVE_STEEM_URL`: Steemd node URL
- `HIVE_REDIS_URL`: Redis connection (optional)
- `HIVE_HTTP_SERVER_PORT`: API server port (default: 8080)
- `HIVE_LOG_LEVEL`: Logging level (default: INFO)
- `HIVE_TELEMETRY_ENABLED`: Enable telemetry (default: true)

See `docker-compose.yml` for all available configuration options.

### Accessing Services

- **API Server**: http://localhost:8080
- **Prometheus Metrics (Server)**: http://localhost:9090/metrics
- **Prometheus Metrics (Indexer)**: http://localhost:9091/metrics
- **Jaeger UI**: http://localhost:16686

## License

MIT

