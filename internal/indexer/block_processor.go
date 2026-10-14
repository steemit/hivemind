package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/logging"
)

// BlockProcessor processes blockchain blocks
type BlockProcessor struct {
	db               *db.DB
	repo             *db.Repository
	steem            steem.Provider
	cachedPost       *CachedPost
	accounts         *AccountIndexer
	posts            *PostIndexer
	follows          *FollowIndexer
	payments         *PaymentIndexer
	customOps        *CustomOpProcessor
	communityIndexer *CommunityIndexer
	logger           *zap.Logger
}

// NewBlockProcessor creates a new block processor. The steemProvider is passed
// down to indexers that need to hit steemd (AccountIndexer.Flush, and
// CachedPost's get_content_batch flush). Pass nil only in tests that exercise
// pure-DB code paths.
func NewBlockProcessor(database *db.DB, repo *db.Repository, steemProvider steem.Provider) *BlockProcessor {
	logger := logging.GetLogger().With(zap.String("component", "block-processor"))

	cachedPost := NewCachedPost(database.DB, steemProvider)
	// Reply/mention/vote notifications ride along the cache flush.
	NewPostNotifs(database.DB, cachedPost, NewNotifyIndexer(repo)).Install()

	// One FollowIndexer shared by the block processor (which flushes count
	// deltas after each batch) and the custom-op processor (which feeds it
	// from follow ops). A second instance would silently lose every delta
	// accumulated through it (audit P0-4).
	follows := NewFollowIndexer(repo, logger)

	return &BlockProcessor{
		db:               database,
		repo:             repo,
		steem:            steemProvider,
		cachedPost:       cachedPost,
		accounts:         NewAccountIndexer(repo, logger, steemProvider),
		posts:            NewPostIndexer(repo, cachedPost, logger),
		follows:          follows,
		payments:         NewPaymentIndexer(repo, logger),
		customOps:        NewCustomOpProcessor(repo, follows, cachedPost, logger),
		communityIndexer: NewCommunityIndexer(repo, cachedPost, logger),
		logger:           logger,
	}
}

// CachedPost exposes the shared CachedPost instance (used by Sync to flush
// after each block batch).
func (bp *BlockProcessor) CachedPost() *CachedPost {
	return bp.cachedPost
}

// Follows exposes the follow indexer (Sync flushes count deltas).
func (bp *BlockProcessor) Follows() *FollowIndexer {
	return bp.follows
}

// Accounts exposes the account indexer (Sync flushes dirty accounts).
func (bp *BlockProcessor) Accounts() *AccountIndexer {
	return bp.accounts
}

