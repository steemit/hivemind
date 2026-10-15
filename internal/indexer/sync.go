package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// Sync manages the blockchain synchronization process
type Sync struct {
	config         *config.Config
	db             *db.DB
	steem          steem.Provider
	blockProcessor *BlockProcessor
	stateRepo      *db.StateRepository
	cacheSync      *CacheSync
	logger         *zap.Logger
}

// NewSync creates a new sync manager. The steemClient is accepted as the
// Provider interface so tests can inject a mock; the concrete *steem.Client
// satisfies it (see internal/steem/provider.go).
func NewSync(cfg *config.Config, database *db.DB, steemClient steem.Provider) (*Sync, error) {
	repo := db.NewRepository(database.DB)
	blockProcessor := NewBlockProcessor(database, repo, steemClient)

	return &Sync{
		config:         cfg,
		db:             database,
		steem:          steemClient,
		blockProcessor: blockProcessor,
		stateRepo:      db.NewStateRepository(repo),
		logger:         logging.GetLogger().With(zap.String("component", "indexer")),
	}, nil
}

// Run starts the sync process
func (s *Sync) Run(ctx context.Context) error {
	s.logger.Info("Starting indexer sync")

	// Determine the sync phase from the persisted hive_state markers
	// (falling back to the legacy feed_cache derivation on databases that
	// predate them), materializing the derived value so the flag is
	// authoritative from this boot on.
	//
	// Cancellation mapping (PR#407 registration for #408): a SIGTERM during
	// this startup flag resolution used to surface as a plain error and the
	// process Fatal-exited with exit 1; the phase never began, so the clean
	// exit (nil, restart resumes) is the correct mapping.
	flags, err := s.resolveInitialSyncState(ctx)
	if err = runPhase(err); err != nil {
		if errors.Is(err, errPhaseCancelled) {
			s.logger.Info("Shutdown during startup state resolution; exiting cleanly (restart resumes)")
			return nil
		}
		return fmt.Errorf("failed to check initial sync status: %w", err)
	}

	if flags.IsInitialSync.Bool {
		s.logger.Info("Initial sync detected, starting initial sync process")
		if err := runPhase(s.initialSync(ctx, flags)); err != nil {
			if errors.Is(err, errPhaseCancelled) {
				// A shutdown arrived mid-phase: the phase aborted cleanly
				// (nothing half-committed) and is resumable from the
				// persisted markers on the next boot. Returning nil makes
				// the process exit instead of "completing" a sync that did
				// not finish and entering live mode with wrong data.
				s.logger.Info("Initial sync cancelled before completion; exiting (restart resumes from persisted markers)")
				return nil
			}
			return fmt.Errorf("initial sync failed: %w", err)
		}
		if s.testMaxBlock() > 0 {
			// TEST_MAX_BLOCK stop point (legacy sync.py:86/154): the tail
			// maintenance has completed, but the persisted initial-sync
			// flag deliberately stays TRUE -- a subsequent continuation
			// run (e.g. 20M -> 30M) must keep fast-sync semantics instead
			// of dropping into live-mode processing mid-history.
			head := s.headBlockNum(ctx)
			s.logger.Info("TEST_MAX_BLOCK stop point reached; exiting cleanly (is_initial_sync stays true)",
				zap.Int64("head_block", head),
				zap.Int64("test_max_block", s.testMaxBlock()))
			return nil
		}
		s.logger.Info("Initial sync completed")
	} else {
		s.logger.Info("Database already initialized; running light startup recovery")
		if err := runPhase(s.recoverFromRestart(ctx, flags)); err != nil {
			if errors.Is(err, errPhaseCancelled) {
				s.logger.Info("Startup recovery cancelled before completion; exiting (restart resumes)")
				return nil
			}
			return fmt.Errorf("startup recovery failed: %w", err)
		}
		if s.testMaxBlock() > 0 {
			// Legacy parity (sync.py:86-88): with test_max_block set, an
			// already-initialized database catches up to the cap in normal
			// semantics and then returns instead of entering the runloop.
			// Cancellation mapping (PR#407 registration for #408): a SIGTERM
			// during the cap path's fail-closed head read used to Fatal-exit
			// the process; nothing was synced, so the clean exit is correct.
			if err := runPhase(s.syncToCapAndExit(ctx)); err != nil {
				if errors.Is(err, errPhaseCancelled) {
					s.logger.Info("Shutdown during TEST_MAX_BLOCK catch-up; exiting cleanly (restart resumes)")
					return nil
				}
				return err
			}
			return nil
		}
	}

	// Prune hive_posts_cache_temp outside the 90-day hot window (60s tick).
	// The pruner is gated: while the indexer is catching up farther than
	// cache_sync_catchup_blocks behind the chain head it pauses (legacy only
	// runs CacheSync from live mode, sync.py:236-237).
	s.cacheSync = NewCacheSync(s.db.DB)
	s.cacheSync.SetCatchupThreshold(s.catchupGateBlocks())
	go s.cacheSync.Run(ctx)

	// Optional periodic cache-consistency audits (0 = disabled; legacy runs
	// these manually).
	if interval := s.config.Indexer.JobsInterval; interval > 0 {
		go s.runCacheJobsLoop(ctx, interval)
	}

	// Determine sync strategy based on configuration
	// For now, we'll use Strategy B (irreversible blocks only)
	// This is simpler and more reliable

	// Start sync loop
	if err := s.syncIrreversible(ctx); err != nil {
		// A cancelled context is the normal SIGTERM shutdown path, not a
		// failure: the shutdown handler waits for this return after the
		// in-flight batch completed and flushed.
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// errPhaseCancelled is runPhase's distinguishable return for "the startup
// phase aborted because the run context was cancelled (SIGTERM)". Mapping
// the cancellation to plain nil (the previous behavior) let Run continue as
// if the phase had SUCCEEDED -- logging "Initial sync completed", starting
// the background goroutines and entering live-mode processing on a database
// whose tail maintenance never finished. Run detects the sentinel and
// returns nil immediately so the process exits and the next boot resumes
// the phase (PR#407 review).
var errPhaseCancelled = errors.New("startup phase cancelled before completion")

// runPhase executes a startup phase (initial sync / recovery), mapping a
// context cancellation to the errPhaseCancelled sentinel: a shutdown
// arriving mid-phase aborts the (idempotent, resumable) work -- in-flight
// block batches have already completed and flushed inside syncBlocks -- and
// the next boot resumes the phase from hive_blocks head and the persisted
// markers. Callers must check for the sentinel and treat it as "exit for
// restart", never as success.
func runPhase(err error) error {
	if errors.Is(err, context.Canceled) {
		return errPhaseCancelled
	}
	return err
}

// runCacheJobsLoop periodically runs the cache audit jobs.
func (s *Sync) runCacheJobsLoop(ctx context.Context, intervalSec int) {
	interval := time.Duration(intervalSec) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := RunCacheJobs(ctx, s.db.DB, s.blockProcessor.CachedPost(), s.steem); err != nil {
				s.logger.Warn("cache jobs run failed", zap.Error(err))
			}
		}
	}
}

