package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
)

// CustomOpProcessor processes custom JSON operations
type CustomOpProcessor struct {
	repo            *db.Repository
	communityIndexer *CommunityIndexer
	notifyIndexer   *NotifyIndexer
	logger          *zap.Logger
}

// NewCustomOpProcessor creates a new custom op processor
func NewCustomOpProcessor(repo *db.Repository, logger *zap.Logger) *CustomOpProcessor {
	return &CustomOpProcessor{
		repo:            repo,
		communityIndexer: NewCommunityIndexer(repo, logger),
		notifyIndexer:   NewNotifyIndexer(repo),
		logger:          logger,
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
			// Follow operations are handled by FollowIndexer
			// This is just a placeholder
		case "community":
			// Process community operations only after START_BLOCK
			if blockNum > START_BLOCK {
				if err := cop.communityIndexer.ProcessCommunityOp(ctx, tx, account, jsonStr, blockDate); err != nil {
					cop.logger.Warn("Failed to process community op",
						zap.String("account", account),
						zap.String("json", jsonStr),
						zap.Error(err))
				}
			}
		case "notify":
			// Process notify operations
			if err := cop.processNotify(ctx, tx, account, jsonStr, blockDate); err != nil {
				cop.logger.Warn("Failed to process notify op",
					zap.String("account", account),
					zap.Error(err))
			}
		}
	}

	return nil
}

// processNotify processes notify operations (e.g., setLastRead)
func (cop *CustomOpProcessor) processNotify(ctx context.Context, tx *gorm.DB, account, jsonStr string, blockDate time.Time) error {
	var opJSON map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &opJSON); err != nil {
		return fmt.Errorf("failed to parse notify op JSON: %w", err)
	}

	command, ok := opJSON["type"].(string)
	if !ok {
		return nil // Not a valid notify op
	}

	if command == "setLastRead" {
		payload, ok := opJSON["date"].(string)
		if !ok {
			return fmt.Errorf("missing date in setLastRead")
		}

		// Parse date
		readDate, err := time.Parse(time.RFC3339, payload)
		if err != nil {
			return fmt.Errorf("invalid date format: %w", err)
		}

		// Ensure read date is not in the future
		if readDate.After(blockDate) {
			readDate = blockDate
		}

		// Update notification last read time
		if err := cop.notifyIndexer.SetLastRead(ctx, account, readDate); err != nil {
			return fmt.Errorf("failed to set last read: %w", err)
		}

		cop.logger.Debug("Set last read",
			zap.String("account", account),
			zap.Time("date", readDate))
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

