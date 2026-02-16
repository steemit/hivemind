---
name: Hivemind Code Analysis and Golang Rewrite Plan
overview: Analyze all Python code in the legacy directory, generate detailed API and business logic documentation, and create a complete rewrite plan using Golang + Gin + OpenTelemetry + Jaeger + Prometheus.
todos:
  - id: analyze-codebase
    content: Analyze all code in the legacy directory, outline module structure and dependencies
    status: completed
  - id: generate-api-docs
    content: Generate detailed API documentation including all condenser_api, bridge_api, and hive_api methods
    status: completed
  - id: generate-business-logic-docs
    content: Generate business logic documentation explaining indexer, cache, community, and other module workflows
    status: completed
  - id: generate-db-schema-docs
    content: Generate database schema documentation including all table structures, indexes, and relationships
    status: completed
  - id: generate-architecture-docs
    content: Generate system architecture documentation explaining overall design and working principles
    status: completed
  - id: create-golang-project-structure
    content: Create Golang project directory structure, initialize go.mod and basic configuration
    status: completed
  - id: setup-telemetry
    content: Integrate OpenTelemetry, Jaeger, and Prometheus
    status: completed
  - id: implement-db-layer
    content: Implement database access layer including model definitions and Repository pattern
    status: completed
  - id: implement-indexer-core
    content: Implement indexer core functionality: block sync, account indexing, post indexing, follow relationship indexing
    status: pending
  - id: implement-condenser-api
    content: Implement all condenser_api interfaces
    status: completed
  - id: implement-bridge-api
    content: Implement all bridge_api interfaces
    status: completed
  - id: implement-hive-api
    content: Implement all hive_api interfaces
    status: completed
  - id: implement-community-features
    content: Implement complete community features (role management, subscriptions, post management, etc.)
    status: completed
  - id: implement-notification-system
    content: Implement notification system
    status: completed
  - id: optimize-and-test
    content: Performance optimization, testing, and deployment preparation
    status: pending
---

# Hivemind Code Analysis and Golang Rewrite Plan

## Phase 1: Code Analysis and Documentation Generation

### 1.1 Project Structure Analysis

- Analyze all modules in the `legacy/hive/` directory
- Outline module dependencies
- Identify core business logic components

### 1.2 API Interface Documentation

Generate detailed API documentation including:

- **condenser_api**: steemd-compatible API interfaces (~20+ methods)
  - Follow related: `get_followers`, `get_following`, `get_follow_count`, `get_reblogged_by`
  - Content related: `get_content`, `get_content_replies`, `get_state`
  - Discussion related: `get_discussions_by_trending`, `get_discussions_by_hot`, `get_discussions_by_created`, `get_discussions_by_blog`, `get_discussions_by_feed`, etc.
  - Blog related: `get_blog`, `get_blog_entries`
  - Tags related: `get_trending_tags`
  - Others: `get_account_reputations`, `get_transaction`
- **bridge_api**: Bridge API (~10+ methods)
  - `get_post`, `get_profile`, `get_ranked_posts`, `get_account_posts`, `get_trending_topics`
  - `normalize_post`, `get_post_header`, `get_discussion`
- **hive_api**: Hive-specific API
  - Community: `get_community`, `list_communities`, `list_subscribers`, etc.
  - Notify: `post_notifications`, `account_notifications`, `unread_notifications`
  - Public: `get_account`, `list_followers`, `list_account_blog`, etc.
  - Stats: `get_payout_stats`
- Each interface includes: method signature, parameter description, return structure, examples

### 1.3 Business Logic Documentation

- **Indexer Module**:
  - `sync.py`: Sync manager, handles initial sync, fast sync, real-time listening
  - `blocks.py`: Block processing
  - `posts.py`: Post management (create, edit, delete, restore)
  - `accounts.py`: Account management
  - `follow.py`: Follow relationship processing
  - `community.py`: Community operations (create, role management, subscribe, post management)
  - `cached_post.py`: Post cache management
  - `feed_cache.py`: Feed cache
  - `notify.py`: Notification system
  - `payments.py`: Payment processing
  - `custom_op.py`: Custom operation processing