// resolveInitialSyncState decides whether this boot resumes the initial
// sync. The persisted hive_state.is_initial_sync marker (migration 0003) is
// authoritative; when it has never been written (NULL -- a legacy-provisioned
// database or one migrated before 0003) the legacy derivation from
// hive_feed_cache emptiness (db_state.py::_is_feed_cache_empty) is used and
// then materialized, so the fragile table-emptiness heuristic is consulted
// at most once per database.
//
// For a database derived as "not initial sync", recount_done is materialized
// as TRUE as well: a database whose feed cache is populated has completed
// the legacy initial sync (which ends with force_recount), and re-running a
// full recount against tens of millions of accounts on first boot would be
// catastrophic. HIVE_FORCE_FOLLOW_RECOUNT remains the manual override.
func (s *Sync) resolveInitialSyncState(ctx context.Context) (*db.SyncFlags, error) {
	flags, err := s.stateRepo.GetSyncFlags(ctx)
	if err != nil {
		return nil, err
	}
	if flags.IsInitialSync.Valid {
		s.logger.Info("Initial sync state read from persisted marker",
			zap.Bool("is_initial_sync", flags.IsInitialSync.Bool),
			zap.Bool("recount_done", flags.RecountDone.Bool))
		// Operator warning (PR#407 review): during a genuine initial sync
		// the feed cache stays EMPTY until the tail trio's Rebuild -- a
		// database marked "initial sync in progress" with a POPULATED feed
		// cache and no completed recount either healed from an interrupted
		// tail on this very boot (recount_done unset -> the trio re-runs
		// below) or is corrupted (see the legacy-migration runbook in
		// docs/indexer-flow.md). The TEST_MAX_BLOCK stop point is the
		// sanctioned non-empty state and is excluded via recount_done.
		recountComplete := flags.RecountDone.Valid && flags.RecountDone.Bool
		if flags.IsInitialSync.Bool && !recountComplete {
			empty, err := s.feedCacheEmpty(ctx)
			if err != nil {
				return nil, err
			}
			if !empty {
				s.logger.Warn("feed cache is populated while initial sync is in progress and recount_done is unset: " +
					"interrupted tail (this boot re-runs the trio and heals) or corrupted state -- verify counts after startup")
			}
		}
		return flags, nil
	}

	// Fallback: legacy feed_cache derivation. EXISTS instead of COUNT: the
	// answer is only "empty or not", and a full-table count on a populated
	// 30M-row feed cache wastes seconds for a boolean.
	empty, err := s.feedCacheEmpty(ctx)
	if err != nil {
		return nil, err
	}
	derived := db.SyncFlags{
		IsInitialSync: sql.NullBool{Bool: empty, Valid: true},
		RecountDone:   sql.NullBool{Bool: !empty, Valid: true},
	}
	if err := s.stateRepo.UpdateSyncFlags(ctx, derived.IsInitialSync, derived.RecountDone); err != nil {
		return nil, err
	}
	s.logger.Info("Initial sync state derived from feed cache (legacy heuristic) and persisted",
		zap.Bool("is_initial_sync", derived.IsInitialSync.Bool),
		zap.Bool("feed_cache_empty", empty))
	return &derived, nil
}

// feedCacheEmpty reports whether hive_feed_cache has no rows (EXISTS probe,
// no full scan).
func (s *Sync) feedCacheEmpty(ctx context.Context) (bool, error) {
	var empty bool
	if err := s.db.DB.WithContext(ctx).
		Raw("SELECT NOT EXISTS (SELECT 1 FROM hive_feed_cache)").
		Scan(&empty).Error; err != nil {
		return false, err
	}
	return empty, nil
}

