package indexer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// PaymentIndexer handles payment indexing
type PaymentIndexer struct {
	repo   *db.Repository
	logger *zap.Logger
}

// NewPaymentIndexer creates a new payment indexer
func NewPaymentIndexer(repo *db.Repository, logger *zap.Logger) *PaymentIndexer {
	return &PaymentIndexer{
		repo:   repo,
		logger: logger,
	}
}

// ProcessTransfer processes a transfer operation. Reads and writes ride the
// block transaction (P1-15): the post lookup must see posts created earlier
// in the same block, and the promoted update / payment row must roll back
// with the block. Deterministic chain-data failures (unparseable amount,
// unregistered sender/recipient) are data errors (opValidationError): legacy
// returns None from _validated and records nothing; infrastructure failures
// propagate for block-level retry (P1-1).
func (pay *PaymentIndexer) ProcessTransfer(ctx context.Context, tx *gorm.DB, op map[string]interface{}, blockNum int64, blockDate time.Time) error {
	from, _ := op["from"].(string)
	to, _ := op["to"].(string)
	amountStr, _ := op["amount"].(string)
	memo, _ := op["memo"].(string)

	// Only process transfers to 'null' account (promoted posts)
	if to != "null" {
		return nil
	}

	// Parse amount
	amount, err := parseAmount(amountStr)
	if err != nil {
		return opValidationErr("failed to parse amount %q: %v", amountStr, err)
	}

	// Extract token type (should be SBD)
	token := "SBD"
	if strings.Contains(amountStr, "STEEM") {
		token = "STEEM"
	}

	postRepo := db.NewPostRepository(pay.repo.WithTx(tx))

	// Parse memo to get post (format: @author/permlink)
	var postID int64
	if memo != "" && strings.HasPrefix(memo, "@") {
		parts := strings.Split(memo[1:], "/")
		if len(parts) == 2 {
			author := parts[0]
			permlink := parts[1]

			post, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
			if err != nil {
				return fmt.Errorf("failed to get post %s/%s: %w", author, permlink, err)
			}
			if post != nil {
				postID = post.ID

				// Update post promoted amount
				post.Promoted += amount
				if err := tx.WithContext(ctx).Save(post).Error; err != nil {
					return fmt.Errorf("failed to update post promoted amount: %w", err)
				}
			}
		}
	}

	// Resolve account names to IDs (hive_payments.from_account/to_account are
	// INTEGER FKs to hive_accounts.id). 'null' and the sender must already be
	// registered (block_processor registers accounts observed in the block).
	accountRepo := db.NewAccountRepository(pay.repo.WithTx(tx))
	fromAccount, err := accountRepo.GetByName(ctx, from)
	if err != nil {
		return fmt.Errorf("failed to look up payment sender %s: %w", from, err)
	}
	if fromAccount == nil {
		return opValidationErr("payment sender not registered: %s", from)
	}
	toAccount, err := accountRepo.GetByName(ctx, to)
	if err != nil {
		return fmt.Errorf("failed to look up payment recipient %s: %w", to, err)
	}
	if toAccount == nil {
		return opValidationErr("payment recipient not registered: %s", to)
	}

	// Record payment
	payment := &models.Payment{
		BlockNum:      blockNum,
		TXIndex:       0, // TODO: Get actual transaction index
		PostID:        postID,
		FromAccountID: fromAccount.ID,
		ToAccountID:   toAccount.ID,
		Amount:        amount,
		Token:         token,
		Memo:          memo,
		CreatedAt:     blockDate,
	}

	if err := tx.WithContext(ctx).Create(payment).Error; err != nil {
		return fmt.Errorf("failed to create payment: %w", err)
	}

	pay.logger.Debug("Processed payment",
		zap.String("from", from),
		zap.String("to", to),
		zap.Float64("amount", amount),
		zap.Int64("post_id", postID))

	return nil
}

// parseAmount parses amount string like "10.000 SBD"
func parseAmount(amountStr string) (float64, error) {
	parts := strings.Fields(amountStr)
	if len(parts) == 0 {
		return 0, fmt.Errorf("invalid amount format")
	}

	amount, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, err
	}

	return amount, nil
}