- **Database Schema**:
  - All table structures (`hive_blocks`, `hive_accounts`, `hive_posts`, `hive_follows`, `hive_communities`, `hive_notifs`, etc.)
  - Index design
  - Relationship descriptions
- **Steem Chain Interaction**:
  - Block stream processing
  - Account data retrieval
  - Content retrieval
  - Dynamic global properties

### 1.4 Documentation Output

Generate in the `docs/` directory:

- `api-reference.md`: Complete API reference documentation
- `business-logic.md`: Detailed business logic documentation
- `database-schema.md`: Database schema documentation
- `architecture.md`: System architecture description
- `indexer-flow.md`: Indexer workflow

## Phase 2: Golang Rewrite Plan

### 2.1 Project Structure Design

```
hivemind/
├── cmd/
│   ├── server/          # API server
│   └── indexer/         # Indexer service
├── internal/
│   ├── api/             # API layer
│   │   ├── condenser/   # condenser_api
│   │   ├── bridge/      # bridge_api
│   │   └── hive/        # hive_api
│   ├── indexer/         # Indexer logic
│   ├── db/              # Database access layer
│   ├── steem/           # Steem SDK wrapper
│   ├── cache/           # Cache layer (Redis)
│   ├── models/          # Data models
│   └── utils/           # Utility functions
├── pkg/
│   ├── telemetry/       # OpenTelemetry integration
│   └── config/          # Configuration management
├── migrations/          # Database migrations
├── docs/                # Documentation (generated from Phase 1)
└── legacy/              # Original Python code
```

### 2.2 Technology Stack

- **Web Framework**: Gin
- **Database**: PostgreSQL (using `github.com/lib/pq` or `gorm.io/gorm`)
- **Cache**: Redis (using `github.com/go-redis/redis/v8`)
- **Steem SDK**: `github.com/steemit/steemgosdk`
- **Observability**:
  - OpenTelemetry: `go.opentelemetry.io/otel`
  - OTLP Exporter: `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp`
  - Prometheus: `github.com/prometheus/client_golang`
- **Configuration**: Viper (`github.com/spf13/viper`)
- **Logging**: Zap (`go.uber.org/zap`) + Scalyr format support

### 2.3 Core Module Implementation Order

#### Phase 1: Infrastructure

1. Project initialization (go.mod, directory structure)
2. Configuration management (environment variables, config files)
3. Database connection and migrations
4. Redis connection
5. OpenTelemetry integration (OTLP + Prometheus)
6. Logging system
   - Integrate Zap logger
   - Implement Scalyr-compatible JSON format encoder
   - Configure log field structure (including trace_id, span_id, etc.)
   - Test log display in Scalyr
7. Steem SDK wrapper
8. **Fork Handling Strategy Evaluation**
   - Analyze legacy fork handling logic
   - Evaluate feasibility of Option A (chase latest block + rollback)
   - If not feasible, use Option B (only chase irreversible blocks)

#### Phase 2: Data Models and Database Layer

1. Define all data models (GORM models)
2. Database access layer (Repository pattern)
3. Database migration scripts
4. Connection pool configuration

#### Phase 3: Indexer Core

1. Block sync manager
   - Implement based on fork handling strategy evaluation results:
     - **Option A**: Block queue, fork detection, limited rollback (max 25 blocks)
     - **Option B**: Only sync to irreversible block height
   - Fork verification at startup (`verify_head` logic)
2. Block processing logic
3. Account indexing
4. Post indexing (create, edit, delete)
5. Follow relationship indexing
6. Feed cache building
7. Post cache management

#### Phase 4: API Layer - Condenser API

