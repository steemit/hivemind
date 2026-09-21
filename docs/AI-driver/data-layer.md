# Data Layer (db / models / cache)

The data layer is `internal/db` (connection + repositories + migrations),
`internal/models` (GORM models), and `internal/cache` (optional Redis). It is
consumed by both binaries; the API server only reads, the indexer writes.

## Connection bootstrap

`db.New(cfg)` (internal/db/db.go):

1. Opens GORM with the pgx v5 driver.
2. Installs a zap-backed GORM logger (`IgnoreRecordNotFoundError: true`,
   slow-query threshold 1s) and forces `NowFunc` to UTC.
3. Injects `options=-c statement_timeout=<ms>` into the DSN so **every
   pooled connection** carries the statement timeout (`0` disables). Note:
   long-running maintenance SQL (feed-cache rebuild, force recounts, cache
   audits) runs under the same timeout as request traffic — size
   `HIVE_DB_STATEMENT_TIMEOUT` accordingly or expect cancels at real scale.
4. Applies pool settings (MaxOpen/MaxIdle/ConnMaxLifetime/ConnMaxIdleTime)
   and pings with a 5 s deadline.

Migrations run through a **separate raw connection** (no statement timeout)
via `db.RunMigrations`.

## Repository pattern

- `Repository{db *gorm.DB}` is the single concrete base
  (internal/db/repository.go); there is deliberately **no Go interface** —
  tests use real PostgreSQL schemas, not mocks.
- Ten specialized repositories (`AccountRepository`, `BlockRepository`,
  `PostRepository`, `PostStatusRepository`, `StateRepository`,
  `NotificationRepository`, `CommunityRepository`, `FeedCacheRepository`,
  `ReblogRepository`, `FollowRepository`) **embed** `*Repository` and are
  constructed per-use with `db.NewXxxRepository(repo)`.
- House conventions:
  - Single-row getters return `(nil, nil)` on not-found
    (`errors.Is(err, gorm.ErrRecordNotFound)` → `nil, nil`).
  - List getters return an empty (non-nil) slice.
  - Writes return a bare `error`.
- **The documented escape hatch**: `Repository.DB()` exposes the raw
  `*gorm.DB`. Complex read paths (cursor pagination, the get_discussion CTE,
  post hydration) legitimately use it with hand-written SQL/GORM clauses.
  The escape hatch is for *reads*; indexer *writes* must go through the
  block transaction, not the pool (see design-philosophy §4 and pitfalls).
- Consequence of dual access styles: **a column rename touches four
  places** — model, migration SQL, repository methods, raw SQL strings in
  `condenser/cursor.go` / `bridge/` / `indexer`. Grep the column name repo-wide
  before renaming anything.

## Migrations

- golang-migrate v4 with `//go:embed all:migrations` over
  `internal/db/migrations/NNNN_name.{up,down}.sql`.
- Current chain: `0001_init_schema` (baseline replicating legacy production
  schema at DB_VERSION 29: v19 trxid table, v20 posts_status, v21 INCLUDE
  indexes, v22–v28 index/autovacuum tuning, seed rows) and
  `0002_payments_extras` (Go-added `hive_payments.memo/created_at`).
- **Baseline semantics**: on a database provisioned by *legacy* hivemind,
  first start requires `HIVE_DB_MIGRATE_FORCE=true`, which stamps version 1
  (baseline) without executing DDL, then `Up()` applies only the delta. On a
  fresh database, plain `HIVE_DB_MIGRATE=true` builds everything.
- Both binaries may run migrations; golang-migrate takes a Postgres advisory
  lock, so concurrent starts serialize safely (production servers should
  still set `HIVE_DB_MIGRATE=false`).

### Adding a table or column (recipe)

1. Write `internal/db/migrations/NNNN_name.up.sql` + `.down.sql` — embedded
   automatically.
2. Add/extend the model in `internal/models/`: `TableName()` plus explicit
   `gorm:"column:..."` tags on every field; nullable columns use
   `sql.NullXxx`.
3. Register the table in **both** maps of `internal/db/model_schema_align_test.go`
   (`expected` tables / `modelCols`) — these tests assert model↔schema
   column parity in both directions against a real database.
4. Optional specialized repository; wire handlers in `router.go` if
   API-facing.
5. Run `HIVE_TEST_DATABASE_URL=... go test ./internal/db/...`.

## Model inventory (17 tables)

| Table | Model file | Keys | Notes |
|-------|-----------|------|-------|
| hive_state | state.go | PK block_num | global state; Go indexer currently does not maintain the price/version columns |
| hive_blocks | block.go | PK num, UX hash | `prev` self-FK |
| hive_accounts | account.go | PK id, UX name | `raw_json` = sanitized steemd account JSON; rank/followers/following/vote_weight are derived |
| hive_posts | post.go | PK id, UX(author,permlink) | facts; community_id/is_pinned/is_muted are community state |
| hive_post_tags | post.go | UNIQUE(tag,post_id) | derived tag index |
| hive_follows | follow.go | UNIQUE(following,follower) | `state` bitmask: bit0=blog, bit1=ignore |
| hive_reblogs | reblog.go | UNIQUE(account,post_id) | account is a *name*, not id |
| hive_payments | payment.go | PK id | FKs to accounts/posts; memo/created_at are Go additions |
| hive_feed_cache | cache.go | UNIQUE(post_id,account_id) | materialized feed; rebuildable |
| hive_posts_cache | cache.go | PK post_id | wide derived read model (main API table) |
| hive_posts_cache_temp | cache.go | PK post_id | 90-day hot-window mirror + `_synced_at` |
| hive_communities | community.go | PK id (=account id), UX name | settings JSON text |
| hive_roles / hive_subscriptions | community.go | UNIQUE pairs | role_id −2…8 |
| hive_notifs | notification.go | PK id | nullable FK columns; payload text |
| hive_posts_status | state.go | UX(list_type,post_id,author) | external moderation list; **written by tooling, not the indexer** |
| hive_trxid_block_num | state.go | partial UX(trx_id) | get_transaction lookup |

GORM relationship tags exist but are essentially unused — joins are explicit
(an intentional divergence from ORM-heavy style; keeps SQL legible and
portable from legacy).

## Redis cache (internal/cache)

- `cache.New(cfg)` returns `(nil, nil)` when disabled; every method is
  nil-receiver safe and returns `ErrCacheDisabled` — callers treat cache as
  best-effort.
- Keys are namespaced `hivemind:<key>`; `HashKey(parts...)` MD5-composes
  short keys. TTLs are caller-defined (bridge ranked uses legacy-matched
  per-sort TTLs).
- **Known trap**: `Delete`/`Exists` historically bypassed the namespace
  prefix — always route through `namespaceKey` when adding methods.
- There is **no active invalidation**: the indexer is a separate process; the
  only consistency mechanism is TTL. Keep TTLs short for sorts that lag
  (created: 3 s) and longer for slowly-moving ones.
- `Cache` holds a fixed `context.Background()` for commands; request
  cancellation does not interrupt Redis calls (accepted trade-off).

## Test foundations (reuse these)

- `internal/db/test_helpers_test.go::columnNames(model)` — extracts column
  names via GORM's own schema parser; powers the alignment tests.
- `internal/indexer/block_processor_test.go::withMigratedDB(t)` — drops and
  recreates the public schema, runs migrations, returns a small-pool
  `*db.DB`; the base for all indexer integration tests (auto-skips without
  `HIVE_TEST_DATABASE_URL`).
- `internal/indexer/fake_provider_test.go` — programmable `steem.Provider`
  fake (function injection + call counters); no mock framework anywhere, by
  design.