// headBlockNum reads the current head block from hive_blocks for
// DIAGNOSTICS ONLY (logging). It swallows errors and returns 0 -- a value
// that must never feed a control-flow decision (PR#407 review: the
// tail-skip check once consumed this 0, so a transient GetHead failure
// looked like "no blocks synced" and wrongly skipped the tail trio; the
// same class of bug could replay blocks from 1). Control-flow call sites
// read BlockRepository.GetHead directly and propagate the error.
func (s *Sync) headBlockNum(ctx context.Context) int64 {
	head, err := db.NewBlockRepository(s.blockProcessor.repo).GetHead(ctx)
	if err != nil || head == nil {
		return 0
	}
	return head.Num
}

// testMaxBlock returns the configured TEST_MAX_BLOCK stop point (0 = unset).
func (s *Sync) testMaxBlock() int64 {
	if s.config == nil {
		return 0
	}
	return int64(s.config.Indexer.TestMaxBlock)
}

// catchupGateBlocks returns the CacheSync gating threshold in blocks.
func (s *Sync) catchupGateBlocks() int64 {
	if s.config == nil {
		return CacheSyncCatchupBlocks
	}
	return int64(s.config.Indexer.CacheSyncCatchupBlocks)
}

// initialSync performs the initial sync routine: fast sync to the target
// block (last irreversible, or TEST_MAX_BLOCK when set), then the tail
// maintenance trio -- RecoverMissingPosts, FeedCache.Rebuild,
// Follow.ForceRecount -- in the legacy order (sync.py:117-119: rebuild
// BEFORE recount).
//
// Recovery ordering (PR#402 audit registration): the is_initial_sync flag is
// only flipped AFTER the whole trio succeeds, and recount_done is written
// immediately after ForceRecount. Legacy derives the phase from
// hive_feed_cache emptiness, which flips as soon as Rebuild populates the
// table -- a crash before recount then left the counts at 0 forever with no
// retry path. With the persisted markers, any interruption before the flag
// flip makes the next boot re-enter this routine (0 blocks to sync) and
// re-run the missing tail steps; both steps are idempotent.
//
// TEST_MAX_BLOCK: when set, the trio runs at the stop point but the
// is_initial_sync flag is NOT flipped (see Run).
func (s *Sync) initialSync(ctx context.Context, flags *db.SyncFlags) error {
	s.logger.Info("Starting initial fast sync")

	blockRepo := db.NewBlockRepository(s.blockProcessor.repo)
	headBlock, err := blockRepo.GetHead(ctx)
	if err != nil {
		return fmt.Errorf("failed to get head block: %w", err)
	}
	headBefore := int64(0)
	if headBlock != nil {
		headBefore = headBlock.Num
	}

	irreversible, err := s.steem.LastIrreversible(ctx)
	if err != nil {
		return fmt.Errorf("failed to get last irreversible block: %w", err)
	}
	ubound := irreversible
	if stop := s.testMaxBlock(); stop > 0 && stop < ubound {
		ubound = stop
	}

	if headBefore < ubound {
		s.logger.Info("Syncing blocks for initial sync",
			zap.Int64("from", headBefore+1),
			zap.Int64("to", ubound))

		// Invalidate the tail marker BEFORE the first new block commits
		// (PR#407 review, P0): recount_done claims "the tail trio completed
		// at the current head". Syncing past that head makes the claim
		// stale, so it is cleared up front -- a crash anywhere between the
		// first new block and the trio's completion then leaves the marker
		// claiming nothing, and the restart re-runs the trio instead of
		// skipping it forever (the 20M-stop -> 30M continuation interrupted
		// mid-trio scenario). Clearing at trio ENTRY alone would leave the
		// blocks-synced -> trio-entry window unprotected.
		if err := s.stateRepo.ClearRecountDone(ctx); err != nil {
			return fmt.Errorf("failed to invalidate recount_done before syncing: %w", err)
		}
		flags.RecountDone = sql.NullBool{}

		// Bounded-retry variant with poison-block observability: keeps the
		// initial-sync fail-fast semantic (the process exits after
		// poisonBlockThreshold consecutive failures of the same block)
		// while riding out transient RPC/DB errors during a multi-week
		// 0->30M sync.
		if err := s.syncBlocksBoundedRetry(ctx, ubound, headBefore); err != nil {
			return fmt.Errorf("failed to sync blocks: %w", err)
		}
	}

	// The tail trio only needs to re-run when blocks were synced since it
	// last completed, or when it never completed (recount_done unset -- the
	// interrupted-tail case). Skipping it keeps a restart at an already
	// processed stop point (e.g. restart with the same TEST_MAX_BLOCK) a
	// short path instead of re-running multi-day maintenance in place.
	// Fail-closed head read (PR#407 review): headBlockNum swallows errors
	// and returns 0, which here would look like "head went backwards" and
	// wrongly trigger the skip. Read GetHead directly; a transient failure
	// returns an error and the (idempotent) restart tries again.
	headAfterBlock, err := blockRepo.GetHead(ctx)
	if err != nil {
		return fmt.Errorf("failed to get head block after sync: %w", err)
	}
	headAfter := int64(0)
	if headAfterBlock != nil {
		headAfter = headAfterBlock.Num
	}
	tailAlreadyDone := flags.RecountDone.Valid && flags.RecountDone.Bool && headAfter <= headBefore
	if tailAlreadyDone {
		s.logger.Info("Initial-sync tail maintenance already completed at this head; skipping",
			zap.Int64("head_block", headAfter))
	} else {
		// Recover missing posts (post cache recovery): fill hive_posts_cache
		// entries for any posts indexed beyond the cache cursor.
		s.logger.Info("Recovering missing posts")
		if err := s.blockProcessor.CachedPost().RecoverMissingPosts(ctx); err != nil {
			return fmt.Errorf("failed to recover missing posts: %w", err)
		}

		// Rebuild feed cache
		s.logger.Info("Rebuilding feed cache")
		feedCacheRepo := db.NewFeedCacheRepository(s.blockProcessor.repo)
		if err := feedCacheRepo.Rebuild(ctx, true); err != nil {
			return fmt.Errorf("failed to rebuild feed cache: %w", err)
		}

		// Force follow recount: recompute followers/following from hive_follows.
		// Last step of the trio: only after it succeeds is the tail marked
		// complete (recount_done), which is what gives a crash between
		// rebuild and recount its recovery path on restart.
		s.logger.Info("Recounting follows")
		if err := s.blockProcessor.Follows().ForceRecount(ctx); err != nil {
			return fmt.Errorf("failed to recount follows: %w", err)
		}
		if err := s.stateRepo.UpdateSyncFlags(ctx,
			sql.NullBool{}, sql.NullBool{Bool: true, Valid: true}); err != nil {
			return fmt.Errorf("failed to persist recount_done marker: %w", err)
		}
		flags.RecountDone = sql.NullBool{Bool: true, Valid: true}
	}

	if s.testMaxBlock() == 0 {
		// Initial sync complete: flip the persisted marker. Written last so
		// every failure path above restarts into this routine rather than
		// the normal loop.
		if err := s.stateRepo.UpdateSyncFlags(ctx,
			sql.NullBool{Bool: false, Valid: true}, sql.NullBool{}); err != nil {
			return fmt.Errorf("failed to persist initial-sync completion: %w", err)
		}
		flags.IsInitialSync = sql.NullBool{Bool: false, Valid: true}
	}

	s.logger.Info("Initial sync completed successfully")
	return nil
}

