package indexer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/internal/steem"
)

// AccountIndexer handles account indexing
type AccountIndexer struct {
	repo   *db.Repository
	steem  steem.Provider
	logger *zap.Logger
	mu     sync.Mutex
	dirty  map[string]bool // Dirty queue for accounts that need updates
}

// NewAccountIndexer creates a new account indexer. The steem provider is used
// by Flush (KR3) to pull fresh account data from steemd; it may be nil in tests
// that only exercise Register/MarkDirty.
func NewAccountIndexer(repo *db.Repository, logger *zap.Logger, steemProvider steem.Provider) *AccountIndexer {
	return &AccountIndexer{
		repo:   repo,
		steem:  steemProvider,
		logger: logger,
		dirty:  make(map[string]bool),
	}
}

// Register registers new accounts from blockchain operations
func (ai *AccountIndexer) Register(ctx context.Context, tx *gorm.DB, names []string, blockDate time.Time) error {
	if len(names) == 0 {
		return nil
	}

	accountRepo := db.NewAccountRepository(ai.repo)

	// Filter out accounts that already exist
	newNames := make([]string, 0)
	for _, name := range names {
		existing, err := accountRepo.GetByName(ctx, name)
		if err != nil {
			return fmt.Errorf("failed to check account existence: %w", err)
		}
		if existing == nil {
			newNames = append(newNames, name)
		}
	}

	if len(newNames) == 0 {
		return nil
	}

	// Create new accounts
	for _, name := range newNames {
		account := &models.Account{
			Name:       name,
			CreatedAt:  blockDate,
			Reputation: 25.0, // Default reputation
		}

		if err := tx.WithContext(ctx).Create(account).Error; err != nil {
			return fmt.Errorf("failed to create account %s: %w", name, err)
		}

		ai.logger.Debug("Registered new account", zap.String("name", name))
	}

	return nil
}

// MarkDirty marks an account as needing cache update
func (ai *AccountIndexer) MarkDirty(name string) {
	ai.mu.Lock()
	ai.dirty[name] = true
	ai.mu.Unlock()
}

// DirtySet marks a batch of accounts for update (legacy dirty_set).
func (ai *AccountIndexer) DirtySet(names []string) {
	ai.mu.Lock()
	for _, n := range names {
		ai.dirty[n] = true
	}
	ai.mu.Unlock()
}

// DirtyOldest flags the limit least-recently-cached accounts for update
// (legacy dirty_oldest; used by periodic maintenance sweeps).
func (ai *AccountIndexer) DirtyOldest(ctx context.Context, limit int) (int, error) {
	var names []string
	if err := ai.repo.DB().WithContext(ctx).
		Table("hive_accounts").
		Select("name").
		Order("cached_at").
		Limit(limit).
		Scan(&names).Error; err != nil {
		return 0, err
	}
	ai.DirtySet(names)
	return len(names), nil
}

// PendingDirtyLen reports the dirty queue size (diagnostics/tests).
func (ai *AccountIndexer) PendingDirtyLen() int {
	ai.mu.Lock()
	defer ai.mu.Unlock()
	return len(ai.dirty)
}

// Flush is the legacy-compatible wrapper around the real flush in
// account_update.go (to be called periodically by Sync).
func (ai *AccountIndexer) Flush(ctx context.Context) error {
	_, err := ai.FlushBatch(ctx, true)
	return err
}

// GetID retrieves account ID by name
func (ai *AccountIndexer) GetID(ctx context.Context, name string) (int64, error) {
	accountRepo := db.NewAccountRepository(ai.repo)
	account, err := accountRepo.GetByName(ctx, name)
	if err != nil {
		return 0, err
	}
	if account == nil {
		return 0, fmt.Errorf("account not found: %s", name)
	}
	return account.ID, nil
}
