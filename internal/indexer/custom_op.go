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
// cachedPost is likewise the BlockProcessor's shared CachedPost instance:
// community mute/unmute ops must queue their post on the one dirty queue
// that Sync flushes.
func NewCustomOpProcessor(repo *db.Repository, followIndexer *FollowIndexer, cachedPost *CachedPost, logger *zap.Logger) *CustomOpProcessor {
	if followIndexer == nil {
		panic("NewCustomOpProcessor: followIndexer must not be nil; inject the BlockProcessor's shared FollowIndexer (audit P0-4)")
	}
	return &CustomOpProcessor{
		repo:             repo,
		communityIndexer: NewCommunityIndexer(repo, cachedPost, logger),
		notifyIndexer:    NewNotifyIndexer(repo),
		followIndexer:    followIndexer,
		logger:           logger,
	}
}

// truncateJSON keeps a bounded prefix of a custom_json payload for logging
// (full payloads can be large; the prefix is only an observability hint).
// Truncation counts runes, not bytes, so a multi-byte character is never
// cut in half (a split rune would render as mojibake in the log line).
func truncateJSON(s string) string {
	const max = 200
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// arrayEnvelope validates an already-unmarshalled custom_json payload in the
// canonical on-chain [action, payload] array envelope, mirroring legacy
// hive/utils/json.py valid_op_json: exactly two elements, the first a string
// (the command/action), the second an object. Any other shape (bare object,
// string, number, wrong arity) is rejected (ok == false).
func arrayEnvelope(raw interface{}) (action string, payload map[string]interface{}, ok bool) {
	opArray, isArr := raw.([]interface{})
	if !isArr || len(opArray) != 2 {
		return "", nil, false
	}
	action, actionIsStr := opArray[0].(string)
	payload, payloadIsObj := opArray[1].(map[string]interface{})
	if !actionIsStr || !payloadIsObj {
		return "", nil, false
	}
	return action, payload, true
}

// parseOpEnvelope parses the `json` field of a custom_json operation and
// requires the [action, payload] array envelope (legacy valid_op_json).
// Malformed JSON and any other shape are rejected (ok == false).
func parseOpEnvelope(jsonStr string) (action string, payload map[string]interface{}, ok bool) {
	var raw interface{}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return "", nil, false
	}
	return arrayEnvelope(raw)
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
	return arrayEnvelope(raw)
}

// ProcessOps processes a list of custom JSON operations. Error handling
// follows the legacy blocks.process_multi contract (P1-1): an operation
// that fails with an infrastructure error (DB/RPC/context -- anything
// IsInfraError) aborts the whole batch by returning the error, so the
// sync layer retries the block with its transaction rolled back and no op
// is lost; a data error (opValidationError: deterministic bad chain data)
// is logged with the block/op identification and the op is skipped.
func (cop *CustomOpProcessor) ProcessOps(ctx context.Context, tx *gorm.DB, ops []map[string]interface{}, blockNum int64, blockDate time.Time, isInitialSync bool) error {
	for _, op := range ops {
		opID, _ := op["id"].(string)
		requiredAuths, _ := op["required_posting_auths"].([]interface{})

		// Only process known operation IDs
		if opID != "follow" && opID != "community" && opID != "notify" {
			continue
		}

		// Legacy _get_auth: custom_json ops carrying active auths are
		// skipped outright (the follow/community/notify plugins only ever
		// use a single posting auth).
		activeAuths, _ := op["required_auths"].([]interface{})
		if len(activeAuths) > 0 {
			cop.logger.Warn("Unexpected active auths in custom_json; skipping op",
				zap.Int64("block", blockNum),
				zap.String("id", opID))
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

		// skipOp logs a data error and drops the op (block continues);
		// infraErr fails the whole block so sync retries it.
		skipOp := func(err error) {
			cop.logger.Warn("Skipping custom_json op (data error)",
				zap.Int64("block", blockNum),
				zap.String("id", opID),
				zap.String("account", account),
				zap.String("json", truncateJSON(jsonStr)),
				zap.Error(err))
		}
		infraErr := func(err error) error {
			return fmt.Errorf("custom_json op failed (id %s, account %s, block %d): %w", opID, account, blockNum, err)
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
					if IsInfraError(err) {
						return infraErr(err)
					}
					skipOp(err)
				}
			case "reblog":
				if err := cop.ProcessReblog(ctx, tx, account, payload, blockDate, isInitialSync); err != nil {
					if IsInfraError(err) {
						return infraErr(err)
					}
					skipOp(err)
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
				if err := cop.communityIndexer.ProcessCommunityOp(ctx, tx, account, jsonStr, blockDate, isInitialSync); err != nil {
					if IsInfraError(err) {
						return infraErr(err)
					}
					skipOp(err)
				}
			}
		case "notify":
			// Process notify operations
			if err := cop.processNotify(ctx, tx, account, jsonStr, blockDate); err != nil {
				if IsInfraError(err) {
					return infraErr(err)
				}
				skipOp(err)
			}
		}
	}

	return nil
}

