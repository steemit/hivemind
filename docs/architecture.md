# Hivemind Architecture Documentation

## Overview

Hivemind is a microservice that provides a "consensus interpretation" layer for the Steem blockchain. It maintains the state of social features such as post feeds, follows, and communities by synchronizing an SQL database with blockchain state.

## System Architecture

```
┌─────────────────┐
│   Steem Chain   │
│   (steemd API)  │
└────────┬────────┘
         │
         │ Blocks, Accounts, Content
         │
┌────────▼─────────────────────────────┐
│         Hivemind Indexer            │
│  ┌──────────────────────────────┐   │
│  │  Block Sync Manager          │   │
│  │  - Initial sync               │   │
│  │  - Fast sync                  │   │
│  │  - Live block following       │   │
│  └──────────┬───────────────────┘   │
│             │                        │
│  ┌──────────▼───────────────────┐   │
│  │  Block Processor              │   │
│  │  - Parse operations           │   │
│  │  - Dispatch to handlers       │   │
│  └──────────┬───────────────────┘   │
│             │                        │
│  ┌──────────▼───────────────────┐   │
│  │  Indexer Modules              │   │
│  │  - Accounts                   │   │
│  │  - Posts                      │   │
│  │  - Follows                    │   │
│  │  - Communities                │   │
│  │  - Payments                   │   │
│  │  - Custom Ops                 │   │
│  └──────────┬───────────────────┘   │
│             │                        │
│  ┌──────────▼───────────────────┐   │
│  │  Cache Layer                  │   │
│  │  - CachedPost                 │   │
│  │  - FeedCache                  │   │
│  └──────────┬───────────────────┘   │
└─────────────┼────────────────────────┘
              │
              │ Write
              │
      ┌───────▼────────┐
      │   PostgreSQL   │
      │   Database     │
      └───────┬────────┘
              │
              │ Read
              │
┌─────────────▼────────────────────────┐
│         API Server                   │
│  ┌──────────────────────────────┐   │
│  │  JSON-RPC Handler            │   │
│  └──────────┬───────────────────┘   │
│             │                        │
│  ┌──────────▼───────────────────┐   │
│  │  API Modules                  │   │
│  │  - condenser_api              │   │
│  │  - bridge_api                 │   │
│  │  - hive_api                   │   │
│  └──────────┬───────────────────┘   │
│             │                        │
│  ┌──────────▼───────────────────┐   │
│  │  Query Layer                 │   │
│  │  - Cursor-based pagination   │   │
│  │  - Post loading              │   │
│  │  - Account loading           │   │
│  └──────────────────────────────┘   │
└─────────────────────────────────────┘
```

## Core Components

### 1. Indexer (Blockchain Sync)

The indexer is responsible for:
- **Block Synchronization**: Following the blockchain and processing blocks
- **Operation Processing**: Parsing blockchain operations and updating database
- **State Management**: Maintaining consistency with blockchain state

#### Key Modules:

- **Sync Manager** (`sync.py`): Orchestrates the sync process
  - Initial sync from checkpoints or steemd
  - Fast sync up to irreversible block
  - Live block following with fork handling

- **Block Processor** (`blocks.py`): Processes individual blocks
  - Validates block structure
  - Extracts operations
  - Dispatches to appropriate handlers

- **Account Indexer** (`accounts.py`): Manages account data
  - Registers new accounts
  - Caches account metadata from steemd
  - Maintains account ID mapping

- **Post Indexer** (`posts.py`): Handles post/comment operations
  - Creates new posts
  - Updates existing posts
  - Handles deletions
  - Manages post hierarchy

- **Follow Indexer** (`follow.py`): Processes follow operations
  - Tracks follow/unfollow actions
  - Maintains follow counts
  - Handles mute/ignore states

- **Community Indexer** (`community.py`): Manages communities
  - Registers new communities
  - Processes community operations
  - Manages roles and subscriptions