// recoverFromRestart performs the light recovery an already-initialized
// database runs at boot (legacy sync.py:70-79): verify the cache against
// hive_posts and heal any gap (RecoverMissingPosts is incremental and a
// no-op when head and cache agree), plus the recount recovery markers.
func (s *Sync) recoverFromRestart(ctx context.Context, flags *db.SyncFlags) error {
	// Recover missing posts: short-circuits when the cache cursor already
	// matches hive_posts (gap <= 0), making this an idempotent short path.
	s.logger.Info("Recovering missing posts (startup recovery)")
	if err := s.blockProcessor.CachedPost().RecoverMissingPosts(ctx); err != nil {
		return fmt.Errorf("failed to recover missing posts: %w", err)
	}

	// Recount marker check (PR#402 audit registration): if the initial-sync
	// tail never completed its ForceRecount, re-run it here so follow
	// counts cannot stay at 0 forever.
	if !flags.RecountDone.Valid || !flags.RecountDone.Bool {
		s.logger.Warn("recount_done marker unset; re-running follow recount (interrupted initial-sync tail?)")
		if err := s.blockProcessor.Follows().ForceRecount(ctx); err != nil {
			return fmt.Errorf("failed to recount follows: %w", err)
		}
		if err := s.stateRepo.UpdateSyncFlags(ctx,
			sql.NullBool{}, sql.NullBool{Bool: true, Valid: true}); err != nil {
			return fmt.Errorf("failed to persist recount_done marker: %w", err)
		}
		flags.RecountDone = sql.NullBool{Bool: true, Valid: true}
	}

	// HIVE_FORCE_FOLLOW_RECOUNT (legacy conf force_follow_recount,
	// sync.py:76-79): manual full recount of all follows at startup.
	if s.config != nil && s.config.Indexer.ForceFollowRecount {
		s.logger.Info("Force recount of all follows (HIVE_FORCE_FOLLOW_RECOUNT)")
		if err := s.blockProcessor.Follows().ForceRecount(ctx); err != nil {
			return fmt.Errorf("failed to recount follows: %w", err)
		}
	}
	return nil
}

// syncToCapAndExit catches an already-initialized database up to the
// TEST_MAX_BLOCK cap in normal semantics and returns (legacy sync.py:88:
// `if test_max_block: return self.from_steemd()`).
func (s *Sync) syncToCapAndExit(ctx context.Context) error {
	stopAt := s.testMaxBlock()
	// Fail-closed head read (PR#407 review): the old headBlockNum() helper
	// swallows errors and returns 0, which here would look like "0 blocks
	// synced" and replay the chain from block 1. Propagate the error; the
	// restart is idempotent.
	headBlock, err := db.NewBlockRepository(s.blockProcessor.repo).GetHead(ctx)
	if err != nil {
		return fmt.Errorf("failed to get head block: %w", err)
	}
	head := int64(0)
	if headBlock != nil {
		head = headBlock.Num
	}
	if head >= stopAt {
		s.logger.Info("TEST_MAX_BLOCK already reached; exiting cleanly",
			zap.Int64("head_block", head), zap.Int64("test_max_block", stopAt))
		return nil
	}
	s.logger.Info("Syncing to TEST_MAX_BLOCK before exit",
		zap.Int64("from", head+1), zap.Int64("to", stopAt))
	if err := s.syncBlocks(ctx, head+1, stopAt, false); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return fmt.Errorf("failed to sync blocks to test cap: %w", err)
	}
	s.logger.Info("TEST_MAX_BLOCK reached; exiting cleanly",
		zap.Int64("head_block", s.headBlockNum(ctx)), zap.Int64("test_max_block", stopAt))
	return nil
}

