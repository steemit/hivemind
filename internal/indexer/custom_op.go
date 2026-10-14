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

// followPluginBarePayloadMaxBlock mirrors the legacy compat cutoff in
// hive/indexer/custom_op.py (load_json_key): before block 6,000,000 some
// clients sent the bare follow payload object instead of the
// ["follow", payload] array; legacy wraps those as ["follow", payload].
// From block 6,000,000 on, only the array envelope is accepted and bare
// objects are skipped.
const followPluginBarePayloadMaxBlock = 6000000

// CustomOpProcessor processes custom JSON operations
type CustomOpProcessor struct {
	repo             *db.Repository
	communityIndexer *CommunityIndexer
	notifyIndexer    *NotifyIndexer
	followIndexer    *FollowIndexer
	logger           *zap.Logger
}

// NewCustomOpProcessor creates a new custom op processor. followIndexer must
// be the same instance the owning BlockProcessor holds and flushes: the
// follow indexer accumulates count deltas in memory, so a privately-owned
// instance would never be flushed and its counts would be silently lost
// (audit P0-4). A nil followIndexer therefore fails fast here -- constructing
// a fallback instance in its place would resurrect exactly that bug.
func NewCustomOpProcessor(repo *db.Repository, followIndexer *FollowIndexer, logger *zap.Logger) *CustomOpProcessor {
	if followIndexer == nil {
		panic("NewCustomOpProcessor: followIndexer must not be nil; inject the BlockProcessor's shared FollowIndexer (audit P0-4)")
	}
	return &CustomOpProcessor{
		repo:             repo,
		communityIndexer: NewCommunityIndexer(repo, logger),
		notifyIndexer:    NewNotifyIndexer(repo),
		followIndexer:    followIndexer,
		logger:           logger,
	}
}

// truncateJSON keeps a bounded prefix of a custom_json payload for logging
// (full payloads can be large; the prefix is only an observability hint).
func truncateJSON(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// parseFollowPluginOp normalizes the `json` payload of a follow-plugin
// custom_json operation (id == "follow"), mirroring legacy custom_op.py
// (_process_legacy + load_json_key legacy compat):
//
//   - the payload must be a ["follow"|"reblog", payload] array whose second
//     element is an object; anything else is skipped (ok == false);
//   - before followPluginBarePayloadMaxBlock a bare payload object is
//     wrapped as ["follow", payload];
//   - at or after that block, bare objects are skipped, exactly as legacy
//     does.
func parseFollowPluginOp(jsonStr string, blockNum int64) (cmd string, payload map[string]interface{}, ok bool) {
	var raw interface{}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return "", nil, false
	}
	if blockNum < followPluginBarePayloadMaxBlock {
		if obj, isObj := raw.(map[string]interface{}); isObj {
			raw = []interface{}{"follow", obj}
		}
	}
	opArray, isArr := raw.([]interface{})
	if !isArr || len(opArray) != 2 {
		return "", nil, false
	}
	cmd, cmdIsStr := opArray[0].(string)
	payload, payloadIsObj := opArray[1].(map[string]interface{})
	if !cmdIsStr || !payloadIsObj {
		return "", nil, false
	}
	return cmd, payload, true
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
			// Follow-plugin ops (id == "follow") cover both follow and
			// reblog commands, distinguished by the envelope's first
			// element: ["follow", {...}] or ["reblog", {...}]. Envelope
			// shapes that are not a [command, payload] pair are skipped,
			// mirroring legacy custom_op.py._process_legacy.
			cmd, payload, ok := parseFollowPluginOp(jsonStr, blockNum)
			if !ok {
				// Silent drop mirrors legacy, but keep it observable at Debug
				// level (unknown envelope shapes were previously invisible).
				cop.logger.Debug("Dropping non-array/invalid follow-plugin envelope",
					zap.Int64("block", blockNum),
					zap.String("account", account),
					zap.String("json", truncateJSON(jsonStr)))
				continue
			}
			switch cmd {
			case "follow":
				if err := cop.followIndexer.ProcessFollow(ctx, tx, account, payload, blockDate, isInitialSync); err != nil {
					cop.logger.Warn("Failed to process follow",
						zap.String("account", account),
						zap.Error(err))
				}
			case "reblog":
				if err := cop.ProcessReblog(ctx, tx, account, payload, blockDate, isInitialSync); err != nil {
					cop.logger.Warn("Failed to process reblog",
						zap.String("account", account),
						zap.Error(err))
				}
			default:
				cop.logger.Debug("Dropping unknown follow-plugin command",
					zap.Int64("block", blockNum),
					zap.String("account", account),
					zap.String("cmd", cmd),
					zap.String("json", truncateJSON(jsonStr)))
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