1. Follow related interfaces
2. Content related interfaces
3. Discussion query interfaces (trending, hot, created, blog, feed)
4. Tags related interfaces
5. Blog related interfaces
6. Other auxiliary interfaces

#### Phase 5: API Layer - Bridge API

1. Post related interfaces
2. Profile interfaces
3. Ranked posts interfaces
4. Account posts interfaces
5. Trending topics interfaces

#### Phase 6: API Layer - Hive API

1. Community related interfaces
2. Notification interfaces
3. Public API interfaces
4. Stats interfaces

#### Phase 7: Advanced Features

1. Complete community feature implementation
2. Notification system
3. Payment statistics
4. Cache optimization
5. Performance optimization

#### Phase 8: Testing and Deployment

1. Unit tests
2. Integration tests
3. API tests
4. Dockerization
5. Deployment documentation

### 2.4 Key Design Decisions

1. **Concurrency Model**: Use goroutine pool for block sync, channels for communication
2. **Database Transactions**: Use transactions for each block processing to ensure data consistency
3. **Caching Strategy**:
   - Redis for hot data (post content, account info)
   - Cache invalidation strategy
4. **Observability**:
   - Add OpenTelemetry span to all API requests
   - Prometheus metrics: request count, latency, error rate, block sync speed
   - Jaeger distributed tracing
5. **Error Handling**: Unified error handling middleware
6. **API Compatibility**: Maintain full JSON-RPC interface compatibility with Python version
7. **Log Format**:
   - Use structured logging (JSON format) for Scalyr parsing
   - Log fields include: timestamp, level, service, component, message, trace_id, span_id, and business-related fields
   - Reference Scalyr log display templates to ensure key fields can be properly indexed and queried
   - Implement custom Zap encoder for Scalyr-compatible format output
8. **Fork Handling Strategy**:
   - **Evaluation Phase**: Deep analysis of legacy fork handling logic (`BlockQueue`, `verify_head`, `_pop` methods)
   - **Option A (if feasible)**: Implement chase latest block + limited rollback (max 25 blocks)
     - Implement block queue and fork detection
     - Implement fork rollback logic (delete affected data)
     - Handle MicroFork and Fork exceptions
     - Risks: data consistency, rollback complexity, performance impact
   - **Option B (if Option A not feasible)**: Only track irreversible block height
     - Simpler, more stable
     - ~21 second delay (Steem irreversible block confirmation time)
     - No fork rollback handling needed
   - **Decision Criteria**:
     - Stability assessment of Option A (data consistency risk)
     - Implementation complexity assessment (completeness of rollback logic)
     - Performance impact assessment
     - If evaluation results are not ideal, use Option B

### 2.5 Performance Targets

- API response time: P95 < 200ms
- Block sync: Keep up with real-time chain speed (~3 seconds per block)
- Concurrency: Support 1000+ concurrent requests
- Database query optimization: Use appropriate indexes and query optimization

### 2.6 Migration Strategy

1. Maintain database schema compatibility (use same table structure)
2. Can run Python and Go versions in parallel for validation
3. Gradually switch traffic
4. Data consistency check tools

## Implementation Steps

1. **Step 1**: Complete code analysis, generate all documentation (estimated 2-3 days) ✅
2. **Step 2**: Set up Go project infrastructure (1-2 days) ✅
   - Include Scalyr log format implementation
   - Fork handling strategy evaluation
3. **Step 3**: Implement indexer core functionality (1-2 weeks)
   - Implement corresponding sync strategy based on evaluation results
4. **Step 4**: Implement API layer (2-3 weeks) ✅
5. **Step 5**: Implement advanced features (1-2 weeks) ✅
6. **Step 6**: Testing and optimization (1-2 weeks)

## Documentation Output Location

All documentation will be saved in the `docs/` directory:

- `api-reference.md`
- `business-logic.md`
- `database-schema.md`
- `architecture.md`
- `indexer-flow.md`
- `golang-rewrite-plan.md` (this plan)