// poisonBlockThreshold is the number of consecutive failed sync attempts of
// the SAME block after which the sync loops escalate their logs to a marked
// `poison block` Error (block number + last error). Purely observability:
// the steady-state loop keeps retrying, the initial-sync path gives up
// (fail-fast). Halting or skipping is a separate decision tracked for the
// operations runbook (PR#406 registration); the minimal #405 scope is the
// fetch/process distinction and the block-number context in every error.
const poisonBlockThreshold = 10

// Error sentinels wrapping sync failures so callers (and logs) can
// distinguish a failed block FETCH from a failed block PROCESS attempt --
// the poison-block triage axis registered by PR#404 for #405.
var (
	errFetchBlocks  = errors.New("failed to fetch blocks")
	errProcessBlock = errors.New("failed to process block")
)

// blockNumberOf extracts the block number from a raw block map; 0 when the
// field is missing or malformed (the number is used for error context and
// poison logging, never for control flow).
func blockNumberOf(block map[string]interface{}) int64 {
	if v, ok := block["block_num"].(float64); ok {
		return int64(v)
	}
	return 0
}

// processBlockError marks a failed block PROCESS attempt inside a tx batch
// and carries the failing block's number out to the sync loops. With
// batch-level rollback the first unprocessed block is the batch START (the
// whole batch rolled back), so the poison-block observability must take the
// actually-failing block from the error instead of head+1 -- which did
// identify the failing block back when each block committed individually.
type processBlockError struct {
	block int64
	err   error
}

func (e *processBlockError) Error() string {
	return fmt.Sprintf("failed to process block %d: %v", e.block, e.err)
}

// Unwrap exposes both the errProcessBlock sentinel (failure classification)
// and the underlying cause (context cancellation, driver errors) so existing
// errors.Is checks keep working.
func (e *processBlockError) Unwrap() error {
	return errors.Join(errProcessBlock, e.err)
}

// poisonTargetBlock returns the block a failed syncBlocks attempt will retry:
// the actually-failing block for process failures (see processBlockError),
// falling back to the first unprocessed block of the attempt (fetch failures
// and the legacy convention).
func poisonTargetBlock(head int64, err error) int64 {
	var pbe *processBlockError
	if errors.As(err, &pbe) {
		return pbe.block
	}
	return head + 1
}

// syncFailureKind classifies a syncBlocks error for poison logging.
func syncFailureKind(err error) string {
	if errors.Is(err, errFetchBlocks) {
		return "fetch"
	}
	return "process"
}