- **Cache Managers**:
  - **CachedPost** (`cached_post.py`): Maintains post cache with computed fields
  - **FeedCache** (`feed_cache.py`): Materialized view of posts + reblogs

### 2. API Server

The API server provides JSON-RPC endpoints for querying indexed data.

#### API Namespaces:

1. **condenser_api**: Compatibility layer for steemd condenser API
   - Follow queries
   - Content queries
   - Discussion queries (trending, hot, created, etc.)
   - Blog queries

2. **bridge_api**: Bridge API for modern clients
   - Post queries
   - Profile queries
   - Ranked posts
   - Account posts

3. **hive_api**: Hive-specific APIs
   - Community APIs
   - Notification APIs
   - Public APIs
   - Stats APIs

### 3. Database Layer

- **Schema Management**: SQLAlchemy-based schema definitions
- **Migrations**: Automatic migration system (version 22)
- **Query Interface**: Async database adapter with connection pooling

### 4. Steem Client

- **HTTP Client**: Communicates with steemd nodes
- **Block Stream**: Streams blocks with fork detection
- **Batch Operations**: Efficient batch fetching of accounts and content

## Data Flow

### Block Processing Flow

```
1. Sync Manager fetches blocks from steemd
2. Block Processor validates and extracts operations
3. For each operation:
   - Account operations → Accounts.register()
   - Comment operations → Posts.comment_op()
   - Vote operations → CachedPost.vote()
   - Transfer operations → Payments.op_transfer()
   - Custom JSON → CustomOp.process_ops()
4. Flush dirty queues:
   - Accounts.flush() - Update account cache
   - CachedPost.flush() - Update post cache
   - Follow.flush() - Update follow counts
5. Commit transaction
```

### API Request Flow

```
1. JSON-RPC request received
2. Method dispatcher routes to appropriate handler
3. Handler validates parameters
4. Query layer fetches data from database
5. Post/Account objects loaded and enriched
6. Response serialized and returned
```

## Fork Handling

Hivemind implements a fork detection and recovery mechanism:

1. **Block Queue**: Maintains a buffer of recent blocks
2. **Fork Detection**: Validates block hash chain
3. **Recovery**: Pops blocks back to last valid block
4. **Data Cleanup**: Removes affected records (posts, follows, etc.)

**Limitations:**
- Maximum fork depth: 25 blocks
- Some data may require manual recount (follow counts)
- Fork recovery only works if fork is within irreversible block

**Alternative Strategy**: Sync only to last irreversible block (more stable, ~21s delay)

## Caching Strategy

### Post Cache (`hive_posts_cache`)

- Stores computed fields for efficient querying
- Updated on: insert, update, vote, payout
- Priority levels: insert > payout > update > upvote > recount

### Feed Cache (`hive_feed_cache`)

- Materialized view of posts + reblogs
- Enables efficient feed/blog queries
- Rebuilt after initial sync

### Account Cache

- Cached in `hive_accounts` table
- Updated periodically from steemd
- Dirty queue tracks accounts needing updates

## Performance Considerations

1. **Batch Processing**: Operations are batched for efficiency
2. **Indexes**: Strategic indexes on frequently queried columns
3. **Partial Indexes**: WHERE clauses on indexes to reduce size
4. **Connection Pooling**: Database connection reuse
5. **Async Operations**: Async/await for I/O operations

## Configuration

Key configuration options:
- `DATABASE_URL`: PostgreSQL connection string
- `STEEMD_URL`: Steemd node URL
- `REDIS_URL`: Redis connection (optional, for caching)
- `TRAIL_BLOCKS`: Blocks to trail behind head (fork protection)
- `MAX_BATCH`: Batch size for block processing
- `MAX_WORKERS`: Number of worker threads

## Deployment

- **Docker**: Containerized deployment
- **Health Checks**: `/health` and `/.well-known/healthcheck.json` endpoints
- **Logging**: Structured logging for monitoring
- **Metrics**: Can be extended with Prometheus metrics

