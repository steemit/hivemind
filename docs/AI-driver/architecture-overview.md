# Architecture Overview

Hivemind is a "consensus interpretation" layer for the Steem blockchain: it
follows the chain through a `steemd` node, materializes the social state
(posts, follows, communities, notifications, payments) into PostgreSQL, and
serves it over JSON-RPC as a more flexible alternative to the raw `steemd`
API. The Go codebase is a rewrite of the original Python implementation
(`hivemind_legacy`), which remains the behavioral reference.

## Two binaries, one shared core

| Binary | Entry | Role | Lifecycle shape |
|--------|-------|------|-----------------|
| API server | `cmd/server/main.go` | Read-only JSON-RPC over HTTP (Gin), `POST /` | Serve until SIGTERM; `http.Server.Shutdown(10s)` drains in-flight requests |
| Indexer | `cmd/indexer/main.go` | Long-running writer: pulls blocks from `steemd`, processes them into PostgreSQL | Runs until context cancel; batch flushes after each block batch |

Both share `pkg/` (config, logging, telemetry) and `internal/` (db, models,
cache, steem). `internal/` is not importable outside the module by design —
anything reusable belongs in `pkg/`.

```
                    ┌──────────────────────── indexer ────────────────────────┐
 Steem chain        │                                                          │
    │               │  steemd JSON-RPC ──► internal/steem (Provider iface)    │
    │               │        │                                                 │
    ▼               │        ▼                                                 │
 nodes running      │  Sync (Strategy B: poll last-irreversible)               │
 steemd ◄───────────┤        │  batches of MaxBatch blocks                     │
                    │        ▼                                                 │
                    │  BlockProcessor ── one DB transaction per block ──┐      │
                    │   ├─ posts / deletes / payments / custom_ops      │      │
                    │   ├─ account registration (+ community detect)   │      │
                    │   └─ trxid map                                  │      │
                    │        │  after each batch:                       │      │
                    │        ▼                                         ▼      │
                    │  CachedPost.Flush / Follows.Flush /            PostgreSQL│
                    │  Accounts.FlushBatch  (steemd round-trips)               │
                    └──────────────────────────────────────────────────────────┘
                    ┌────────────────────── API server ───────────────────────┐
                    │  POST / (JSON-RPC 2.0)                                  │
                    │    └─ JSONRPCHandler.Handle → methods map (router.go)   │
                    │        └─ namespace handlers (condenser/bridge/hive)    │
                    │            └─ Cursor + repositories + PostLoader        │
                    │                  ├─ PostgreSQL (read)                   │
                    │                  └─ Redis (optional, ranked-results)    │
                    └──────────────────────────────────────────────────────────┘
```

## The read path (API)

1. Client sends a JSON-RPC 2.0 body to `POST /`.
2. `internal/api/jsonrpc.go` binds and validates the envelope, splits
   `namespace.method`, looks the method up in the handler map, opens a child
   span named after the method, and calls the handler.
3. `internal/api/router.go::registerMethods()` is the **single source of
   truth** for which methods exist and which struct serves them. Alias
   namespaces (`follow_api.*`, `tags_api.*`) register the same handler
   function value as their `condenser_api.*` counterpart.
4. Handlers read through two layers:
   - `internal/db` repositories (GORM) for simple CRUD;
   - `internal/api/condenser/cursor.go` — the ID-cursor query layer shared by
     all three namespaces — plus `internal/api/objects.PostLoader`, which
     hydrates IDs into full response objects in one of two shapes
     (condenser shape vs bridge shape; see [api-layer.md](api-layer.md)).
5. Optional Redis (`internal/cache`) caches `bridge.get_ranked_posts`
   results per (sort, start, limit, tag) with legacy-aligned TTLs.

## The write path (indexer)

`internal/indexer/sync.go::Run`:

1. **Initial-sync detection**: `hive_feed_cache` empty ⇒ initial sync. Fast
   forward from DB head to last-irreversible block, then recover missing
   cache rows, rebuild the feed cache, force a follow recount.
2. **Steady state (Strategy B)**: poll `get_dynamic_global_properties` every
   `SyncInterval` seconds; when the DB head trails LIB, pull the missing
   blocks in `MaxBatch`-sized batches via `steem.GetBlocksRange`.
3. **Per block**: `BlockProcessor.ProcessBlock` opens one transaction:
   insert the `hive_blocks` row, dispatch each operation, then (in order)
   custom-json ops, account registration, community registration, trxid map.