// syncIrreversible implements Strategy B: sync only to irreversible blocks
func (s *Sync) syncIrreversible(ctx context.Context) error {
	s.logger.Info("Using fork strategy: irreversible blocks only")

	blockRepo := db.NewBlockRepository(s.blockProcessor.repo)
	syncInterval := s.config.Indexer.SyncInterval
	if syncInterval == 0 {
		syncInterval = 3 // Default 3 seconds
	}

	// Poison-block observability: the first block of the next unprocessed
	// range (currentHead+1) identifies what a failed attempt will retry.
	// After poisonBlockThreshold consecutive failures of that same block,
	// every failure logs with the `poison block` marker so a permanently
	// broken block is visible instead of silently spinning. A success (or
	// progress to another block) resets the counters.
	var poisonBlock int64
	var poisonFails int

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Get current head from database
			headBlock, err := blockRepo.GetHead(ctx)
			if err != nil {
				s.logger.Error("Failed to get head block from database", zap.Error(err))
				// Retry after a short delay
				s.wait(ctx, syncInterval)
				continue
			}

			currentHead := int64(0)
			if headBlock != nil {
				currentHead = headBlock.Num
			}

			// Get last irreversible block from steemd
			irreversible, err := s.steem.LastIrreversible(ctx)
			if err != nil {
				s.logger.Error("Failed to get last irreversible block", zap.Error(err))
				// Retry after a short delay
				s.wait(ctx, syncInterval)
				continue
			}

			// Feed the CacheSync gate: prune pauses while catching up.
			if s.cacheSync != nil {
				s.cacheSync.SetBehind(irreversible - currentHead)
			}

			// TEST_MAX_BLOCK (§2.6): stop cleanly once the cap is reached
			// instead of entering the steady-state runloop.
			//
			// NOTE: with the current Run() flow this branch is unreachable
			// -- both the initial-sync and the already-initialized paths
			// return at the stop point BEFORE starting this loop. It stays
			// as defense-in-depth so a future Run() refactor that lets a
			// capped indexer reach the runloop still stops at the cap
			// instead of processing beyond it.
			ubound := irreversible
			if stop := s.testMaxBlock(); stop > 0 {
				if currentHead >= stop {
					s.logger.Info("TEST_MAX_BLOCK reached; exiting cleanly",
						zap.Int64("head_block", currentHead),
						zap.Int64("test_max_block", stop))
					return nil
				}
				if stop < ubound {
					ubound = stop
				}
			}

			// Sync blocks from currentHead+1 to irreversible
			if currentHead < ubound {
				s.logger.Info("Syncing blocks",
					zap.Int64("current_head", currentHead),
					zap.Int64("target", ubound),
					zap.Int64("blocks_to_sync", ubound-currentHead))

				if err := s.syncBlocks(ctx, currentHead+1, ubound, false); err != nil {
					if ctx.Err() != nil {
						// Shutdown requested mid-sync: not a poison block.
						return ctx.Err()
					}
					// The retry target: with batch-level rollback the whole
					// tx batch rolled back, so head+1 is the batch START --
					// for process failures the actually-failing block comes
					// from the error itself (see processBlockError).
					retryBlock := poisonTargetBlock(currentHead, err)
					if retryBlock == poisonBlock {
						poisonFails++
					} else {
						poisonBlock = retryBlock
						poisonFails = 1
					}
					if poisonFails >= poisonBlockThreshold {
						// Escalated, marked error -- emitted on EVERY failure
						// at/after the threshold so the spin never goes quiet.
						s.logger.Error("poison block: repeated failures on the same block; sync is stalled and retrying",
							zap.Int64("block", poisonBlock),
							zap.Int("consecutive_failures", poisonFails),
							zap.Int("threshold", poisonBlockThreshold),
							zap.String("failure_kind", syncFailureKind(err)),
							zap.Error(err))
					} else {
						s.logger.Error("Failed to sync blocks",
							zap.Int64("block", poisonBlock),
							zap.Int("attempt", poisonFails),
							zap.String("failure_kind", syncFailureKind(err)),
							zap.Error(err))
					}
					// Retry after a short delay
					s.wait(ctx, syncInterval)
					continue
				}

				poisonBlock, poisonFails = 0, 0

				s.logger.Info("Successfully synced blocks",
					zap.Int64("from", currentHead+1),
					zap.Int64("to", ubound))
			} else {
				// Already synced, wait before next check
				s.logger.Debug("Already synced to irreversible block",
					zap.Int64("current_head", currentHead),
					zap.Int64("irreversible", irreversible))
			}

			// Payout backlog sweep (P1-10): legacy takes care of the payout
			// backlog after every catch-up round and, in live mode, before
			// each block's flush (sync.py:98 and sync.py:208). This loop's
			// iteration -- one catch-up step followed by the sync-interval
			// wait -- covers both cadences. Failures are logged and retried
			// on the next iteration; the sweep is idempotent.
			if err := s.payoutSweep(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				s.logger.Error("Payout sweep failed", zap.Error(err))
			}

			// Wait before next check
			s.wait(ctx, syncInterval)
		}
	}
}

// payoutSweep marks posts whose payout_at has passed without the is_paidout
// flag, marks their authors dirty (legacy Accounts.dirty_set inside
// dirty_paidouts), and flushes the dirty queue (legacy sync.py:97-99:
// `CachedPost.dirty_paidouts(Blocks.head_date()); CachedPost.flush(trx=True)`,
// and the per-block live variant at sync.py:208-209). The scan date is the
// local head block's date -- the indexer never sweeps for payouts beyond
// what it has actually indexed.
func (s *Sync) payoutSweep(ctx context.Context) error {
	headBlock, err := db.NewBlockRepository(s.blockProcessor.repo).GetHead(ctx)
	if err != nil {
		return fmt.Errorf("failed to get head block for payout sweep: %w", err)
	}
	if headBlock == nil {
		// No blocks indexed yet: nothing can have paid out.
		return nil
	}
	n, err := s.blockProcessor.CachedPost().DirtyPaidouts(ctx, headBlock.CreatedAt, s.blockProcessor.Accounts().MarkDirty)
	if err != nil {
		return fmt.Errorf("failed to mark paid-out posts: %w", err)
	}
	if n > 200 {
		s.logger.Info("Payout sweep", zap.Int("posts", n))
	} else if n > 0 {
		s.logger.Debug("Payout sweep", zap.Int("posts", n))
	}
	if _, err := s.blockProcessor.CachedPost().Flush(ctx, true); err != nil {
		return fmt.Errorf("failed to flush payout sweep: %w", err)
	}
	return nil
}

