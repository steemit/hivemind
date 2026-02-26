# PR: hive_posts_cache_temp for hot queries (90-day window)

## Summary

This branch introduces a second cache table **`hive_posts_cache_temp`** that holds only the last ~90 days of post cache data. Hot list APIs (trending, hot, created, promoted, payout, payout_comments) are routed to this table to reduce query load on the main `hive_posts_cache` table.

## What’s in this branch

### 1. Temp table and schema
- **`hive/db/schema.py`**: New table `hive_posts_cache_temp` with the same columns as `hive_posts_cache` (plus optional `_synced_at`), and indexes tuned for hot sorts.
- **`hive/db/db_state.py`**: Migration (v24→25) creates the temp table if missing and runs a **one-time cold-start backfill** (INSERT from main table, 90-day window). Uses `SET LOCAL statement_timeout = '0'` for the long-running insert; adapter whitelist updated to allow `SET LOCAL`.

### 2. Query routing
- **`hive/db/cache_router.py`**: `CacheRouter.get_table(sort)` returns `hive_posts_cache_temp` for hot sorts and `hive_posts_cache` otherwise.
- **Bridge / condenser / hive_api**: List and cursor code use `CacheRouter.get_table(sort)` so hot sorts hit the temp table; `comments_by_id` queries temp first and falls back to main for missing IDs; `posts_by_id` remains main-only.

### 3. Writes (dual-write)
- **`hive/indexer/cached_post.py`**: Every INSERT/UPDATE to `hive_posts_cache` is applied to **both** the main and temp table in the **same batch/transaction** (dual-write). Undelete path updated to write to both tables.
- **Logging**: `[DUAL-WRITE] batch: N posts written to main+temp` per batch; `[PREP] posts cache process (main+temp): ...` for large flushes, so operators can confirm dual-write in logs.

### 4. Deletes
- **`hive/indexer/cache_sync.py`**: Every **60s** (triggered from listen when `num % 20 == 0`), a background thread runs `DELETE FROM hive_posts_cache_temp WHERE created_at < :cutoff` (90-day cutoff). No INSERT/sync from main—temp is fed only by dual-write and cold-start backfill.
- **`hive/indexer/cached_post.py`**: `CachedPost.delete()` deletes from both main and temp when a post is removed (e.g. delete_comment).
- **`hive/indexer/blocks.py`**: Fork rollback (`_pop_blocks()`) deletes affected post_ids from both main and temp.

### 5. Documentation
- **`docs/hive_posts_cache_temp-90day-boundary.md`**:
  - Describes routing and 90-day boundary behavior (list APIs do *not* auto-fallback to main at 90 days; only comment-by-ID falls back).
  - **§6** documents all write/delete locations for the temp table (cold-start, dual-write, 60s prune, delete at delete time, fork rollback).

## Design notes

- **Dual-write** was chosen instead of a periodic sync (e.g. chunked INSERT from main every 60s) so that temp and main are updated in the same transaction and write timing is consistent; the previous 60s chunked-sync approach was dropped for performance and simplicity.
- Hot lists are intentionally 90-day only; “load more” does not automatically switch to the main table beyond that window.

## Testing

- New tests: `tests/db/test_cache_router.py`, `tests/db/test_cache_sync.py`.
- Cache sync tests updated (e.g. no INSERT batch size assertions after sync became delete-only).

## Related code (quick ref)

| Area            | Files |
|-----------------|--------|
| Schema / migration | `hive/db/schema.py`, `hive/db/db_state.py` |
| Routing         | `hive/db/cache_router.py` |
| Dual-write      | `hive/indexer/cached_post.py` |
| 90-day prune    | `hive/indexer/cache_sync.py`, `hive/indexer/sync.py` |
| Delete at delete / fork | `hive/indexer/cached_post.py`, `hive/indexer/blocks.py` |
| API usage       | `hive/server/bridge_api/cursor.py`, `hive/server/condenser_api/cursor.py`, `hive/server/hive_api/objects.py`, `hive/server/hive_api/thread.py` |
| Docs            | `docs/hive_posts_cache_temp-90day-boundary.md` |