4. **Per batch, after the blocks commit**: three flush phases that all need
   `steemd` round-trips —
   - `CachedPost.Flush` — dual-write the derived post cache (see below),
   - `Follows().Flush` — apply pending follower/following count deltas as
     batched `UPDATE ... SET n = n + ? WHERE id IN (...)`,
   - `Accounts().FlushBatch` — re-fetch dirty accounts from `steemd`
     `get_accounts` and refresh their ~17 derived columns.
5. Background goroutines: `CacheSync` prunes `hive_posts_cache_temp` outside
   the 90-day hot window (60 s tick); the optional jobs loop
   (`HIVE_JOBS_INTERVAL`, 0 = off) runs cache-consistency audits.

Fork policy: **irreversible-blocks-only** (Strategy B). The indexer never
indexes past LIB, so fork rollback is not implemented; legacy's `verify_head`
fork check has no Go equivalent by design. Strategy A (live head with trail
and rollback) is an explicit not-implemented stub.

## The data model: facts vs derived state

Understanding this split is the key to not corrupting the indexer:

| Layer | Tables | Written by | Rebuildable? |
|-------|--------|-----------|--------------|
| **Chain facts** | `hive_blocks`, `hive_accounts` (identity cols), `hive_posts`, `hive_follows`, `hive_reblogs`, `hive_payments`, `hive_communities`, `hive_roles`, `hive_subscriptions`, `hive_trxid_block_num`, `hive_notifs` | BlockProcessor transaction, per op | No (only by re-syncing blocks) |
| **Derived read model** | `hive_posts_cache` (+`_temp` mirror), `hive_post_tags`, `hive_feed_cache`, count columns on `hive_accounts` (`followers`, `following`, `rank`, `vote_weight`, …) and `hive_communities` (`subscribers`, `rank`) | Flush phases, rebuild/audit jobs | Yes — that is what the audit jobs and initial-sync rebuild do |

`hive_posts_cache` is the **primary API read table**: a wide (~32 column)
denormalized row per post (title, preview, votes CSV, payout splits, display
flags, `sc_trend`/`sc_hot` sort scores). It is filled from `steemd
get_content` responses, not from `hive_posts`. The `CachedPost` component
queues *dirty* posts (levels: insert / update / upvote / recount), and the
flush phase resolves unknown IDs, fetches cores from `steemd`, and writes the
cache row plus its tag rows and post-notifications in one pass.

`hive_feed_cache` is the materialized blog/feed view (post_id, account_id)
that powers `get_discussions_by_feed` / `blog`; rebuildable via
`FeedCacheRepository.Rebuild`.

## Observability

- **Tracing**: OTLP/HTTP exporter (`pkg/telemetry`); `jsonrpc.handle` root
  span per request + one child span per RPC method. Indexer hot paths are
  spanned through `telemetry.StartSpanWithName`.
- **Metrics**: `promauto` metric families in `pkg/telemetry/metrics.go`,
  recorded via `telemetry.Record*` helpers from the JSON-RPC layer and
  repositories. (Exposing the `/metrics` endpoint is a known open gap — see
  [pitfalls.md](pitfalls.md).)
- **Logging**: global zap logger via `logging.GetLogger()`; every component
  must attach `zap.String("component", "...")`. Optional Scalyr-compatible
  JSON encoder.

## Configuration & deployment

- Config: Viper, `HIVE_` env prefix, snake_case keys, defaults registered in
  **both** `Load()` and `setDefaults()` (see
  [infrastructure.md](infrastructure.md) for the rule and the reason).
- Deployment: `docker-compose.yml` runs postgres, redis (optional), server,
  indexer, jaeger. Health endpoints: `GET /health`,
  `GET /.well-known/healthcheck.json`.

## Where to make which kind of change

| You are adding… | Start in |
|-----------------|----------|
| An RPC method | `internal/api/<namespace>/` + `router.go::registerMethods()` (full recipe in [api-layer.md](api-layer.md)) |
| Support for a chain operation | `block_processor.go::processOperation()` switch + the entity indexer (full recipe in [indexer.md](indexer.md)) |
| A table / column | migration in `internal/db/migrations/` + model in `internal/models/` + alignment test entry (full recipe in [data-layer.md](data-layer.md)) |
| A config knob | `pkg/config/config.go` — struct field, `Load()`, `setDefaults()`, `Validate()` (four places, see [infrastructure.md](infrastructure.md)) |
| A cached/derived column | `CachedPost.buildSQLs` + the flush path (see [indexer.md](indexer.md)) |
