package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// CustomOpProcessor processes custom JSON operations
type CustomOpProcessor struct {
	repo            *db.Repository
	communityIndexer *CommunityIndexer
	notifyIndexer   *NotifyIndexer
	followIndexer   *FollowIndexer
	logger          *zap.Logger
}

// NewCustomOpProcessor creates a new custom op processor
func NewCustomOpProcessor(repo *db.Repository, logger *zap.Logger) *CustomOpProcessor {
	return &CustomOpProcessor{
		repo:            repo,
		communityIndexer: NewCommunityIndexer(repo, logger),
		notifyIndexer:   NewNotifyIndexer(repo),
		followIndexer:   NewFollowIndexer(repo, logger),
		logger:          logger,
	}
}

// ProcessOps processes a list of custom JSON operations
func (cop *CustomOpProcessor) ProcessOps(ctx context.Context, tx *gorm.DB, ops []map[string]interface{}, blockNum int64, blockDate time.Time, isInitialSync bool) error {
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
			// Follow operations can be either follow or reblog
			// Try to parse as array first (legacy format: ["follow", {...}] or ["reblog", {...}])
			var opArray []interface{}
			if err := json.Unmarshal([]byte(jsonStr), &opArray); err == nil && len(opArray) == 2 {
				cmd, _ := opArray[0].(string)
				payload, _ := opArray[1].(map[string]interface{})
				if cmd == "reblog" {
					// Process reblog from legacy format
					if err := cop.ProcessReblog(ctx, tx, account, payload, blockDate, isInitialSync); err != nil {
						cop.logger.Warn("Failed to process reblog",
							zap.String("account", account),
							zap.Error(err))
					}
					continue
				} else if cmd == "follow" {
					// Process follow with legacy format
					// This will be handled by FollowIndexer
					// For now, we just continue
					continue
				}
			}

			// Try to parse as object (new format after block 6000000)
			var opJSON map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &opJSON); err == nil {
				// Check if it's a reblog operation (has author and permlink)
				if _, hasAuthor := opJSON["author"]; hasAuthor {
					if _, hasPermlink := opJSON["permlink"]; hasPermlink {
					// This is a reblog operation
					if err := cop.ProcessReblog(ctx, tx, account, opJSON, blockDate, isInitialSync); err != nil {
							cop.logger.Warn("Failed to process reblog",
								zap.String("account", account),
								zap.Error(err))
						}
						continue
					}
				}

				// Otherwise, it's a follow operation
				// Process follow operation
				if err := cop.followIndexer.ProcessFollow(ctx, tx, account, opJSON, blockDate); err != nil {
					cop.logger.Warn("Failed to process follow",
						zap.String("account", account),
						zap.Error(err))
				}
			}
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
func (cop *CustomOpProcessor) ProcessReblog(ctx context.Context, tx *gorm.DB, account string, opJSON map[string]interface{}, blockDate time.Time, isInitialSync bool) error {
	author, _ := opJSON["author"].(string)
	permlink, _ := opJSON["permlink"].(string)
	
	if author == "" || permlink == "" {
		return fmt.Errorf("missing author or permlink in reblog op")
	}

	// Get post ID
	postRepo := db.NewPostRepository(cop.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
	if err != nil {
		return fmt.Errorf("failed to get post: %w", err)
	}
	if post == nil {
		return fmt.Errorf("post not found: @%s/%s", author, permlink)
	}

	// Only root posts can be reblogged
	if post.Depth != 0 {
		cop.logger.Warn("Attempted to reblog non-root post",
			zap.String("author", author),
			zap.String("permlink", permlink))
		return nil
	}

	// Get account ID
	accountRepo := db.NewAccountRepository(cop.repo)
	accountObj, err := accountRepo.GetByName(ctx, account)
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}
	if accountObj == nil {
		return fmt.Errorf("account not found: %s", account)
	}

	reblogRepo := db.NewReblogRepository(cop.repo)
	feedCacheRepo := db.NewFeedCacheRepository(cop.repo)

	// Check if this is a delete operation
	if deleteFlag, ok := opJSON["delete"].(string); ok && deleteFlag == "delete" {
		// Delete reblog
		if err := reblogRepo.Delete(ctx, account, post.ID); err != nil {
			return fmt.Errorf("failed to delete reblog: %w", err)
		}

		// Remove from feed cache (only if not in initial sync)
		if !isInitialSync {
			accountID := accountObj.ID
			if err := feedCacheRepo.Delete(ctx, post.ID, &accountID); err != nil {
				cop.logger.Warn("Failed to delete from feed cache",
					zap.Int64("post_id", post.ID),
					zap.Int64("account_id", accountID),
					zap.Error(err))
			}
		}

		cop.logger.Debug("Deleted reblog",
			zap.String("account", account),
			zap.String("author", author),
			zap.String("permlink", permlink))
	} else {
		// Create reblog
		reblog := &models.Reblog{
			Account:   account,
			PostID:    post.ID,
			CreatedAt: blockDate,
		}

		if err := reblogRepo.Create(ctx, reblog); err != nil {
			// Ignore duplicate reblog errors
			cop.logger.Debug("Reblog already exists or failed to create",
				zap.String("account", account),
				zap.Int64("post_id", post.ID),
				zap.Error(err))
		} else {
			// Add to feed cache (only if not in initial sync)
			if !isInitialSync {
				if err := feedCacheRepo.Insert(ctx, post.ID, accountObj.ID, blockDate); err != nil {
					cop.logger.Warn("Failed to insert into feed cache",
						zap.Int64("post_id", post.ID),
						zap.Int64("account_id", accountObj.ID),
						zap.Error(err))
				}
			}

			// Send notification to post author
			// TODO: Implement notification for reblog
			cop.logger.Debug("Created reblog",
				zap.String("account", account),
				zap.String("author", author),
				zap.String("permlink", permlink))
		}
	}

	return nil
}