// naiveDateTimeLayout mirrors legacy hive/utils/json.py valid_date: Steem
// timestamps are naive (no timezone designator), formatted
// YYYY-MM-DDTHH:MM:SS. RFC3339 parsing would reject them.
const naiveDateTimeLayout = "2006-01-02T15:04:05"

// processNotify processes notify-plugin custom_json operations (id ==
// "notify"), mirroring legacy custom_op.py _process_notify:
//
//   - the payload must be a ["setLastRead", {...}] array envelope; any other
//     envelope shape is skipped (legacy valid_op_json assertion);
//   - the payload must carry exactly the `date` key (legacy
//     valid_keys(required=['date']) with no optional keys);
//   - the date must be a naive YYYY-MM-DDTHH:MM:SS string (legacy
//     valid_date regex rejects timezone suffixes AND fractional seconds /
//     non-padded fields, enforced by a format round-trip) and must not be
//     after the block date (legacy asserts date <= block_date; future dates
//     are rejected, not clamped).
//
// Validation failures skip the op (legacy logs a warning); only the
// SetLastRead write error propagates.
func (cop *CustomOpProcessor) processNotify(ctx context.Context, tx *gorm.DB, account, jsonStr string, blockDate time.Time) error {
	command, payload, ok := parseOpEnvelope(jsonStr)
	if !ok {
		cop.logger.Debug("Dropping non-array/invalid notify envelope",
			zap.String("account", account),
			zap.String("json", truncateJSON(jsonStr)))
		return nil
	}
	if command != "setLastRead" {
		cop.logger.Warn("notify op fail: invalid command",
			zap.String("account", account),
			zap.String("command", command))
		return nil
	}
	if len(payload) != 1 {
		cop.logger.Warn("notify op fail: payload keys must be exactly ['date']",
			zap.String("account", account))
		return nil
	}
	dateStr, isStr := payload["date"].(string)
	if !isStr {
		cop.logger.Warn("notify op fail: missing date in setLastRead",
			zap.String("account", account))
		return nil
	}
	readDate, err := time.Parse(naiveDateTimeLayout, dateStr)
	if err != nil {
		cop.logger.Warn("notify op fail: invalid date format",
			zap.String("account", account),
			zap.String("date", dateStr))
		return nil
	}
	// Strict round-trip: Go's time.Parse accepts extensions the legacy
	// VALID_DATE regex (`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d$`) rejects --
	// fractional seconds (`...T07:17:23.5`) and non-padded fields.
	// Re-formatting with the layout and comparing pins the exact form.
	if readDate.Format(naiveDateTimeLayout) != dateStr {
		cop.logger.Warn("notify op fail: date not in naive YYYY-MM-DDTHH:MM:SS form",
			zap.String("account", account),
			zap.String("date", dateStr))
		return nil
	}
	if readDate.After(blockDate) {
		cop.logger.Warn("notify op fail: date is after block date",
			zap.String("account", account),
			zap.String("date", dateStr))
		return nil
	}

	// Update notification last read time. The write rides the block
	// transaction (P1-15: a pool-side UPDATE here would survive a block
	// rollback and update an account the block never committed to).
	if err := cop.notifyIndexer.WithTx(tx).SetLastRead(ctx, account, readDate); err != nil {
		return fmt.Errorf("failed to set last read: %w", err)
	}

	cop.logger.Debug("Set last read",
		zap.String("account", account),
		zap.Time("date", readDate))
	return nil
}

