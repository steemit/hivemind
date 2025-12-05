package indexer

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// AccountIndexer handles account indexing
type AccountIndexer struct {
	repo   *db.Repository
	logger *zap.Logger
	dirty  map[string]bool // Dirty queue for accounts that need updates
}

// NewAccountIndexer creates a new account indexer
func NewAccountIndexer(repo *db.Repository, logger *zap.Logger) *AccountIndexer {
	return &AccountIndexer{
		repo:   repo,
		logger: logger,
		dirty:   make(map[string]bool),
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
			Name:      name,
			CreatedAt: blockDate,
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
	ai.dirty[name] = true
}

// Flush processes dirty accounts (to be called periodically)
func (ai *AccountIndexer) Flush(ctx context.Context) error {
	if len(ai.dirty) == 0 {
		return nil
	}

	// TODO: Fetch accounts from steemd and update cache
	// For now, just clear the dirty queue
	ai.dirty = make(map[string]bool)
	
	return nil
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