// syncBlocksBoundedRetry drives the initial-sync fast sync with the
// poison-block observability the steady-state loop has, but bounded: after
// poisonBlockThreshold consecutive failures of the same block it gives up
// (the historical fail-fast semantic of the initial sync -- the process
// exits, a restart resumes from hive_blocks head), instead of retrying
// forever. Transient RPC/DB errors during a multi-week 0->30M sync are
// ridden out in-process; every failure is logged with the block number,
// the consecutive-failure count and the fetch/process classification.
func (s *Sync) syncBlocksBoundedRetry(ctx context.Context, ubound, headBefore int64) error {
	blockRepo := db.NewBlockRepository(s.blockProcessor.repo)
	var poisonBlock int64
	var poisonFails int
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		head := headBefore
		if hb, err := blockRepo.GetHead(ctx); err == nil && hb != nil {
			head = hb.Num
		}
		if head >= ubound {
			return nil
		}
		err := s.syncBlocks(ctx, head+1, ubound, true)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The first unprocessed block of the failed attempt: with
		// batch-level rollback every block of the failed tx batch rolled
		// back, so head+1 is the batch start; process failures identify the
		// actually-failing block through the error (see processBlockError).
		retryBlock := poisonTargetBlock(head, err)
		if retryBlock == poisonBlock {
			poisonFails++
		} else {
			poisonBlock = retryBlock
			poisonFails = 1
		}
		if poisonFails >= poisonBlockThreshold {
			s.logger.Error("poison block: initial sync giving up after repeated failures of the same block",
				zap.Int64("block", poisonBlock),
				zap.Int("consecutive_failures", poisonFails),
				zap.Int("threshold", poisonBlockThreshold),
				zap.String("failure_kind", syncFailureKind(err)),
				zap.Error(err))
			return fmt.Errorf("initial sync stalled on block %d after %d attempts: %w",
				poisonBlock, poisonFails, err)
		}
		s.logger.Error("Initial sync attempt failed; retrying",
			zap.Int64("block", poisonBlock),
			zap.Int("consecutive_failures", poisonFails),
			zap.Int("threshold", poisonBlockThreshold),
			zap.String("failure_kind", syncFailureKind(err)),
			zap.Error(err))
		s.wait(ctx, s.config.Indexer.SyncInterval)
	}
}

// defaultTxBatchBlocks mirrors legacy sync.py's chunk_size (from_steemd /
// from_checkpoints): the number of blocks sharing one database transaction.
const defaultTxBatchBlocks = 1000

// txBatchBlocks returns the configured tx-batch size (HIVE_INDEXER_TX_BATCH_
// BLOCKS). Values <= 0 select the legacy default; this also covers tests
// that construct config.Config zero-values.
func (s *Sync) txBatchBlocks() int64 {
	if s.config == nil || s.config.Indexer.TxBatchBlocks <= 0 {
		return defaultTxBatchBlocks
	}
	return int64(s.config.Indexer.TxBatchBlocks)
}

// syncBlocks syncs a range of blocks. isInitialSync must reflect the phase
// the indexer is in: historical catch-up during startup passes true (suppresses
// follow count deltas, follow notifications and account dirty-marking, exactly
// as legacy gates on DbState.is_initial_sync()), the steady-state
// syncIrreversible loop passes false. The phase itself is persisted in
// hive_state.is_initial_sync (see resolveInitialSyncState).
//
// Transaction structure (§4.1, legacy blocks.process_multi): blocks are
// grouped into tx batches of HIVE_INDEXER_TX_BATCH_BLOCKS (default 1000 =
// legacy chunk_size). One batch = one transaction: the blocks are fetched
// first, then processed, follow-count-flushed and committed atomically. A
// failure of ANY block in the batch rolls back the WHOLE batch (legacy
// parity: a chunk transaction failure rolls back the chunk and the sync
// retries it), and the retry re-processes the batch from its first block.
//
// Shutdown semantics (§2.9, batch granularity): cancellation is only checked
// BETWEEN tx batches. A batch whose fetch already succeeded always finishes
// -- its transaction, the in-transaction follow flush and the trailing queue
// flushes run on a context with the cancellation removed -- so a SIGTERM
// cannot leave a half-flushed dirty queue or an aborted in-flight
// transaction behind.
func (s *Sync) syncBlocks(ctx context.Context, from, to int64, isInitialSync bool) error {
	s.logger.Info("Syncing blocks",
		zap.Int64("from", from),
		zap.Int64("to", to),
		zap.Int64("count", to-from+1),
		zap.Bool("initial_sync", isInitialSync),
		zap.Int64("tx_batch_blocks", s.txBatchBlocks()))

	txBatch := s.txBatchBlocks()
	for start := from; start <= to; start += txBatch {
		// Cancellation boundary between tx batches (see method comment): a
		// shutdown here aborts with nothing in flight -- the previous batch
		// committed and flushed, this one has no transaction open.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		end := start + txBatch - 1
		if end > to {
			end = to
		}
		if err := s.syncTxBatch(ctx, start, end, isInitialSync); err != nil {
			return err
		}
	}

	return nil
}

