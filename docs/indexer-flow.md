# Indexer Flow Documentation

## Overview

The Hivemind indexer is responsible for synchronizing blockchain data into the database. It processes blocks sequentially, extracts operations, and updates the database state.

## Indexer Modes

### 1. Initial Sync

Runs when the persisted `hive_state.is_initial_sync` marker is TRUE. For
databases that predate the marker (NULL -- e.g. provisioned by the Python
legacy), the phase derives once from `hive_feed_cache` emptiness (the legacy
heuristic) and is then materialized; the marker is authoritative from then on.

**Process:**
1. Load database schema
2. Disable non-critical indexes for faster sync
3. Load from checkpoints (if available)
4. Fast sync from steemd up to last irreversible block (bounded retry with
   poison-block logging: after 10 consecutive failures of the same block the
   process exits -- fail-fast -- and a restart resumes from `hive_blocks` head)
5. Build initial cache (tail trio, legacy order):
   - Recover missing posts
   - Rebuild feed cache
   - Force recount follows
   - (`recount_done` is persisted immediately after the recount succeeds, and
   is invalidated BEFORE any continuation sync -- so the tail is skipped only
   when nothing new was synced AND the trio truly completed at this head; an
   interrupted tail always heals by re-running the missing steps)
6. Re-enable indexes
7. Flip `hive_state.is_initial_sync` to FALSE -- written only after the whole
   tail trio succeeded, so every failure path restarts into this routine
   instead of the normal loop
8. `HIVE_TEST_MAX_BLOCK` stop point: when set, the tail trio still runs at the
   cap but the flag deliberately stays TRUE (a subsequent continuation run
   keeps fast-sync semantics) and the process exits cleanly
9. A shutdown arriving mid-phase (initial sync or recovery) aborts it cleanly:
   the process EXITS (`cancelled before completion`) instead of reporting a
   bogus completion and entering live mode; the next boot resumes the phase
   from `hive_blocks` head and the persisted markers

Maintenance SQL above (rebuild, recount, missing-post sweeps, cache audits)
runs with `SET LOCAL statement_timeout = 0` plus `SET LOCAL lock_timeout =
'60s'`: these statements legitimately run minutes-long on a fully synced
database and must not be killed by the indexer's default 30s session guard,
while the lock timeout keeps any of them from queueing forever behind a long
reader. The temp-table pruner (`CacheSync`) deletes in committed batches of
10,000 rows instead of one giant `DELETE`, so the first live-mode prune after
a cold sync cannot be killed half-way and retried forever by the session
guard, and autovacuum can reclaim space incrementally.

### 2. Fast Sync

Syncs from current head to last irreversible block.

**Process:**
1. Get current head block number
2. Get last irreversible block number
3. Group the range into tx batches of `HIVE_INDEXER_TX_BATCH_BLOCKS` blocks
   (default 1000, legacy `chunk_size`)
4. Per tx batch:
   - Fetch the whole batch from steemd first (fetch chunks of
     `HIVE_MAX_BATCH`; cancellable between chunks -- a shutdown here aborts
     with nothing in flight)
   - Open ONE transaction, process every block of the batch, flush the
     follow count deltas INSIDE the transaction (legacy
     `Follow.flush(trx=False)` in `process_multi`), COMMIT -- all on a
     context with the cancellation removed, so an in-flight batch always
     finishes
   - Between transactions: `Accounts.FlushBatch` then `CachedPost.Flush`,
     each in its own transaction (legacy `from_steemd` runs both after the
     chunk loop; they make steemd RPC calls and stay out of the batch
     transaction)
5. A failure of any block in the batch rolls back the WHOLE batch (legacy
   parity: a chunk transaction failure rolls back the chunk); the sync loop
   retries the batch from its first block

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

## Legacy Database Migration Runbook

Bringing a Python-legacy-provisioned database to the Go indexer (first boot
with `HIVE_DB_MIGRATE_FORCE=1` to stamp the baseline, see `cmd/indexer`):
the persisted markers (`hive_state.is_initial_sync`, `recount_done`) are
NULL on such a database, so the first boot derives the phase once from
`hive_feed_cache` emptiness and materializes it.

