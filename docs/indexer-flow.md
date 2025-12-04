# Indexer Flow Documentation

## Overview

The Hivemind indexer is responsible for synchronizing blockchain data into the database. It processes blocks sequentially, extracts operations, and updates the database state.

## Indexer Modes

### 1. Initial Sync

Runs when the database is empty or `hive_feed_cache` is empty.

**Process:**
1. Load database schema
2. Disable non-critical indexes for faster sync
3. Load from checkpoints (if available)
4. Fast sync from steemd up to last irreversible block
5. Build initial cache:
   - Recover missing posts
   - Rebuild feed cache
   - Force recount follows
6. Re-enable indexes
7. Mark initial sync as complete

### 2. Fast Sync

Syncs from current head to last irreversible block.

**Process:**
1. Get current head block number
2. Get last irreversible block number
3. Fetch blocks in batches (default: 1000 blocks)
4. Process blocks in transaction
5. Flush dirty queues:
   - Accounts.flush()
   - CachedPost.flush()
   - Follow.flush()
6. Commit transaction

### 3. Live Sync (Block Following)

Follows the blockchain in real-time, processing new blocks as they arrive.

**Process:**
1. Start from current head + 1
2. Stream blocks with trail (default: 2 blocks behind head)
3. For each block:
   - Start transaction
   - Process block
   - Flush follows
   - Flush accounts (spread over 8 calls)
   - Mark dirty payouts
   - Flush cached posts
   - Commit transaction
4. Periodic tasks:
   - Every 1200 blocks (1 hour): Fetch account ranks
   - Every 200 blocks (10 min): Recalculate community pending payouts
   - Every 100 blocks (5 min): Dirty oldest accounts
   - Every 20 blocks (1 min): Update chain state

## Block Processing Flow

```
Block Received
    │
    ├─► Validate block structure
    │
    ├─► Insert into hive_blocks
    │
    ├─► Extract operations
    │   │
    │   ├─► Account Operations
    │   │   ├─► pow_operation → register worker_account
    │   │   ├─► pow2_operation → register worker_account
    │   │   ├─► account_create_operation → register new_account_name
    │   │   ├─► account_create_with_delegation_operation → register
    │   │   ├─► create_claimed_account_operation → register
    │   │   ├─► account_update_operation → Accounts.dirty()
    │   │   └─► account_update2_operation → Accounts.dirty()
    │   │
    │   ├─► Post Operations
    │   │   ├─► comment_operation → Posts.comment_op()
    │   │   │                       → Accounts.dirty(author)
    │   │   └─► delete_comment_operation → Posts.delete_op()
    │   │
    │   ├─► Vote Operations
    │   │   └─► vote_operation → Accounts.dirty(author, voter)
    │   │                       → CachedPost.vote()
    │   │
    │   ├─► Transfer Operations
    │   │   └─► transfer_operation → Payments.op_transfer()
    │   │
    │   └─► Custom JSON Operations
    │       └─► custom_json_operation → CustomOp.process_ops()
    │
    ├─► Register new accounts → Accounts.register()
    │
    ├─► Process custom ops → CustomOp.process_ops()
    │   ├─► follow → Follow.follow_op()
    │   ├─► reblog → CustomOp.reblog()
    │   ├─► community → process_json_community_op()
    │   └─► notify → CustomOp._process_notify()
    │
    └─► Save transaction IDs
```

## Operation Handlers

### Account Registration

**Handler:** `Accounts.register(names, block_date)`

**Process:**
1. Filter out already-registered names
2. Insert new accounts into `hive_accounts`
3. Load IDs into memory map
4. Check for new communities (if after START_DATE)

### Post Operations

**Handler:** `Posts.comment_op(op, block_date)`

**Process:**
1. Check if post exists
2. If new: `Posts.insert()`
   - Build post object
   - Insert into `hive_posts`
   - Cache post ID
   - Insert into feed cache (if depth=0)
   - Trigger notification if error
3. If exists and not deleted: `Posts.update()`
   - Mark for cache update
4. If exists but deleted: `Posts.undelete()`
   - Re-allocate record
   - Update cache

**Post Validation:**
- Inherits parent properties (depth, category, community_id, is_valid, is_muted)
- Validates community permissions
- Checks for muted/invalid parent

### Vote Operations

**Handler:** `CachedPost.vote(author, permlink, pid, voter)`

