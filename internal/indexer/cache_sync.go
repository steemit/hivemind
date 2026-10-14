package indexer

import (
	"context"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

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

	syncing atomic.Bool
	logger  *zap.Logger
}

// CacheSync defaults (legacy SYNC_WINDOW = 60s, HOT_DAYS = 90).
const (
	CacheSyncWindow  = 60 * time.Second
	CacheSyncHotDays = 90
	// CacheSyncCatchupBlocks is the default trailing-distance gate
	// (blocks). Legacy live mode trails the head by at most 100 blocks
	// (stream max_gap), so anything farther is catch-up.
	CacheSyncCatchupBlocks = 100
)

// NewCacheSync creates the temp-table pruner. The catch-up gate starts in
// the paused state (behind = unknown); the owner wires the configured
// threshold via SetCatchupThreshold and the live distance via SetBehind.
func NewCacheSync(database *gorm.DB) *CacheSync {
	c := &CacheSync{
		db:     database,
		window: CacheSyncWindow,
		hotDay: CacheSyncHotDays,
		logger: logging.GetLogger().With(zap.String("component", "cache-sync")),
	}
	c.catchupBlocks = CacheSyncCatchupBlocks
	c.behind.Store(-1)
	return c
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

	if behind := c.behind.Load(); behind < 0 || behind > c.catchupBlocks {
		c.logger.Debug("catch-up in progress; temp-table prune paused",
			zap.Int64("behind", behind),
			zap.Int64("threshold", c.catchupBlocks))
		return
	}
	c.prune(ctx)
}

func (c *CacheSync) prune(ctx context.Context) {
	cutoff := time.Now().UTC().AddDate(0, 0, -c.hotDay)
	res := c.db.WithContext(ctx).
		Exec("DELETE FROM hive_posts_cache_temp WHERE created_at < ?", cutoff)
	if res.Error != nil {
		c.logger.Error("prune failed", zap.Error(res.Error))
		return
	}
	c.logger.Info("pruned temp rows outside hot window",
		zap.Int64("deleted", res.RowsAffected),
		zap.Time("cutoff", cutoff))
}
