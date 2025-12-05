package indexer

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
)

// CustomOpProcessor processes custom JSON operations
type CustomOpProcessor struct {
	repo   *db.Repository
	logger *zap.Logger
}

// NewCustomOpProcessor creates a new custom op processor
func NewCustomOpProcessor(repo *db.Repository, logger *zap.Logger) *CustomOpProcessor {
	return &CustomOpProcessor{
		repo:   repo,
		logger: logger,
	}
}

// ProcessOps processes a list of custom JSON operations
func (cop *CustomOpProcessor) ProcessOps(ctx context.Context, tx *gorm.DB, ops []map[string]interface{}, blockNum int64, blockDate time.Time) error {
	for _, op := range ops {
		opID, _ := op["id"].(string)
		requiredAuths, _ := op["required_posting_auths"].([]interface{})

		// Only process known operation IDs
		if opID != "follow" && opID != "community" && opID != "notify" {
			continue
		}

		// Get account from required_posting_auths
		if len(requiredAuths) != 1 {
			cop.logger.Warn("Unexpected auths in custom_json", zap.Int("count", len(requiredAuths)))
			continue
		}

		account, _ := requiredAuths[0].(string)
		if account == "" {
			continue
		}

		// Parse JSON
		jsonStr, _ := op["json"].(string)
		if jsonStr == "" {
			continue
		}

		// Process based on operation ID
		switch opID {
		case "follow":
			// TODO: Parse and process follow operation
			// This will be handled by FollowIndexer
		case "community":
			// TODO: Process community operations
		case "notify":
			// TODO: Process notify operations (setLastRead, etc.)
		}
	}

	return nil
}

// ProcessReblog processes a reblog operation
func (cop *CustomOpProcessor) ProcessReblog(ctx context.Context, tx *gorm.DB, account string, opJSON map[string]interface{}, blockDate time.Time) error {
	// TODO: Implement reblog processing
	// This should:
	// 1. Get post ID from author/permlink
	// 2. Check if delete flag is set
	// 3. Insert/delete from hive_reblogs
	// 4. Update feed cache
	return fmt.Errorf("reblog processing not yet implemented")
}