**Process:**
1. Mark post as dirty with 'upvote' level
2. Track voter for notification

### Follow Operations

**Handler:** `Follow.follow_op(account, op_json, date)`

**Process:**
1. Validate operation
2. Calculate new state (blog=1, ignore=2, both=3)
3. Check delta from old state
4. Insert or update `hive_follows`
5. Track count deltas
6. Trigger notification if new follow

**State Values:**
- 0: No relationship
- 1: Blog follow
- 2: Ignore
- 3: Blog follow + ignore

### Reblog Operations

**Handler:** `CustomOp.reblog(account, op_json, block_date)`

**Process:**
1. Validate operation
2. Get post ID
3. If delete: Remove from `hive_reblogs` and feed cache
4. If create: Insert into `hive_reblogs` and feed cache
5. Trigger notification

### Community Operations

**Handler:** `process_json_community_op(actor, op_json, date)`

**Supported Operations:**
- `updateProps`: Update community properties
- `setRole`: Set user role in community
- `setUserTitle`: Set custom user title
- `mutePost`: Mute a post
- `unmutePost`: Unmute a post
- `pinPost`: Pin a post
- `unpinPost`: Unpin a post
- `flagPost`: Flag a post
- `subscribe`: Subscribe to community
- `unsubscribe`: Unsubscribe from community

**Process:**
1. Validate operation structure
2. Validate permissions (role-based)
3. Apply state changes
4. Trigger notifications

## Cache Management

### Post Cache Queue

Posts are queued for cache updates with priority levels:

1. **insert**: New post (highest priority)
2. **payout**: Post was paid out
3. **update**: Post was modified
4. **upvote**: Vote changed payout
5. **recount**: Child count changed (lowest priority)

**Flush Process:**
1. Load missing post IDs
2. Group by priority level
3. Fetch posts from steemd in batches (1000)
4. Generate SQL updates
5. Write to database
6. Trigger notifications

### Feed Cache

Materialized view of posts + reblogs.

**Insert:**
- When new post created (depth=0)
- When reblog created

**Delete:**
- When post deleted
- When reblog removed

**Rebuild:**
- After initial sync
- Can be manually triggered

### Account Cache

Accounts are marked dirty and updated periodically.

**Dirty Triggers:**
- Account update operation
- Post creation (lite update)
- Vote operation (reputation update)
- Payout (force update)

**Flush Process:**
1. Shift portion from dirty queue
2. Fetch accounts from steemd (batch: 1000)
3. Update database
4. Spread over multiple calls to avoid overload

## Fork Handling

### Fork Detection

**Block Queue:**
- Maintains buffer of recent blocks (size = TRAIL_BLOCKS)
- Validates block hash chain
- Detects forks by comparing previous hash

**Fork Types:**
- **MicroFork**: Fork within buffer (easily recoverable)
- **Fork**: Fork beyond buffer (requires recovery)

### Fork Recovery

**Process:**
1. On startup: `Blocks.verify_head()`
   - Compare hive head with steemd
   - Pop blocks until match found
   - Maximum depth: 25 blocks
2. During sync: Exception handling
   - Catch MicroForkException
   - Restart stream
3. Pop operation: `Blocks._pop(blocks)`
   - Delete affected records:
     - Notifications
     - Subscriptions
     - Roles
     - Communities
     - Feed cache
     - Reblogs
     - Follows
     - Posts (cache, tags, core)
     - Payments
     - Blocks
   - Commit transaction

**Limitations:**
- Maximum fork depth: 25 blocks
- Follow counts may need manual recount
- Some state may be inconsistent

## Performance Optimizations

1. **Batch Processing**: Operations batched for efficiency
2. **Transaction Management**: Each block in single transaction
3. **Dirty Queues**: Changes queued and flushed in batches
4. **Index Management**: Non-critical indexes disabled during initial sync
5. **Connection Pooling**: Database connections reused
6. **Memory Caching**: Account ID map, post ID map, ranks cached in memory

## Error Handling

- **Missing Posts**: Logged and deferred for retry
- **Deleted Posts**: Handled gracefully
- **Invalid Operations**: Logged and skipped
- **Fork Exceptions**: Caught and handled with recovery
- **Database Errors**: Transaction rolled back

## Monitoring

Key metrics to monitor:
- Block processing rate
- Queue sizes (dirty accounts, dirty posts)
- Cache hit rates
- Fork frequency
- Database connection pool usage
- Error rates