// ProcessReblog processes a reblog operation. All reads and writes go
// through the block transaction (P1-15): pool-bound reads could not see
// rows created earlier in the same block, and a pool-bound write would
// survive a block rollback as an orphan row. Deterministic chain-data
// failures (missing author/permlink, unknown post/account, non-root post)
// are data errors -- legacy returns silently in all these cases, so here
// they return opValidationError and the caller skips the op while the
// block continues; infrastructure failures propagate for block-level retry.
func (cop *CustomOpProcessor) ProcessReblog(ctx context.Context, tx *gorm.DB, account string, opJSON map[string]interface{}, blockDate time.Time, isInitialSync bool) error {
	author, _ := opJSON["author"].(string)
	permlink, _ := opJSON["permlink"].(string)

	if author == "" || permlink == "" {
		return opValidationErr("missing author or permlink in reblog op")
	}

	// Get post ID
	postRepo := db.NewPostRepository(cop.repo.WithTx(tx))
	post, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
	if err != nil {
		return fmt.Errorf("failed to get post: %w", err)
	}
	if post == nil {
		return opValidationErr("reblog post not found: @%s/%s", author, permlink)
	}

	// Only root posts can be reblogged
	if post.Depth != 0 {
		cop.logger.Warn("Attempted to reblog non-root post",
			zap.String("author", author),
			zap.String("permlink", permlink))
		return nil
	}

	// Get account ID
	accountRepo := db.NewAccountRepository(cop.repo.WithTx(tx))
	accountObj, err := accountRepo.GetByName(ctx, account)
	if err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}
	if accountObj == nil {
		return opValidationErr("reblog account not found: %s", account)
	}

	reblogRepo := db.NewReblogRepository(cop.repo.WithTx(tx))
	feedCacheRepo := db.NewFeedCacheRepository(cop.repo.WithTx(tx))

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
				return fmt.Errorf("failed to delete from feed cache: %w", err)
			}
		}

		cop.logger.Debug("Deleted reblog",
			zap.String("account", account),
			zap.String("author", author),
			zap.String("permlink", permlink))
	} else {
		// Create reblog. The insert mirrors legacy
		// `ON CONFLICT (account, post_id) DO NOTHING`: a duplicate pair is
		// an idempotent no-op (inserted == 0, no error). Any real failure is
		// infrastructure and propagates for block-level retry (PR#406 audit
		// P2: no swallowed DB errors on write paths -- and no SQL-level
		// duplicate error either, which would abort the transaction).
		reblog := &models.Reblog{
			Account:   account,
			PostID:    post.ID,
			CreatedAt: blockDate,
		}
		inserted, err := reblogRepo.Create(ctx, reblog)
		if err != nil {
			return fmt.Errorf("failed to create reblog: %w", err)
		}
		if inserted == 0 {
			cop.logger.Debug("Reblog already exists",
				zap.String("account", account),
				zap.Int64("post_id", post.ID))
			return nil
		}

		// Add to feed cache (only if not in initial sync)
		if !isInitialSync {
			if err := feedCacheRepo.Insert(ctx, post.ID, accountObj.ID, blockDate); err != nil {
				return fmt.Errorf("failed to insert into feed cache: %w", err)
			}
		}

		// Send notification to post author
		// TODO: Implement notification for reblog (#407)
		cop.logger.Debug("Created reblog",
			zap.String("account", account),
			zap.String("author", author),
			zap.String("permlink", permlink))
	}

	return nil
}
