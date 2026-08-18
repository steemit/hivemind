package indexer

import (
	"context"
	"sync"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/pkg/logging"
)

// accountRankCache is the process-wide vote_weight rank map (legacy
// Accounts._ranks / fetch_ranks). Rank is 1-based: rank 1 = highest
// vote_weight. Shared by notification scoring and account flush.
type accountRankCache struct {
	db *gorm.DB

	mu     sync.RWMutex
	ranks  map[int64]int
	loaded bool
	logger *zap.Logger
}

var (
	rankCacheMu sync.Mutex
	rankCache   *accountRankCache
)

// sharedRankCache returns the process-wide cache bound to db.
func sharedRankCache(db *gorm.DB) *accountRankCache {
	rankCacheMu.Lock()
	defer rankCacheMu.Unlock()
	if rankCache == nil {
		rankCache = &accountRankCache{
			db:     db,
			logger: logging.GetLogger().With(zap.String("component", "account-ranks")),
		}
	}
	return rankCache
}

// Rank returns the 1-based rank of an account (ok=false when unranked).
func (a *accountRankCache) Rank(ctx context.Context, accountID int64) (int, bool) {
	a.ensureLoaded(ctx)
	a.mu.RLock()
	rank, ok := a.ranks[accountID]
	a.mu.RUnlock()
	return rank, ok
}

// Reload rebuilds the rank map (e.g. after a bulk account refresh).
func (a *accountRankCache) Reload(ctx context.Context) {
	var ids []int64
	if err := a.db.WithContext(ctx).
		Table("hive_accounts").
		Select("id").
		Order("vote_weight DESC").
		Scan(&ids).Error; err != nil {
		a.logger.Warn("rank reload failed", zap.Error(err))
		return
	}
	ranks := make(map[int64]int, len(ids))
	for i, id := range ids {
		ranks[id] = i + 1
	}
	a.mu.Lock()
	a.ranks = ranks
	a.loaded = true
	a.mu.Unlock()
}

func (a *accountRankCache) ensureLoaded(ctx context.Context) {
	a.mu.RLock()
	loaded := a.loaded
	a.mu.RUnlock()
	if !loaded {
		a.Reload(ctx)
	}
}

// defaultScoreForAccount maps a vote_weight rank to a notification score
// (legacy Accounts.default_score thresholds).
func defaultScoreForAccount(ctx context.Context, gdb *gorm.DB, accountID int64) int16 {
	rank, ok := sharedRankCache(gdb).Rank(ctx, accountID)
	if !ok {
		rank = 1000000
	}
	switch {
	case rank < 200:
		return 70 // top 0.02%
	case rank < 1000:
		return 60 // top 0.1%
	case rank < 6500:
		return 50 // top 0.5%
	case rank < 25000:
		return 40 // top 2%
	case rank < 100000:
		return 30 // top 8%
	default:
		return 20
	}
}
