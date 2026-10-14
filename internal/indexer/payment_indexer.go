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

// ProcessTransfer processes a transfer operation, mirroring legacy
// Payments._validated + op_transfer (hive/indexer/payments.py):
//
//   - only transfers TO 'null' are considered;
//   - only SBD payments count (legacy `if token != 'SBD': return`): a STEEM
//     burn is skipped entirely -- no payment row, no promoted bump;
//   - the memo must be exactly `@author/permlink` (non-empty, leading '@',
//     exactly one '/'); any other shape (plain text, `@a/b/c`, empty) skips
//     the whole record -- in particular a payment row must NOT be written
//     with post_id 0: hive_payments_fk3 would reject it, the error is
//     infrastructure-class, and the block would retry forever on perfectly
//     ordinary chain data (PR#406 audit P1);
//   - the memo author must be a registered account and the post must exist;
//     otherwise the record is skipped (legacy `Accounts.exists` /
//     `Posts.get_id` returning None);
//   - unregistered sender/recipient are data errors (opValidationError):
//     legacy asserts on Accounts.get_id and cannot proceed either;
//   - reads and writes ride the block transaction (P1-15): the post lookup
//     must see posts created earlier in the same block, and the payment row
//     / promoted update roll back with the block.
//
// Infrastructure failures propagate for block-level retry (P1-1).
func (pay *PaymentIndexer) ProcessTransfer(ctx context.Context, tx *gorm.DB, op map[string]interface{}, blockNum int64, blockDate time.Time) error {
	from, _ := op["from"].(string)
	to, _ := op["to"].(string)
	amountStr, _ := op["amount"].(string)
	memo, _ := op["memo"].(string)

	// Only process transfers to 'null' account (promoted posts)
	if to != "null" {
		return nil
	}

	// Parse amount (steemd asset string "<decimal> <SYMBOL>")
	amount, token, err := parseAmount(amountStr)
	if err != nil {
		return opValidationErr("failed to parse amount %q: %v", amountStr, err)
	}

	// Legacy gate: only SBD payments are promotions.
	if token != "SBD" {
		pay.logger.Debug("Skipping non-SBD transfer to null",
			zap.String("token", token),
			zap.Int64("block", blockNum))
		return nil
	}

	// Legacy _validate_url: memo must be exactly @author/permlink.
	if !validPromoMemo(memo) {
		pay.logger.Debug("Skipping transfer with invalid memo url",
			zap.String("memo", truncateJSON(memo)),
			zap.Int64("block", blockNum))
		return nil
	}
	author, permlink := splitPromoMemo(memo)

	accountRepo := db.NewAccountRepository(pay.repo.WithTx(tx))

	// Legacy Accounts.exists(author) gate: skip when the memo author is not
	// a registered account.
	authorAcc, err := accountRepo.GetByName(ctx, author)
	if err != nil {
		return fmt.Errorf("failed to look up memo author %s: %w", author, err)
	}
	if authorAcc == nil {
		return nil
	}

	// Legacy Posts.get_id: a promotion targeting a post this indexer has not
	// (yet) seen skips the record entirely (return None) -- never a payment
	// row with post_id 0.
	postRepo := db.NewPostRepository(pay.repo.WithTx(tx))
	post, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
	if err != nil {
		return fmt.Errorf("failed to get post %s/%s: %w", author, permlink, err)
	}
	if post == nil {
		pay.logger.Debug("Skipping payment for unknown post",
			zap.String("url", memo),
			zap.Int64("block", blockNum))
		return nil
	}

	// Resolve sender/recipient names to IDs (hive_payments.from_account/
	// to_account are INTEGER FKs to hive_accounts.id). The sender must be
	// registered; 'null' is a genesis account the schema seeds.
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

	// Record payment, then bump promoted (legacy op_transfer order).
	payment := &models.Payment{
		BlockNum:      blockNum,
		TXIndex:       0, // TODO: Get actual transaction index
		PostID:        post.ID,
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

	// Update post promoted amount
	post.Promoted += amount
	if err := tx.WithContext(ctx).Save(post).Error; err != nil {
		return fmt.Errorf("failed to update post promoted amount: %w", err)
	}

	pay.logger.Debug("Processed payment",
		zap.String("from", from),
		zap.String("to", to),
		zap.Float64("amount", amount),
		zap.Int64("post_id", post.ID))

	return nil
}

// validPromoMemo mirrors legacy Payments._validate_url: non-empty, leading
// '@', and exactly one '/'.
func validPromoMemo(memo string) bool {
	if memo == "" || memo[0] != '@' {
		return false
	}
	return strings.Count(memo, "/") == 1
}

// splitPromoMemo mirrors legacy Payments._split_url for an already-validated
// @author/permlink memo.
func splitPromoMemo(memo string) (author, permlink string) {
	parts := strings.SplitN(memo[1:], "/", 2)
	return parts[0], parts[1]
}

// parseAmount parses amount strings like "10.000 SBD", returning the decimal
// value and the token symbol (legacy parse_amount).
func parseAmount(amountStr string) (float64, string, error) {
	parts := strings.Fields(amountStr)
	if len(parts) != 2 {
		return 0, "", fmt.Errorf("invalid amount format")
	}

	amount, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return 0, "", err
	}

	return amount, parts[1], nil
}