// syncTxBatch runs one transaction batch: fetch the whole batch, then open
// the transaction, process every block, flush the follow count deltas
// INSIDE it, COMMIT, and run the trailing queue flushes between
// transactions.
func (s *Sync) syncTxBatch(ctx context.Context, from, to int64, isInitialSync bool) error {
	// Phase 1 -- fetch the whole batch BEFORE opening the transaction
	// (legacy sync.py from_steemd: `blocks = steemd.get_blocks_range(lbound,
	// to)` then `Blocks.process_multi(blocks)`): the transaction never spans
	// an RPC, and a cancelled fetch aborts the batch with nothing open to
	// roll back. Fetches are chunked at the fetch batch size (capped by the
	// tx batch size so the degenerate 1-block tx batch also fetches singly).
	fetchSize := int64(s.config.Indexer.MaxBatch)
	if fetchSize > s.txBatchBlocks() || fetchSize < 1 {
		fetchSize = s.txBatchBlocks()
	}
	blocks := make([]map[string]interface{}, 0, to-from+1)
	var fetchDur time.Duration
	for start := from; start <= to; start += fetchSize {
		end := start + fetchSize - 1
		if end > to {
			end = to
		}
		fetchStart := time.Now()
		got, err := s.steem.GetBlocksRange(ctx, start, end)
		fetchDur += time.Since(fetchStart)
		if err != nil {
			return fmt.Errorf("%w %d-%d: %w", errFetchBlocks, start, end, err)
		}
		blocks = append(blocks, got...)
	}

	// Phase 2 -- the transaction. Runs on a context with the cancellation
	// removed (#405 semantics at the tx-batch granularity): a batch whose
	// fetch succeeded always finishes.
	batchCtx := context.WithoutCancel(ctx)
	writeStart := time.Now()
	tx := s.db.DB.WithContext(batchCtx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("failed to start batch transaction: %w", tx.Error)
	}
	committed := false
	// Snapshot the pending follow count deltas before the batch: they
	// accumulate in FollowIndexer memory OUTSIDE the transaction, so a batch
	// rollback must discard the whole batch's deltas -- the retry re-processes
	// every block of the batch and re-accumulates them (the batch-level
	// analogue of the per-block snapshot in ProcessBlock; PR#406 audit P1).
	restoreDeltas := s.blockProcessor.Follows().SnapshotDeltas()
	defer func() {
		if r := recover(); r != nil {
			restoreDeltas()
			if !committed {
				tx.Rollback()
			}
			panic(r)
		}
	}()

	for _, block := range blocks {
		if err := s.blockProcessor.ProcessBlockInTx(batchCtx, tx, block, isInitialSync); err != nil {
			restoreDeltas()
			tx.Rollback()
			return &processBlockError{block: blockNumberOf(block), err: err}
		}
	}

	// Follow count deltas ride the batch transaction (legacy
	// blocks.process_multi: Follow.flush(trx=False) runs INSIDE the chunk
	// transaction, before COMMIT): the block writes and the count UPDATEs
	// become atomic, closing the crash window where committed blocks'
	// deltas lived only in memory and were lost on a process death before
	// the flush (PR#406 audit registration, delivered with #408).
	if err := s.blockProcessor.Follows().FlushTx(batchCtx, tx); err != nil {
		restoreDeltas()
		tx.Rollback()
		return fmt.Errorf("failed to flush follow deltas in batch transaction: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		// The commit itself failed: nothing is durably landed, so the
		// batch's deltas must not survive either -- the retry re-processes
		// and re-accumulates them.
		restoreDeltas()
		return fmt.Errorf("failed to commit batch transaction: %w", err)
	}
	committed = true

	// Trailing queue flushes BETWEEN batch transactions (legacy from_steemd:
	// `Accounts.flush(trx=True)` and `CachedPost.flush(trx=True)` run AFTER
	// the chunk loop, in their own transactions -- NOT inside the chunk
	// transaction; only Follow.flush rides it). These make steemd RPC calls
	// (get_accounts / get_content_batch), which is exactly why legacy keeps
	// them out of the chunk transaction. Their dirty queues live in memory
	// and are explicitly disposable in legacy terms: a crash can lose edits
	// and pre-payout votes ("worst case it will be synced upon payout"),
	// and RecoverMissingPosts heals missing cache INSERTs at startup.
	if n, err := s.blockProcessor.Accounts().FlushBatch(batchCtx, true); err != nil {
		s.logger.Warn("account flush failed", zap.Error(err))
	} else if n > 0 {
		s.logger.Debug("Accounts flushed", zap.Int("count", n))
	}

	if counts, err := s.blockProcessor.CachedPost().Flush(batchCtx, true); err != nil {
		s.logger.Warn("post cache flush failed", zap.Error(err))
	} else if total := len(counts); total > 0 {
		s.logger.Debug("Post cache flushed",
			zap.Int("inserts", counts["insert"]),
			zap.Int("updates", counts["update"]+counts["payout"]+counts["upvote"]+counts["recount"]))
	}

	// Throughput observability (§4.2): the counter's rate() is blocks/s;
	// fetch and write histograms attribute a slowdown to the RPC or the
	// transactional path. Log at Info for substantial batches (initial-sync
	// chunks) and Debug for the small live-mode batches.
	writeDur := time.Since(writeStart)
	phase := "live"
	if isInitialSync {
		phase = "initial"
	}
	telemetry.RecordIndexerBatch(phase, len(blocks), fetchDur.Seconds(), writeDur.Seconds())
	rate := 0.0
	if writeDur > 0 {
		rate = float64(len(blocks)) / writeDur.Seconds()
	}
	batchLog := s.logger.Debug
	if len(blocks) >= 100 {
		batchLog = s.logger.Info
	}
	batchLog("Synced block batch",
		zap.Int64("from", from),
		zap.Int64("to", to),
		zap.Int("blocks", len(blocks)),
		zap.Duration("fetch", fetchDur),
		zap.Duration("write", writeDur),
		zap.Float64("blocks_per_sec", rate))
	return nil
}

// wait waits for the specified duration or until context is cancelled
func (s *Sync) wait(ctx context.Context, seconds int) {
	if seconds <= 0 {
		// Yield once so cancellation is observed promptly; a zero interval
		// is used by tests driving the retry loops.
		select {
		case <-ctx.Done():
		default:
		}
		return
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		return
	}
}
