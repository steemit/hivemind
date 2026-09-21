# Indexer (block sync and entity indexing)

`internal/indexer/` + `cmd/indexer/main.go`. The only writer to the chain-fact
tables; everything it produces for reads flows through derived tables
maintained here.

## Sync loop (sync.go)

`Sync.Run(ctx)`:

1. **Initial-sync detection**: `hive_feed_cache` row count == 0 ⇒ initial
   sync. Fast-sync blocks from DB head to last-irreversible, then the three
   repair passes: `CachedPost.RecoverMissingPosts`,
   `FeedCacheRepository.Rebuild`, `Follows().ForceRecount`.
2. Background goroutines: `CacheSync` (prunes the temp cache table outside
   the 90-day hot window, 60 s tick) and the optional jobs loop
   (`HIVE_JOBS_INTERVAL`, 0 = disabled) running the cache audits.
3. **Steady state**: poll LIB every `SyncInterval` s; when trailing, fetch
   `[head+1, LIB]` in `MaxBatch`-sized batches; per block
   `BlockProcessor.ProcessBlock`; **after each batch** the three flushes
   (post cache → follow deltas → accounts).

Fork policy: irreversible-only (Strategy B) — no fork rollback exists or is
needed; Strategy A (live head + rollback) is an explicit not-implemented
stub.

Block-insert note: `hive_blocks` insert is not `ON CONFLICT`-guarded, but the
loop re-reads head from the DB every iteration, so a lost commit-ack
self-heals on the next poll. Deterministic per-block failures retry the same
block forever (visible in logs).

## BlockProcessor (block_processor.go)

One DB transaction per block:

```
Begin
 ├─ insert hive_blocks row (counts finalized later)
 ├─ for each tx/op: processOperation switch
 │    ├─ account ops (pow/pow2/account_create*) → collect names (pure extraction)
 │    ├─ account_update[2] → Accounts.MarkDirty (live sync only)
 │    ├─ comment / delete_comment → PostIndexer (inside tx)
 │    ├─ vote → MarkDirty(author,voter) + CachedPost.Vote (live sync only)
 │    ├─ transfer → PaymentIndexer (inside tx)
 │    └─ custom_json → buffered into jsonOps
 ├─ customOps.ProcessOps (follow/reblog/community/notify)
 ├─ accounts.Register (collected names) → communityIndexer.Register
 ├─ saveTransactionIDs (ON CONFLICT DO NOTHING batch)
 └─ update block counts, Commit
```

Op-dispatch errors are currently logged-and-skipped (legacy fails the whole
batch and retries) — a known tracked divergence; see pitfalls before relying
on retry semantics.

### custom_json envelope (custom_op.go)

On-chain custom_json payloads are **array envelopes**
`["command", {payload}]` (`["follow", {...}]`, `["reblog", {...}]`,
`["subscribe", {...}]`, `["setLastRead", {...}]`). The processor switches on
the op `id` field: `"follow"` (follow/reblog commands), `"community"`
(gated `blockNum > START_BLOCK = 37,500,000`), `"notify"` (setLastRead).
When extending: parse the envelope first, then hand the payload object to
the entity indexer. (The historical failure mode — expecting a bare object
and silently dropping every real chain op — is documented in pitfalls.)

## Entity indexers

| Component | File | Responsibility |
|-----------|------|----------------|
| `PostIndexer` | post_indexer.go | comment_op (create/edit/undelete), delete_op; parent resolution, depth/category inheritance, feed-cache insert/evict |
| `CachedPost` | cached_post.go | the derived post cache engine (below) |
| `AccountIndexer` + `account_update.go` + `account_rank.go` | | registration, dirty-set, steemd `get_accounts` flush (~17 derived columns), vote-weight ranks |
| `FollowIndexer` | follow_indexer.go | hive_follows state machine + count deltas + ForceRecount |
| `CommunityIndexer` | community_indexer.go | community registration (hive-1xxxxx accounts), community ops (subscribe/role/title/mute/pin/props) |
| `PaymentIndexer` | payment_indexer.go | SBD transfers to `@author/permlink` memos → promoted amounts + payment rows |
| `NotifyIndexer` + notifs.go | | notification writes + reply/mention/vote generation riding the cache flush |
| `CustomOpProcessor` | custom_op.go | envelope dispatch (above) |
| `jobs.go` / `cache_sync.go` | | audit jobs / temp-table pruning |

## CachedPost — the derived-cache engine (the most intricate component)

**Dual tables**: every write hits both `hive_posts_cache` (full history) and
`hive_posts_cache_temp` (90-day hot window mirror with `_synced_at`).

**Dirty queue** (in-memory `map[url]int`, url = `author/permlink`), levels by
ascending priority (re-dirtying keeps the smaller number):