// ProcessBlock processes a single block
func (bp *BlockProcessor) ProcessBlock(ctx context.Context, block map[string]interface{}, isInitialSync bool) error {
	// Start transaction
	tx := bp.db.DB.WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to start transaction: %w", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			panic(r)
		}
	}()

	// Process block
	if err := bp.processBlockInTx(ctx, tx, block, isInitialSync); err != nil {
		tx.Rollback()
		return err
	}

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// processBlockInTx processes a block within a transaction
func (bp *BlockProcessor) processBlockInTx(ctx context.Context, tx *gorm.DB, block map[string]interface{}, isInitialSync bool) error {
	// Extract block metadata
	blockNum := int64(block["block_num"].(float64))
	blockHash := block["block_id"].(string)
	prevHash := block["previous"].(string)
	timestamp := block["timestamp"].(string)

	blockDate, err := time.Parse("2006-01-02T15:04:05", timestamp)
	if err != nil {
		return fmt.Errorf("failed to parse block timestamp: %w", err)
	}

	// Insert block
	blockModel := &models.Block{
		Num:       blockNum,
		Hash:      blockHash,
		Prev:      &prevHash,
		TXs:       0, // Will be set from transactions
		Ops:       0, // Will be set from operations
		CreatedAt: blockDate,
	}

	if err := tx.Create(blockModel).Error; err != nil {
		return fmt.Errorf("failed to insert block: %w", err)
	}

	// Process transactions
	transactions, ok := block["transactions"].([]interface{})
	if !ok {
		return fmt.Errorf("invalid transactions format")
	}

	accountNames := make(map[string]bool)
	var jsonOps []map[string]interface{}
	var trxIDs []string

	txCount := int16(len(transactions))
	opCount := int16(0)

	for txIdx, txInterface := range transactions {
		txData, ok := txInterface.(map[string]interface{})
		if !ok {
			continue
		}

		// Get transaction ID
		if txIDList, ok := block["transaction_ids"].([]interface{}); ok && txIdx < len(txIDList) {
			if txID, ok := txIDList[txIdx].(string); ok {
				trxIDs = append(trxIDs, txID)
			}
		}

		// Process operations
		operations, ok := txData["operations"].([]interface{})
		if !ok {
			continue
		}

		for _, opInterface := range operations {
			opArray, ok := opInterface.([]interface{})
			if !ok || len(opArray) != 2 {
				continue
			}

			opType := opArray[0].(string)
			opValue := opArray[1].(map[string]interface{})

			opCount++

			// Process operation. Error classification mirrors legacy
			// blocks.process_multi (P1-1): an infrastructure failure
			// (DB/RPC/context) fails the whole block so the sync layer
			// retries it with the transaction rolled back and no op lost;
			// a data error (deterministically bad chain data) is logged
			// with the block/op identification and the op is skipped.
			if err := bp.processOperation(ctx, tx, opType, opValue, blockDate, blockNum, isInitialSync, accountNames, &jsonOps); err != nil {
				if IsInfraError(err) {
					return fmt.Errorf("block %d: op %s failed (infrastructure): %w", blockNum, opType, err)
				}
				bp.logger.Warn("Skipping operation (data error)",
					zap.String("type", opType),
					zap.Int64("block", blockNum),
					zap.Error(err))
			}
		}
	}

	// Update block operation counts
	blockModel.TXs = txCount
	blockModel.Ops = opCount
	if err := tx.Save(blockModel).Error; err != nil {
		return fmt.Errorf("failed to update block counts: %w", err)
	}

	// Register new accounts (and any communities among them) BEFORE
	// processing custom_json operations, mirroring legacy blocks.py
	// (`Accounts.register(...)` then `CustomOp.process_ops(...)`): a block
	// can create an account and emit a follow/community op for it in the
	// same block, and those ops resolve the account through this
	// transaction's uncommitted rows (P1-3).
	accountNamesList := make([]string, 0, len(accountNames))
	for name := range accountNames {
		accountNamesList = append(accountNamesList, name)
	}
	if len(accountNamesList) > 0 {
		if err := bp.accounts.Register(ctx, tx, accountNamesList, blockDate); err != nil {
			return fmt.Errorf("failed to register accounts: %w", err)
		}

		// Check if any new accounts are communities and register them.
		// Legacy runs this inside Accounts.register (same transaction); a
		// failure fails the block.
		if err := bp.communityIndexer.Register(ctx, tx, accountNamesList, blockDate); err != nil {
			return fmt.Errorf("failed to register communities: %w", err)
		}
	}

	// Process custom JSON operations (follow/reblog/community/notify) after
	// registration. Any error escaping ProcessOps is an infrastructure
	// failure (data errors are classified and skipped inside) and fails the
	// block for retry (P1-1).
	if len(jsonOps) > 0 {
		if err := bp.customOps.ProcessOps(ctx, tx, jsonOps, blockNum, blockDate, isInitialSync); err != nil {
			return fmt.Errorf("block %d: custom ops failed: %w", blockNum, err)
		}
	}

	// Save transaction IDs. hive_trxid_block_num is part of the strict
	// checksum comparison, so a failure must fail the block (legacy
	// _save_trxids runs inside the process_multi transaction) rather than
	// commit the block with missing rows.
	if len(trxIDs) > 0 {
		if err := bp.saveTransactionIDs(ctx, tx, trxIDs, blockNum); err != nil {
			return fmt.Errorf("block %d: %w", blockNum, err)
		}
	}

	return nil
}

