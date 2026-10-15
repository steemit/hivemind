package indexer

import (
	"context"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/pkg/logging"
)

// CacheSync prunes hive_posts_cache_temp of rows outside the 90-day hot
// window. Port of hive/indexer/cache_sync.py.
//
// DELETE-only by design: orphan temp rows (post_id no longer in the main
// cache) are removed at delete time by CachedPost.Delete and fork recovery;
// the cold-start backfill happened once in migration v24.
//
// Gating (§2.7): legacy only invokes CacheSync.sync() from live mode
// (sync.py:236-237, every 20 blocks inside listen()) -- never during
// historical catch-up. Catching up on old history inserts rows whose
// created_at is far outside the 90-day window; pruning mid-catch-up would
// delete rows the dual-write just produced. The Go indexer has no separate
// listen() phase, so the gate approximates it: while the indexer trails the
// chain head by more than catchupBlocks, pruning pauses; once it is caught
// up (live), pruning resumes. The sync loop reports the trailing distance
// via SetBehind after every head/lib poll.
type CacheSync struct {
	db     *gorm.DB
	window time.Duration // tick interval
	hotDay int           // retention window in days

	// catchupBlocks is the trailing-distance threshold (blocks) at or below
	// which the indexer counts as live.
	catchupBlocks int64
	// behind is the distance (blocks) from the chain's last irreversible
	// block to the indexed head, published by the sync loop. -1 means
	// unknown (treat as catching up -- prune stays paused until the sync
	// loop reports).
	behind atomic.Int64

	// batch is the prune batch size (rows per DELETE statement).
	batch int

	syncing atomic.Bool
	logger  *zap.Logger
}

// catchupPaused reports whether background maintenance must pause: the
// trailing distance from the chain head is unknown (< 0 -- before the sync
// loop's first report) or beyond the catch-up threshold. Shared by
// CacheSync's prune gate and the cache-jobs tick gate (PR#411 review) so
// both consumers keep identical pause semantics.
func catchupPaused(behind, threshold int64) bool {
	return behind < 0 || behind > threshold
}

// CacheSync defaults (legacy SYNC_WINDOW = 60s, HOT_DAYS = 90).
const (
	CacheSyncWindow  = 60 * time.Second
	CacheSyncHotDays = 90
	// CacheSyncCatchupBlocks is the default trailing-distance gate
	// (blocks). Legacy live mode trails the head by at most 100 blocks
	// (stream max_gap), so anything farther is catch-up.
	CacheSyncCatchupBlocks = 100
	// CacheSyncPruneBatch is the default prune batch size (rows per DELETE
	// statement).
	CacheSyncPruneBatch = 10000
)

// NewCacheSync creates the temp-table pruner. The catch-up gate starts in
// the paused state (behind = unknown); the owner wires the configured
// threshold via SetCatchupThreshold and the live distance via SetBehind.
func NewCacheSync(database *gorm.DB) *CacheSync {
	c := &CacheSync{
		db:     database,
		window: CacheSyncWindow,
		hotDay: CacheSyncHotDays,
		batch:  CacheSyncPruneBatch,
		logger: logging.GetLogger().With(zap.String("component", "cache-sync")),
	}
	c.catchupBlocks = CacheSyncCatchupBlocks
	c.behind.Store(-1)
	return c
}

// SetPruneBatch overrides the prune batch size (rows per DELETE statement).
// Tests use it to drive the batched path with a handful of rows.
func (c *CacheSync) SetPruneBatch(n int) {
	if n <= 0 {
		n = CacheSyncPruneBatch
	}
	c.batch = n
}

// SetCatchupThreshold configures the trailing-distance gate (blocks).
func (c *CacheSync) SetCatchupThreshold(blocks int64) {
	if blocks < 0 {
		blocks = 0
	}
	c.catchupBlocks = blocks
}

// SetBehind publishes the current distance between the chain head (last
// irreversible block) and the indexed head. Called by the sync loop after
// every head/lib poll; -1 marks the distance unknown (prune paused).
func (c *CacheSync) SetBehind(behind int64) {
	c.behind.Store(behind)
}

// Run ticks until ctx is cancelled, invoking Sync each window. Intended to
// run as a background goroutine started by Sync.
func (c *CacheSync) Run(ctx context.Context) {
	ticker := time.NewTicker(c.window)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.Sync(ctx)
		}
	}
}

// Sync prunes out-of-window rows; non-blocking: skips if a run is active.
func (c *CacheSync) Sync(ctx context.Context) {
	if !c.syncing.CompareAndSwap(false, true) {
		c.logger.Debug("previous sync still running, skip")
		return
	}
	defer c.syncing.Store(false)

	if behind := c.behind.Load(); catchupPaused(behind, c.catchupBlocks) {
		c.logger.Debug("catch-up in progress; temp-table prune paused",
			zap.Int64("behind", behind),
			zap.Int64("threshold", c.catchupBlocks))
		return
	}
	c.prune(ctx)
}

// prune deletes out-of-window rows in fixed-size batches and returns the
// total number of rows removed.
//
// Batching (PR#407 review): after a 0->30M initial sync the temp table
// holds tens of millions of cold rows and the FIRST live-mode prune must
// remove essentially all of them. A single giant DELETE would be killed by
// the DSN's 30s statement_timeout -- and having been killed, it would retry
// forever on the next ticks without ever shrinking the tail, while the one
// huge rolled-back transaction bloats the table with dead tuples. Deleting
// in slices of `batch` rows (via ctid, using the created_at index) keeps
// every statement short, and COMMITTING each slice lets autovacuum reclaim
// space incrementally instead of after one monolithic transaction. Each
// batch runs inside WithoutStatementTimeout so the session guard can never
// kill even an unlucky batch; the 60s lock_timeout it installs bounds any
// queue behind a concurrent reader.
func (c *CacheSync) prune(ctx context.Context) int64 {
	cutoff := time.Now().UTC().AddDate(0, 0, -c.hotDay)
	// Shutdown between batches (loop condition) keeps what already
	// committed; the next boot's first live tick resumes from the remainder.
	var total int64
	for ctx.Err() == nil {
		var deleted int64
		err := db.WithoutStatementTimeout(ctx, c.db, func(tx *gorm.DB) error {
			res := tx.Exec(`DELETE FROM hive_posts_cache_temp WHERE ctid IN (
				SELECT ctid FROM hive_posts_cache_temp
				WHERE created_at < ?
				LIMIT ?)`, cutoff, c.batch)
			if res.Error != nil {
				return res.Error
			}
			deleted = res.RowsAffected
			return nil
		})
		if err != nil {
			c.logger.Error("prune batch failed (resuming on next tick)",
				zap.Error(err), zap.Int64("deleted_so_far", total))
			break
		}
		total += deleted
		if deleted < int64(c.batch) {
			break // window exhausted
		}
	}
	c.logger.Info("pruned temp rows outside hot window",
		zap.Int64("deleted", total),
		zap.Int("batch", c.batch),
		zap.Time("cutoff", cutoff))
	return total
}
