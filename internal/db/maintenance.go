package db

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// WithoutStatementTimeout runs fn inside a transaction with the session
// statement_timeout disabled (`SET LOCAL statement_timeout = 0`).
//
// The indexer applies a per-connection statement_timeout through the DSN
// (see buildDSN, default 30s) so no single query can pin a pooled
// connection indefinitely. Maintenance statements -- the initial-sync tail
// trio (RecoverMissingPosts' gap scans, FeedCache.Rebuild, follow
// ForceRecount) and the cache audit jobs -- legitimately run multi-minute
// SQL on million-row tables during/after a 30M-block sync, and must not be
// killed by that guard. The server side keeps its default timeout: SET
// LOCAL scopes the override to this one transaction, so a pooled
// connection returns to the DSN-configured timeout at COMMIT/ROLLBACK and
// other sessions are never affected.
func WithoutStatementTimeout(ctx context.Context, gdb *gorm.DB, fn func(tx *gorm.DB) error) error {
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL statement_timeout = 0").Error; err != nil {
			return fmt.Errorf("disable statement timeout: %w", err)
		}
		return fn(tx)
	})
}
