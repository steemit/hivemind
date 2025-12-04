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

## License

MIT

