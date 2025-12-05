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

// FollowIndexer handles follow relationship indexing
type FollowIndexer struct {
	repo   *db.Repository
	logger *zap.Logger
}

// NewFollowIndexer creates a new follow indexer
func NewFollowIndexer(repo *db.Repository, logger *zap.Logger) *FollowIndexer {
	return &FollowIndexer{
		repo:   repo,
		logger: logger,
	}
}

// ProcessFollow processes a follow operation
func (fi *FollowIndexer) ProcessFollow(ctx context.Context, tx *gorm.DB, account string, opJSON map[string]interface{}, blockDate time.Time) error {
	follower, _ := opJSON["follower"].(string)
	following, _ := opJSON["following"].(string)
	what, _ := opJSON["what"].([]interface{})

	if follower == "" || following == "" {
		return fmt.Errorf("invalid follow operation: missing follower or following")
	}

	// Validate account matches operation signer
	if follower != account {
		return fmt.Errorf("follower account mismatch")
	}

	// Get account IDs
	accountRepo := db.NewAccountRepository(fi.repo)
	followerAcc, err := accountRepo.GetByName(ctx, follower)
	if err != nil || followerAcc == nil {
		return fmt.Errorf("follower account not found: %s", follower)
	}

	followingAcc, err := accountRepo.GetByName(ctx, following)
	if err != nil || followingAcc == nil {
		return fmt.Errorf("following account not found: %s", following)
	}

	// Calculate state
	state := int16(0) // 0 = none
	for _, w := range what {
		if wStr, ok := w.(string); ok {
			switch wStr {
			case "blog":
				state |= models.FollowStateBlog
			case "ignore":
				state |= models.FollowStateIgnore
			}
		}
	}

	// Get existing follow relationship
	var existing models.Follow
	err = tx.WithContext(ctx).
		Where("follower = ? AND following = ?", followerAcc.ID, followingAcc.ID).
		First(&existing).Error

	if err == gorm.ErrRecordNotFound {
		// Create new follow
		follow := &models.Follow{
			FollowerID:  followerAcc.ID,
			FollowingID: followingAcc.ID,
			State:       state,
			CreatedAt:   blockDate,
		}
		if err := tx.WithContext(ctx).Create(follow).Error; err != nil {
			return fmt.Errorf("failed to create follow: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to check existing follow: %w", err)
	} else {
		// Update existing follow
		oldState := existing.State
		existing.State = state
		if err := tx.WithContext(ctx).Save(&existing).Error; err != nil {
			return fmt.Errorf("failed to update follow: %w", err)
		}

		// Track count deltas
		// TODO: Implement count delta tracking
		_ = oldState
	}

	fi.logger.Debug("Processed follow operation",
		zap.String("follower", follower),
		zap.String("following", following),
		zap.Int16("state", state))

	return nil
}

// Flush flushes follow count deltas (to be called periodically)
func (fi *FollowIndexer) Flush(ctx context.Context) error {
	// TODO: Implement follow count delta flushing
	return nil
}

