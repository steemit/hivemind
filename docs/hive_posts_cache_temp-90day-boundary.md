# hive_posts_cache_temp and 90-Day Boundary Evaluation

This document records the evaluation of the temp table (`hive_posts_cache_temp`) routing logic and whether list APIs automatically fall back to `hive_posts_cache` when "load more" crosses the ~90-day boundary.

## Summary

- **List APIs (including "load more")**: They do **not** automatically read from `hive_posts_cache` at the 90-day boundary. Each request uses a single table (temp or main) chosen by `CacheRouter.get_table(sort)`; there is no cursor-based fallback to the main table.
- **By-ID object APIs**: `comments_by_id` queries temp first, then fills missing IDs from main, so it **does** automatically read `hive_posts_cache`. `posts_by_id` uses only the main table.

So: **"Load more" does not automatically read `hive_posts_cache`**; only comment-by-ID resolution falls back to the main table when data is missing from temp.

---

## 1. Temp Table and 90-Day Boundary

- **Temp table**: `hive_posts_cache_temp` holds only the last ~90 days of hot data. Rows older than 90 days are pruned by `CacheSync` (see `hive/indexer/cache_sync.py`).
- **Routing**: In `hive/db/cache_router.py`, `CacheRouter.get_table(sort)` returns the temp table for hot sorts (`trending`, `hot`, `created`, `promoted`, `payout`, `payout_comments`) and the main table otherwise. Routing is based only on `sort`; there is no logic that considers cursor position or proximity to the 90-day cutoff.

---

## 2. List + Pagination ("Load More") Implementation

List and pagination are implemented in `hive/server/bridge_api/cursor.py` and `hive/server/condenser_api/cursor.py`:

- `table = CacheRouter.get_table(sort)` is used once per request; the same table is used for the entire list query and for the cursor/seek condition.
- Pagination uses `seek_id` / `last_id` on that single table with `ORDER BY ... LIMIT`; there is no "query temp then fall back to main" logic.

Example from `pids_by_category` (bridge):

```python
table = CacheRouter.get_table(sort)
# ...
sql = ("""SELECT post_id FROM %s WHERE %s
          ORDER BY %s DESC, post_id LIMIT :limit
          """ % (table, ' AND '.join(where), field))
return await db.query_col(sql, tag=tag, last_id=last_id, limit=limit)
```

So for sorts that use the temp table (e.g. trending, hot, created), the entire list (first page and all "load more" pages) is read only from the temp table. When the user scrolls near the 90-day boundary, the temp table simply has no older rows (they were pruned), so the next page returns fewer or no rows; the code does **not** switch to `hive_posts_cache` for older data.

- If the product expectation is "hot lists only show the last 90 days", the current behavior is correct and does not wrongly read the main table.
- If the product expectation is "load more should continue to show posts older than 90 days", then the current implementation does **not** satisfy that; you would need to add logic to use the main table (or a combined strategy) when the cursor is near or past the 90-day boundary.

---

## 3. APIs That Do Automatically Read hive_posts_cache

Only the **comment-by-ID** path does a temp-then-main fallback. In `hive/server/hive_api/objects.py`, `comments_by_id`:

- Queries the temp table first with the requested IDs.
- Collects which IDs were not found.
- If there are missing IDs, runs the same query against `hive_posts_cache` and appends those rows.

So comments older than 90 days are still returned by reading from the main table when they are missing from temp. List APIs do not have this fallback.

---

## 4. Whether the 90-Day Boundary Is a "Bug"

- **Correctness**: When only the temp table is used for a list, the results are consistent and do not mix in unintended rows from the main table. At the boundary, "load more" simply runs out of rows in temp; there is no automatic read from `hive_posts_cache`.
- **Product expectation**: If the design is "hot lists are 90-day only", there is no bug. If the design is "load more should include data beyond 90 days", then the current list implementation does not do that and would need to be extended (e.g. use main table or union when the cursor is near the 90-day cutoff).

---

## 5. Quick Reference

| Scenario | Automatically reads hive_posts_cache? | Notes |
|----------|--------------------------------------|--------|
| List first page + load more (trending / hot / created, etc.) | **No** | Only temp is used; no fallback at boundary |
| Comment by ID (`comments_by_id`) | **Yes** | Temp first, then main for missing IDs |
| Post by ID (`posts_by_id`) | N/A (main only) | Uses main table only, not temp |

**Direct answer**: "Load more" does **not** automatically read `hive_posts_cache`. Only the comment-by-ID resolution falls back to the main table when IDs are missing from temp. If you want list APIs to continue beyond the 90-day boundary using the main table, that logic would need to be added explicitly (e.g. cursor-based table selection or a combined temp+main query).

---

## Related Code

- `hive/db/cache_router.py` – table selection by `sort`
- `hive/indexer/cache_sync.py` – 90-day pruning of `hive_posts_cache_temp`
- `hive/db/db_state.py` – migration that creates and backfills temp (v24)
- `hive/server/bridge_api/cursor.py` – list + pagination (e.g. `pids_by_category`, `pids_by_community`)
- `hive/server/condenser_api/cursor.py` – `pids_by_query`
- `hive/server/hive_api/objects.py` – `comments_by_id` (temp + main), `posts_by_id` (main only)
