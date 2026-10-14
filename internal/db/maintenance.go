package db

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// WithoutStatementTimeout runs fn inside a transaction with the session
// statement_timeout disabled (`SET LOCAL statement_timeout = 0`) and a 60s
// lock_timeout installed in its place.
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
//
// The lock_timeout is the counterweight (PR#407 review): Rebuild's TRUNCATE
// and the audits' bulk writes need ACCESS EXCLUSIVE / row locks, and with
// statement_timeout = 0 NOTHING would bound the time spent queueing behind
// a long reader -- the maintenance transaction would hang indefinitely,
// pinning a pooled connection and cascading the stall into the whole
// indexer. 60s bounds only the lock WAIT, never the statement itself: a
// statement that acquires its locks may then run as long as it needs.
func WithoutStatementTimeout(ctx context.Context, gdb *gorm.DB, fn func(tx *gorm.DB) error) error {
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL statement_timeout = 0").Error; err != nil {
			return fmt.Errorf("disable statement timeout: %w", err)
		}
		if err := tx.Exec("SET LOCAL lock_timeout = '60s'").Error; err != nil {
			return fmt.Errorf("set lock timeout: %w", err)
		}
		return fn(tx)
	})
}
