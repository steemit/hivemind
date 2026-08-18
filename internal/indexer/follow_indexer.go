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
)

// FollowIndexer handles follow relationship indexing
type FollowIndexer struct {
	repo          *db.Repository
	logger        *zap.Logger
	notifyIndexer *NotifyIndexer

	mu        sync.Mutex
	deltaFoll map[int64]int // followers delta: account_id -> pending change
	deltaFing map[int64]int // following delta: account_id -> pending change
}

// NewFollowIndexer creates a new follow indexer
func NewFollowIndexer(repo *db.Repository, logger *zap.Logger) *FollowIndexer {
	return &FollowIndexer{
		repo:          repo,
		logger:        logger,
		notifyIndexer: NewNotifyIndexer(repo),
		deltaFoll:     map[int64]int{},
		deltaFing:     map[int64]int{},
	}
}

// follow/unfollow accumulate count deltas for the next Flush (legacy
// Follow.follow/_apply_delta).
func (fi *FollowIndexer) follow(follower, following int64) {
	fi.applyDelta(follower, &fi.deltaFing, 1)
	fi.applyDelta(following, &fi.deltaFoll, 1)
}

func (fi *FollowIndexer) unfollow(follower, following int64) {
	fi.applyDelta(follower, &fi.deltaFing, -1)
	fi.applyDelta(following, &fi.deltaFoll, -1)
}

func (fi *FollowIndexer) applyDelta(account int64, m *map[int64]int, dir int) {
	fi.mu.Lock()
	(*m)[account] += dir
	fi.mu.Unlock()
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

	// Determine old state (0 when creating).
	oldState := int16(0)
	if err == gorm.ErrRecordNotFound {
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
		oldState = existing.State
		existing.State = state
		if err := tx.WithContext(ctx).Save(&existing).Error; err != nil {
			return fmt.Errorf("failed to update follow: %w", err)
		}
	}

	// Count deltas + follow notification, mirroring legacy follow_op:
	//  - ignore-only toggles (0<->2) change nothing
	//  - blog set -> follow; blog cleared -> unfollow
	//  - notify on 0 -> 1 transitions, scored by follower rank
	if state^oldState == 2 {
		// ignore-only jump
	} else if state == models.FollowStateBlog {
		fi.follow(followerAcc.ID, followingAcc.ID)
		if oldState == 0 {
			score := defaultScoreForAccount(ctx, fi.repo.DB(), followerAcc.ID)
			followerID := followerAcc.ID
			followingID := followingAcc.ID
			if err := fi.notifyIndexer.Write(ctx, models.NotifyTypeFollow, blockDate,
				&followerID, &followingID, nil, nil, nil, &score); err != nil {
				fi.logger.Warn("follow notif write failed", zap.Error(err))
			}
		}
	} else if oldState&models.FollowStateBlog == models.FollowStateBlog {
		fi.unfollow(followerAcc.ID, followingAcc.ID)
	}

	fi.logger.Debug("Processed follow operation",
		zap.String("follower", follower),
		zap.String("following", following),
		zap.Int16("state", state))

	return nil
}

// Flush applies pending follow/following count deltas in batched UPDATEs
// (legacy Follow.flush: group accounts by delta magnitude).
func (fi *FollowIndexer) Flush(ctx context.Context) error {
	fi.mu.Lock()
	foll := fi.deltaFoll
	fing := fi.deltaFing
	fi.deltaFoll = map[int64]int{}
	fi.deltaFing = map[int64]int{}
	fi.mu.Unlock()

	byMag := func(deltas map[int64]int) map[int][]int64 {
		out := map[int][]int64{}
		for id, d := range deltas {
			if d != 0 {
				out[d] = append(out[d], id)
			}
		}
		return out
	}

	gdb := fi.repo.DB()
	for col, deltas := range map[string]map[int64]int{"followers": foll, "following": fing} {
		for mag, ids := range byMag(deltas) {
			sql := fmt.Sprintf("UPDATE hive_accounts SET %s = %s + ? WHERE id IN ?", col, col)
			if err := gdb.WithContext(ctx).Exec(sql, mag, ids).Error; err != nil {
				// Re-queue the deltas on failure so they are not lost.
				fi.mu.Lock()
				for _, id := range ids {
					if col == "followers" {
						fi.deltaFoll[id] += mag
					} else {
						fi.deltaFing[id] += mag
					}
				}
				fi.mu.Unlock()
				return err
			}
		}
	}
	return nil
}

// PendingDeltasLen reports pending delta entries (diagnostics/tests).
func (fi *FollowIndexer) PendingDeltasLen() int {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	return len(fi.deltaFoll) + len(fi.deltaFing)
}

// ForceRecount recomputes followers/following for every account from
// hive_follows (legacy force_recount; run once after initial sync).
func (fi *FollowIndexer) ForceRecount(ctx context.Context) error {
	gdb := fi.repo.DB()
	statements := []string{
		`CREATE TEMP TABLE IF NOT EXISTS following_counts AS (
			SELECT id account_id, COUNT(state) num
			  FROM hive_accounts
			  LEFT JOIN hive_follows hf ON id = hf.follower AND state IN (1,3)
			 GROUP BY id)`,
		`CREATE TEMP TABLE IF NOT EXISTS follower_counts AS (
			SELECT id account_id, COUNT(state) num
			  FROM hive_accounts
			  LEFT JOIN hive_follows hf ON id = hf.following AND state IN (1,3)
			 GROUP BY id)`,
		`UPDATE hive_accounts SET followers = num FROM follower_counts
		  WHERE id = account_id AND followers != num`,
		`UPDATE hive_accounts SET following = num FROM following_counts
		  WHERE id = account_id AND following != num`,
	}
	for _, sql := range statements {
		if err := gdb.WithContext(ctx).Exec(sql).Error; err != nil {
			return fmt.Errorf("force recount: %w", err)
		}
	}
	return nil
}