| Level | Value | Meaning |
|-------|-------|---------|
| insert | 0 | new post (writes immutable columns too) |
| payout | 1 | cashout settlement rescan |
| update | 2 | edit |
| upvote | 3 | vote activity |
| recount | 4 | children count changed |

Auxiliary maps: `ids` (url→post_id cache), `noids` (urls pending ID
resolution via `PostsHelper.URLsToIDs` before flush), `pendingPromted`
(promoted amounts to fold into the next write of that post), `votes` (per-url
voter list consumed by `PopPendingVoters` for vote notifications).

**Flush** (per batch, trx=false; recovery paths use trx=true): resolve noids
→ drain by level (urls sorted per level for determinism) → fetch cores from
steemd `get_content` in id-ordered 1000-chunks → overlay core fields from
`hive_posts` (`category`, `community_id`, moderation flags — the fact table
is authoritative for these) → `buildSQLs` per post → execute (optionally in
one transaction). Blank steemd responses: deleted posts log an error;
otherwise the post is re-queued (node lag defer). `recount` on a comment
bubbles to its parent (chain re-count propagation).

**`last_id` cursor**: "cache covered up to post_id X", lazily initialized
from `MAX(post_id)`; bumped per processed pid with a paranoid gap check
(`ensureSafeGap` — the interval must contain no live posts). Undelete uses
it to decide insert-vs-force-insert a 4-column placeholder row (whose
`payout_at` default '1990-01-01' makes the payout scan adopt it).

**Recovery**: `RecoverMissingPosts` loops `dirtyMissing` (gap between
`MAX(hive_posts.id)` and `MAX(cache.post_id)`) until closed. `DirtyPaidouts`
re-dirties settled-but-unflagged posts (`is_paidout=false AND payout_at <=
now`) — the sweep that keeps trending/hot honest.

**Tags**: root posts diff `hive_post_tags` on insert/update (delete removed,
`ON CONFLICT DO NOTHING` add new — collation-safe).

## Account flush and ranks

Registration inserts `(name, created_at)`; the dirty set is flushed per batch
in ≤1000-name chunks through steemd `get_accounts`, computing the 17 derived
columns (profile extraction, vote_weight = vests + received − delegated,
proxy_weight, active_at, sanitized `raw_json`, …). `rank` = position by
`vote_weight DESC`, bucketed into notification `default_score`s
(<200→70, <1000→60, <6500→50, <25k→40, <100k→30, else 20).

## Follow deltas

`hive_follows` rows (state bitmask bit0=blog, bit1=ignore) are written inside
the block tx; follower/following counts are **not** updated per row — deltas
accumulate in memory and `Flush` emits grouped
`UPDATE hive_accounts SET followers = followers + ? WHERE id IN (...)`
statements (mirroring legacy's bulk flush). `ForceRecount` rebuilds both
count columns from scratch.

## Audit jobs (jobs.go)

1. `AuditCacheMissing` — left-join sweep for live posts lacking cache rows →
   queue at insert level.
2. `AuditCacheDeleted` — cache rows for deleted posts → evict.
3. `AuditCacheUndelete` — deleted posts whose author/permlink re-exist on
   chain (checked via `get_content`) → rebuild the fact row and re-enter the
   cache pipeline.

## Recipes

### Adding support for a new chain operation

1. Add the case to `block_processor.go::processOperation()` — immediate tx
   writes call an entity indexer with `tx`; steemd-dependent work queues a
   dirty entry instead.
2. Implement the entity handler in the owning indexer file; follow the
   legacy module for exact semantics (`hive/indexer/*.py`).
3. If it must ride the cache flush, add a dirty level / hook in
   `cached_post.go` instead of calling steemd inside the block tx.
4. Update `AGENTS.md`/docs and add a test using `withMigratedDB` +
   `fakeSteemProvider` (see kr3/kr4 tests for the pattern).

### Adding a derived cache column

1. Column on `models.PostCache` + migration (mind the Undelete placeholder
   path — column needs a default).
2. Compute in `scoring.go` (`ComputePostBasic/Payout/Stats`).
3. Assemble in `cached_post.go::buildSQLs` choosing the write level:
   immutable columns = insert only; content = insert/payout/update;
   statistics = all levels.
4. Partial index in the migration if queried (follow the ix2–ix34 pattern).
5. Account-column analog: `account_update.go::buildAccountUpdate`.

## Reuse inventory

`steem.Provider` + `fakeSteemProvider` (test base) · `withMigratedDB(t)` ·
`normalize.go` helpers (parse_amount / parse_time / rep_log10 — all verified
against legacy) · the dirty-queue pattern for any new expensive propagation ·
`PostsHelper.URLsToIDs` batch resolver.
