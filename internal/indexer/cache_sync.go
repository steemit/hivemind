package indexer

import (
	"context"
	"sync"
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
type CacheSync struct {
	db     *gorm.DB
	window time.Duration // tick interval
	hotDay int           // retention window in days

	syncing atomic.Bool
	logger  *zap.Logger
}

// CacheSync defaults (legacy SYNC_WINDOW = 60s, HOT_DAYS = 90).
const (
	CacheSyncWindow  = 60 * time.Second
	CacheSyncHotDays = 90
)

// NewCacheSync creates the temp-table pruner.
func NewCacheSync(database *gorm.DB) *CacheSync {
	return &CacheSync{
		db:     database,
		window: CacheSyncWindow,
		hotDay: CacheSyncHotDays,
		logger: logging.GetLogger().With(zap.String("component", "cache-sync")),
	}
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

// syncMu guards the shared-instance helpers below (tests / manual triggers).
var syncMu sync.Mutex
