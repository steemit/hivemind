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

// followStateFromWhat computes the follow state bitfield positionally: only
// what[0] is considered for "blog" and only what[1] for "ignore", exactly
// mirroring legacy follow.py _validated_op (first()/second() checks).
// Notable consequences of the positional check: ["ignore"] alone yields
// state 0 (no ignore bit set), as does ["ignore","blog"].
func followStateFromWhat(what []interface{}) int16 {
	state := int16(0) // 0 = none
	if len(what) > 0 {
		if w, ok := what[0].(string); ok && w == "blog" {
			state += models.FollowStateBlog
		}
	}
	if len(what) > 1 {
		if w, ok := what[1].(string); ok && w == "ignore" {
			state += models.FollowStateIgnore
		}
	}
	return state
}

// ProcessFollow processes a follow operation, mirroring legacy
// hive/indexer/follow.py (Follow.follow_op + Follow._validated_op):
//
//   - the payload must carry string follower/following and a `what` list;
//     a missing or non-list `what` skips the op entirely -- it is NOT an
//     unfollow (legacy requires `'what' in op and isinstance(op['what'], list)`);
//   - state is computed positionally: only what[0] can set the blog bit and
//     only what[1] the ignore bit (legacy checks first()/second()), so
//     e.g. ["ignore"] alone yields state 0;
//   - self-follows and follower/signer mismatches are rejected;
//   - a computed state equal to the stored state is an idempotent no-op:
//     no row write and no count deltas (legacy
//     `if new_state == (old_state or 0): return`), which also means a
//     state-0 op for a pair with no existing row inserts nothing;
//   - count deltas and the follow notification are only tracked outside
//     initial sync (legacy gates on `not DbState.is_initial_sync()`).
func (fi *FollowIndexer) ProcessFollow(ctx context.Context, tx *gorm.DB, account string, opJSON map[string]interface{}, blockDate time.Time, isInitialSync bool) error {
	follower, followerIsStr := opJSON["follower"].(string)
	following, followingIsStr := opJSON["following"].(string)
	what, whatIsList := opJSON["what"].([]interface{})

	// Legacy _validated_op: `what` must be present and a list, and both
	// accounts present; anything else is skipped entirely. Notably a
	// missing `what` must NOT be treated as an unfollow.
	if !whatIsList || !followerIsStr || !followingIsStr {
		fi.logger.Debug("Skipping malformed follow op",
			zap.String("account", account))
		return nil
	}

	// Legacy rejects self-follows and follower/signer mismatches.
	if follower == following {
		return fmt.Errorf("invalid follow operation: cannot follow self")
	}
	if follower != account {
		return fmt.Errorf("follower account mismatch")
	}

	// Get account IDs. A missing account rejects the op (legacy
	// Accounts.exists check). Note: legacy registers same-block new
	// accounts before processing custom ops; Go's registration ordering is
	// tracked separately as audit P1-3 and intentionally not changed here.
	accountRepo := db.NewAccountRepository(fi.repo)
	followerAcc, err := accountRepo.GetByName(ctx, follower)
	if err != nil || followerAcc == nil {
		return fmt.Errorf("follower account not found: %s", follower)
	}

	followingAcc, err := accountRepo.GetByName(ctx, following)
	if err != nil || followingAcc == nil {
		return fmt.Errorf("following account not found: %s", following)
	}

	// Calculate state.
	state := followStateFromWhat(what)

	// Get existing follow relationship
	var existing models.Follow
	err = tx.WithContext(ctx).
		Where("follower = ? AND following = ?", followerAcc.ID, followingAcc.ID).
		First(&existing).Error

	// Determine old state (0 when creating).
	oldState := int16(0)
	if err == gorm.ErrRecordNotFound {
		// Idempotent short-circuit for pairs with no row: legacy never
		// inserts a state-0 row for a pair that was not followed.
		if state == 0 {
			return nil
		}
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
		// Idempotent short-circuit: a repeated identical follow neither
		// rewrites the row nor re-applies count deltas.
		if state == oldState {
			return nil
		}
		existing.State = state
		if err := tx.WithContext(ctx).Save(&existing).Error; err != nil {
			return fmt.Errorf("failed to update follow: %w", err)
		}
	}

	// Count deltas + follow notification, mirroring legacy follow_op:
	//  - skipped entirely during initial sync (counts come from the
	//    terminal ForceRecount instead)
	//  - ignore-only toggles (0<->2) change nothing
	//  - blog set -> follow; blog cleared -> unfollow
	//  - notify on 0 -> 1 transitions, scored by follower rank
	if !isInitialSync {
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
