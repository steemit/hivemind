package indexer

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/logging"
)

// BlockProcessor processes blockchain blocks
type BlockProcessor struct {
	db              *db.DB
	repo            *db.Repository
	accounts        *AccountIndexer
	posts           *PostIndexer
	follows         *FollowIndexer
	payments        *PaymentIndexer
	customOps       *CustomOpProcessor
	communityIndexer *CommunityIndexer
	logger          *zap.Logger
}

// NewBlockProcessor creates a new block processor
func NewBlockProcessor(database *db.DB, repo *db.Repository) *BlockProcessor {
	logger := logging.GetLogger().With(zap.String("component", "block-processor"))
	
	return &BlockProcessor{
		db:              database,
		repo:            repo,
		accounts:        NewAccountIndexer(repo, logger),
		posts:           NewPostIndexer(repo, logger),
		follows:         NewFollowIndexer(repo, logger),
		payments:        NewPaymentIndexer(repo, logger),
		customOps:       NewCustomOpProcessor(repo, logger),
		communityIndexer: NewCommunityIndexer(repo, logger),
		logger:          logger,
	}
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

			// Process operation
			if err := bp.processOperation(ctx, tx, opType, opValue, blockDate, blockNum, isInitialSync, accountNames, &jsonOps); err != nil {
				bp.logger.Error("Failed to process operation",
					zap.String("type", opType),
					zap.Int64("block", blockNum),
					zap.Error(err))
				// Continue processing other operations
			}
		}
	}

	// Update block operation counts
	blockModel.TXs = txCount
	blockModel.Ops = opCount
	if err := tx.Save(blockModel).Error; err != nil {
		return fmt.Errorf("failed to update block counts: %w", err)
	}

	// Process custom JSON operations
	if len(jsonOps) > 0 {
		if err := bp.customOps.ProcessOps(ctx, tx, jsonOps, blockNum, blockDate, isInitialSync); err != nil {
			bp.logger.Error("Failed to process custom ops", zap.Error(err))
		}
	}

	// Register new accounts
	accountNamesList := make([]string, 0, len(accountNames))
	for name := range accountNames {
		accountNamesList = append(accountNamesList, name)
	}
	if len(accountNamesList) > 0 {
		if err := bp.accounts.Register(ctx, tx, accountNamesList, blockDate); err != nil {
			return fmt.Errorf("failed to register accounts: %w", err)
		}
		
		// Check if any new accounts are communities and register them
		if err := bp.communityIndexer.Register(ctx, tx, accountNamesList, blockDate); err != nil {
			bp.logger.Warn("Failed to register communities", zap.Error(err))
			// Don't fail block processing for community registration errors
		}
	}

	// Save transaction IDs
	if len(trxIDs) > 0 {
		if err := bp.saveTransactionIDs(ctx, tx, trxIDs, blockNum); err != nil {
			bp.logger.Warn("Failed to save transaction IDs", zap.Error(err))
			// Don't fail the block processing for this
		}
	}

	return nil
}

// saveTransactionIDs saves transaction IDs to hive_trxid_block_num table
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
			TrxID:    trxID,
			BlockNum: blockNum,
		})
	}

	if len(records) == 0 {
		return nil
	}

	// Use ON CONFLICT DO NOTHING to handle duplicates
	if err := tx.WithContext(ctx).
		Exec("INSERT INTO hive_trxid_block_num (trx_id, block_num) VALUES "+
			"(?, ?) ON CONFLICT (trx_id) DO NOTHING",
			records[0].TrxID, records[0].BlockNum).Error; err != nil {
		// Fallback to individual inserts if batch fails
		for _, record := range records {
			if err := tx.WithContext(ctx).
				Where("trx_id = ?", record.TrxID).
				FirstOrCreate(&record).Error; err != nil {
				bp.logger.Warn("Failed to save transaction ID",
					zap.String("trx_id", record.TrxID),
					zap.Int64("block", blockNum),
					zap.Error(err))
			}
		}
	}

	return nil
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
	// Account operations
	case "pow_operation":
		if worker, ok := opValue["worker_account"].(string); ok {
			accountNames[worker] = true
		}
	case "pow2_operation":
		if work, ok := opValue["work"].(map[string]interface{}); ok {
			if value, ok := work["value"].(map[string]interface{}); ok {
				if input, ok := value["input"].(map[string]interface{}); ok {
					if worker, ok := input["worker_account"].(string); ok {
						accountNames[worker] = true
					}
				}
			}
		}
	case "account_create_operation":
		if name, ok := opValue["new_account_name"].(string); ok {
			accountNames[name] = true
		}
	case "account_create_with_delegation_operation":
		if name, ok := opValue["new_account_name"].(string); ok {
			accountNames[name] = true
		}
	case "create_claimed_account_operation":
		if name, ok := opValue["new_account_name"].(string); ok {
			accountNames[name] = true
		}
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
			
			// Mark post for cache update
			if author != "" && permlink != "" {
				if err := bp.posts.MarkPostDirty(ctx, tx, author, permlink); err != nil {
					bp.logger.Warn("Failed to mark post dirty for vote",
						zap.String("author", author),
						zap.String("permlink", permlink),
						zap.Error(err))
				}
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

