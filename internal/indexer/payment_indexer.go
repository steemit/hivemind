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

// ProcessTransfer processes a transfer operation
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
		return fmt.Errorf("failed to parse amount: %w", err)
	}

	// Extract token type (should be SBD)
	token := "SBD"
	if strings.Contains(amountStr, "STEEM") {
		token = "STEEM"
	}

	// Parse memo to get post (format: @author/permlink)
	var postID int64
	if memo != "" && strings.HasPrefix(memo, "@") {
		parts := strings.Split(memo[1:], "/")
		if len(parts) == 2 {
			author := parts[0]
			permlink := parts[1]
			
			postRepo := db.NewPostRepository(pay.repo)
			post, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
			if err == nil && post != nil {
				postID = post.ID
				
				// Update post promoted amount
				post.Promoted += amount
				if err := tx.WithContext(ctx).Save(post).Error; err != nil {
					pay.logger.Warn("Failed to update post promoted amount", zap.Error(err))
				}
			}
		}
	}

	// Record payment
	payment := &models.Payment{
		BlockNum: blockNum,
		TXIndex:  0, // TODO: Get actual transaction index
		From:     from,
		To:       to,
		Amount:   amount,
		Token:    token,
		Memo:     memo,
		PostID:   postID,
		CreatedAt: blockDate,
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