### Suspected interrupted tail

The legacy initial sync ended with `feed_cache.Rebuild` followed by
`force_recount`. A legacy database whose initial sync was interrupted between
the two shows: feed cache POPULATED, `hive_accounts.followers/following`
counts at 0 (never recomputed), and no retry ever scheduled. On the first Go
boot this shape derives `is_initial_sync = false` (feed cache non-empty) and
`recount_done = true` (presumed healthy) -- which would skip the recount
forever. For a database you SUSPECT carries this shape, force the recount on
the first Go boot:

```bash
HIVE_FORCE_FOLLOW_RECOUNT=true HIVE_DB_MIGRATE_FORCE=1 ./bin/hivemind-indexer
```

### Marker inspection and repair

```sql
-- Inspect the phase markers and tail state.
SELECT block_num, is_initial_sync, recount_done FROM hive_state;
SELECT COUNT(*) AS feed_cache_rows FROM hive_feed_cache;
SELECT COUNT(*) AS zero_follower_accounts
  FROM hive_accounts WHERE followers = 0 AND following = 0;
```

If the markers disagree with reality (e.g. after manual surgery), repair
them on the LOWEST `block_num` row -- flag reads and writes always target
that row, and a legacy database must keep exactly one `hive_state` row:

```sql
-- Replay the initial-sync tail on the next boot:
UPDATE hive_state SET recount_done = NULL
 WHERE block_num = (SELECT MIN(block_num) FROM hive_state);

-- Or mark the tail complete (only after verifying counts are correct):
UPDATE hive_state SET recount_done = true
 WHERE block_num = (SELECT MIN(block_num) FROM hive_state);

-- Re-enter the full initial-sync routine (blocks re-check + tail trio):
UPDATE hive_state SET is_initial_sync = true
 WHERE block_num = (SELECT MIN(block_num) FROM hive_state);
```

### Corruption warning at startup

A database that reports `is_initial_sync = true` with a POPULATED feed cache
and `recount_done` unset is in one of two states: an interrupted tail (this
boot re-runs the trio and heals -- recount re-runs, counts recover), or a
corrupted marker set. The indexer logs

```
WARN feed cache is populated while initial sync is in progress and recount_done is unset: interrupted tail (this boot re-runs the trio and heals) or corrupted state -- verify counts after startup
```

