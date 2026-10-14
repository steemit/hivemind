package indexer

import (
	"context"
	"errors"
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
//     initial sync (legacy gates on `not DbState.is_initial_sync()`);
//   - account lookups and the follow-row read/write go through the block
//     transaction so same-block registrations are visible (P1-3/P1-15);
//   - validation failures of deterministic chain data return an
//     opValidationError (data error: skip the op, keep processing the
//     block); infrastructure failures (DB) propagate for block-level retry
//     (P1-1).
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
		return opValidationErr("invalid follow operation: cannot follow self")
	}
	if follower != account {
		return opValidationErr("follower account mismatch")
	}

	// Get account IDs through the block transaction: legacy registers
	// same-block new accounts before processing custom ops, and the
	// WithTx view makes those uncommitted rows visible here. A missing
	// account rejects the op (legacy Accounts.exists check -> skip).
	accountRepo := db.NewAccountRepository(fi.repo.WithTx(tx))
	followerAcc, err := accountRepo.GetByName(ctx, follower)
	if err != nil {
		return fmt.Errorf("failed to look up follower account %s: %w", follower, err)
	}
	if followerAcc == nil {
		return opValidationErr("follower account not found: %s", follower)
	}

	followingAcc, err := accountRepo.GetByName(ctx, following)
	if err != nil {
		return fmt.Errorf("failed to look up following account %s: %w", following, err)
	}
	if followingAcc == nil {
		return opValidationErr("following account not found: %s", following)
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
	if errors.Is(err, gorm.ErrRecordNotFound) {
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
	//  - notify on 0 -> 1 transitions, scored by follower rank; the write
	//    rides the block transaction (§2.10) and a failure propagates
	//    (P1-1: a follow row committed without its notification would be a
	//    partial application).
	if !isInitialSync {
		if state^oldState == 2 {
			// ignore-only jump
		} else if state == models.FollowStateBlog {
			fi.follow(followerAcc.ID, followingAcc.ID)
			if oldState == 0 {
				// The rank lookup deliberately stays on the pool view: the
				// rank map is a process-wide cache bound to one *gorm.DB,
				// and re-binding it per block transaction would rebuild the
				// full account ranking on every follow op. Ranks are a
				// stable snapshot used only for notification scoring.
				score := defaultScoreForAccount(ctx, fi.repo.DB(), followerAcc.ID)
				followerID := followerAcc.ID
				followingID := followingAcc.ID
				if err := fi.notifyIndexer.WithTx(tx).Write(ctx, models.NotifyTypeFollow, blockDate,
					&followerID, &followingID, nil, nil, nil, &score); err != nil {
					return fmt.Errorf("failed to write follow notification: %w", err)
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

// SnapshotDeltas deep-copies both pending delta maps and returns a restore
// closure that swaps them back in. The count deltas accumulate in memory
// OUTSIDE the block transaction, so a block rollback does not touch them:
// without a restore, a failed block leaves its deltas pending, the sync
// layer's retry of the same block re-accumulates them, and the next Flush
// applies them twice -- permanently inflating hive_accounts.followers/
// following (ForceRecount only runs at the end of initial sync and never
// self-heals live drift; PR#406 audit P1).
//
// The snapshot granularity is a single block, not a batch: deltas of blocks
// that already committed earlier in the batch live on in the snapshot and
// survive the restore. Replacing both maps wholesale is correct because the
// sync loop processes blocks single-threaded -- between SnapshotDeltas and
// restore, the only writers are the ops of the block being snapshotted.
func (fi *FollowIndexer) SnapshotDeltas() func() {
	fi.mu.Lock()
	foll := make(map[int64]int, len(fi.deltaFoll))
	for id, d := range fi.deltaFoll {
		foll[id] = d
	}
	fing := make(map[int64]int, len(fi.deltaFing))
	for id, d := range fi.deltaFing {
		fing[id] = d
	}
	fi.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			fi.mu.Lock()
			fi.deltaFoll = foll
			fi.deltaFing = fing
			fi.mu.Unlock()
		})
	}
}

// PendingDeltasLen reports pending delta entries (diagnostics/tests).
func (fi *FollowIndexer) PendingDeltasLen() int {
	fi.mu.Lock()
	defer fi.mu.Unlock()
	return len(fi.deltaFoll) + len(fi.deltaFing)
}

// ForceRecount recomputes followers/following for every account from
// hive_follows (legacy force_recount; run once after initial sync).
// Legacy issues the statements on a single session; here they run inside one
// transaction so the pool cannot spread the TEMP tables' lifecycle across
// different connections (a temp table only exists in the session that
// created it). ON COMMIT DROP also drops the tables at commit, so a later
// call reusing the same pooled connection repopulates them instead of
// reading stale rows.
func (fi *FollowIndexer) ForceRecount(ctx context.Context) error {
	gdb := fi.repo.DB()
	statements := []string{
		`CREATE TEMP TABLE following_counts ON COMMIT DROP AS (
			SELECT id account_id, COUNT(state) num
			  FROM hive_accounts
			  LEFT JOIN hive_follows hf ON id = hf.follower AND state IN (1,3)
			 GROUP BY id)`,
		`CREATE TEMP TABLE follower_counts ON COMMIT DROP AS (
			SELECT id account_id, COUNT(state) num
			  FROM hive_accounts
			  LEFT JOIN hive_follows hf ON id = hf.following AND state IN (1,3)
			 GROUP BY id)`,
		`UPDATE hive_accounts SET followers = num FROM follower_counts
		  WHERE id = account_id AND followers != num`,
		`UPDATE hive_accounts SET following = num FROM following_counts
		  WHERE id = account_id AND following != num`,
	}
	return gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, stmt := range statements {
			if err := tx.Exec(stmt).Error; err != nil {
				return fmt.Errorf("force recount: %w", err)
			}
		}
		return nil
	})
}