// saveTransactionIDs saves transaction IDs to hive_trxid_block_num table.
// Runs inside the block transaction; any failure propagates so the block is
// retried whole instead of committing with missing trxid rows.
func (bp *BlockProcessor) saveTransactionIDs(ctx context.Context, tx *gorm.DB, trxIDs []string, blockNum int64) error {
	if len(trxIDs) == 0 {
		return nil
	}

	// Batch insert transaction IDs
	records := make([]models.TransactionBlock, 0, len(trxIDs))
	for _, trxID := range trxIDs {
		if trxID == "" {
			continue
		}
		records = append(records, models.TransactionBlock{
			TrxID:    sql.NullString{String: trxID, Valid: true},
			BlockNum: blockNum,
		})
	}

	if len(records) == 0 {
		return nil
	}

	// Batch insert with ON CONFLICT DO NOTHING. The legacy v19 schema uses a
	// PARTIAL unique index (hive_trxid_ix1 ON (trx_id) WHERE trx_id IS NOT
	// NULL); the conflict target must repeat the index predicate for
	// PostgreSQL to infer it -- without the WHERE clause the statement is
	// rejected outright ("no unique or exclusion constraint matching the
	// ON CONFLICT specification"), which used to send every block down the
	// per-row fallback path.
	values := make([]string, 0, len(records))
	args := make([]interface{}, 0, len(records)*2)
	for _, r := range records {
		values = append(values, "(?, ?)")
		args = append(args, r.TrxID.String, r.BlockNum)
	}
	if err := tx.WithContext(ctx).
		Exec("INSERT INTO hive_trxid_block_num (trx_id, block_num) VALUES "+
			strings.Join(values, ", ")+
			" ON CONFLICT (trx_id) WHERE trx_id IS NOT NULL DO NOTHING",
			args...).Error; err != nil {
		// Fallback to individual inserts if batch fails. A failure here
		// leaves the transaction aborted, so the fallback errors too and
		// the error propagates to fail the block (P1-1: no swallowed
		// writes on the trxid path).
		for _, record := range records {
			if err := tx.WithContext(ctx).
				Where("trx_id = ?", record.TrxID).
				FirstOrCreate(&record).Error; err != nil {
				return fmt.Errorf("failed to save transaction ID %s: %w", record.TrxID.String, err)
			}
		}
	}

	return nil
}

// extractAccountName pulls the account name that a create/pow-style operation
// introduces, registering it into accountNames. This is the pure (DB-free)
// part of processOperation's account branch, factored out for unit testing.
// Returns true if an account name was registered.
func extractAccountName(opType string, opValue map[string]interface{}, accountNames map[string]bool) bool {
	switch opType {
	case "pow_operation":
		if worker, ok := opValue["worker_account"].(string); ok && worker != "" {
			accountNames[worker] = true
			return true
		}
	case "pow2_operation":
		if work, ok := opValue["work"].(map[string]interface{}); ok {
			if value, ok := work["value"].(map[string]interface{}); ok {
				if input, ok := value["input"].(map[string]interface{}); ok {
					if worker, ok := input["worker_account"].(string); ok && worker != "" {
						accountNames[worker] = true
						return true
					}
				}
			}
		}
	case "account_create_operation", "account_create_with_delegation_operation", "create_claimed_account_operation":
		if name, ok := opValue["new_account_name"].(string); ok && name != "" {
			accountNames[name] = true
			return true
		}
	}
	return false
}

// processOperation processes a single operation
func (bp *BlockProcessor) processOperation(
	ctx context.Context,
	tx *gorm.DB,
	opType string,
	opValue map[string]interface{},
	blockDate time.Time,
	blockNum int64,
	isInitialSync bool,
	accountNames map[string]bool,
	jsonOps *[]map[string]interface{},
) error {
	switch opType {
	// Account operations — registration candidates (pure extraction).
	case "pow_operation", "pow2_operation",
		"account_create_operation", "account_create_with_delegation_operation",
		"create_claimed_account_operation":
		extractAccountName(opType, opValue, accountNames)
	case "account_update_operation":
		if !isInitialSync {
			if account, ok := opValue["account"].(string); ok {
				bp.accounts.MarkDirty(account)
			}
		}
	case "account_update2_operation":
		if !isInitialSync {
			if account, ok := opValue["account"].(string); ok {
				bp.accounts.MarkDirty(account)
			}
		}

	// Post operations
	case "comment_operation":
		return bp.posts.ProcessComment(ctx, tx, opValue, blockDate, isInitialSync)
	case "delete_comment_operation":
		return bp.posts.ProcessDelete(ctx, tx, opValue)

	// Vote operations
	case "vote_operation":
		if !isInitialSync {
			author, _ := opValue["author"].(string)
			voter, _ := opValue["voter"].(string)
			permlink, _ := opValue["permlink"].(string)

			if author != "" {
				bp.accounts.MarkDirty(author)
			}
			if voter != "" {
				bp.accounts.MarkDirty(voter)
			}

			// Queue the post for a cache update (upvote level); the id is
			// resolved lazily at flush time when not known.
			if author != "" && permlink != "" {
				bp.cachedPost.Vote(author, permlink, 0, voter)
			}
		}

	// Transfer operations
	case "transfer_operation":
		return bp.payments.ProcessTransfer(ctx, tx, opValue, blockNum, blockDate)

	// Custom JSON operations
	case "custom_json_operation":
		*jsonOps = append(*jsonOps, opValue)
	}

	return nil
}