on that combination; after such a boot, verify the counts (query above)
before serving traffic. The sanctioned non-empty state -- a
`HIVE_TEST_MAX_BLOCK` stop point with the trio complete -- carries
`recount_done = true` and does not warn.


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
2. **Transaction Batching**: `HIVE_INDEXER_TX_BATCH_BLOCKS` blocks (default
   1000, legacy `chunk_size`) share one transaction -- one COMMIT per batch
   instead of one per block. Set to 1 to restore per-block transactions
   (degenerate mode, useful for isolating a poison block's blast radius).
3. **Dirty Queues**: Changes queued and flushed in batches
4. **Index Management**: Non-critical indexes disabled during initial sync
5. **Connection Pooling**: Database connections reused
6. **Memory Caching**: Account ID map, post ID map, ranks cached in memory
7. **Batched Account Flush**: account ids for the rank column are resolved
   in one query per 1000-account chunk (legacy keeps a name->id map in
   memory -- accounts.py `load_ids`)

Throughput observability (Prometheus): `hivemind_indexer_blocks_processed_total`
(rate() = blocks/s, `phase` = initial|live),
`hivemind_indexer_blocks_fetch_duration_seconds` (steemd fetch RPC wall time
per batch) and `hivemind_indexer_tx_batch_duration_seconds` (transaction
open through COMMIT). The per-batch log line `Synced block batch` carries
the same numbers (blocks, fetch, write, blocks_per_sec).
`hivemind_indexer_poison_retries_total{phase}` counts every failed sync
attempt the poison loops retry (see the poison runbook below for the
amplification math it guards).

## PostgreSQL Tuning for Bulk Sync (Operations Runbook)

Recommended settings for the dedicated sync instance during a 0->30M
initial sync (baseline sized for a 64 GiB / 8 vCPU box; scale
proportionally). These are instance-level recommendations, not code:

| Setting | Value | Rationale |
|---|---|---|
| `synchronous_commit` | `off` | A crash loses the last few commits; the indexer replays from `hive_blocks` MAX(num) on restart, so nothing is lost but time. Revert to `on` for the long-lived live instance. |
| `max_wal_size` | `32GB` | 1000-block batch transactions generate large WAL bursts; the default (1-4GB) forces frequent checkpoints that stall write throughput. |
| `checkpoint_timeout` | `30min` | Spreads checkpoints out during the multi-week bulk load. |
| `wal_compression` | `on` | Trades a little CPU for substantially less WAL volume. |
| `shared_buffers` | `16GB` | Write-heavy working set on a dedicated instance. |
| `effective_cache_size` | `48GB` | Planner hint; the OS page cache holds the rest. |

Notes:

- **UNLOGGED tables are NOT applicable**: every hive table must survive a
  restart (`hive_posts_cache_temp` is a durability-preserving staging table
  synced into `hive_posts_cache`, not a scratch table).
- The batch transaction holds row locks for its full duration (up to 1000
  blocks of writes). Keep the sync database dedicated during initial sync --
  API readers contending on locked rows is expected otherwise.
- The 30s statement timeout does not bound the batch: it applies per
  statement, not per transaction. Maintenance SQL keeps its explicit
  exemption (see `db.WithoutStatementTimeout`).

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
- Poison retry rate (`hivemind_indexer_poison_retries_total`; see below)

## Poison-Block Runbook: Orphan Comments

When the indexer logs `poison block` errors with `failure_kind=process` and a
"parent post ... not found" message, a comment references a parent that does
not exist in `hive_posts`. Steem protocol guarantees a parent with any reply
can never be deleted, so a real orphan means hive-side data loss (e.g. manual
row deletion). Recovery options:

1. Re-insert the missing parent row (`author/permlink` from the log's error
   context) and let the block retry proceed.
2. If the parent is genuinely unavailable, an operator may advance the head
   manually (`UPDATE hive_blocks SET ...` / delete the stuck block row) --
   understand that this skips all operations in that block and diverges from
   the reference sync; document the decision for the 20M comparison.

The initial-sync path gives up after 10 consecutive failures (process exits
with a non-zero status); the live path keeps retrying with escalated logs.

### Retry amplification under tx batching (PR#411 review registration)

Whole-batch rollback (§4.1) turns a deterministically failing block anywhere
in a batch into a per-retry cost proportional to the BATCH size N (default
1000), not to the failing block:

- **Re-fetch**: the retry re-fetches the entire batch before its transaction
  opens; the range fetch issues one `get_block` RPC per block, so each retry
  costs ~N RPCs.
- **Re-process**: the up to N-1 blocks before the failing one are processed
  into the open transaction and then rolled back with it — wasted write work
  on every retry.
- **Initial sync** gives up after 10 consecutive failures of the same block:
  for a failing block at the batch tail that is ~10×N blocks of ineffective
  fetch+process before the process exits (and a restart starts the cycle
  again from the same head).
- **Live mode** never gives up: one full retry round every sync interval
  (default 3s) plus the batch duration, indefinitely.

Operational signal: `hivemind_indexer_poison_retries_total{phase=initial|live}`
counts every failed sync attempt the poison loops retry (per attempt, not per
block). A sustained nonzero `rate()` on either phase pins a deterministically
failing block — take the block number from the `poison block` log marker (the
error carries the actually-failing block, not the batch start) and apply the
recovery options above.

Follow-up candidate (registered, NOT implemented in this PR): once the poison
threshold is reached, automatically shrink the tx batch to 1 — a
binary-search isolation of the failing block within the batch, converting the
per-retry cost from O(N) RPCs + N blocks of rolled-back processing to
O(log N) lookups plus the single poison block's processing. Until then the
batch size stays fixed and recovery relies on the manual options above (an
operator can also restart with `HIVE_INDEXER_TX_BATCH_BLOCKS=1` to isolate a
poison block's blast radius manually).
