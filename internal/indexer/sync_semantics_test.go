package indexer

// Initial-sync semantics tests (planned PR#405, KR3 W3):
//   - §2.2  persisted is_initial_sync flag (hive_state, migration 0003):
//           flag flip on completion, legacy feed_cache fallback, no
//           re-entry after completion even with an emptied feed cache,
//           per-vote cache gate still holding under the persisted flag;
//   - §2.3  light restart recovery after a completed initial sync
//           (incremental RecoverMissingPosts, no full Rebuild);
//   - §2.5  statement-timeout exemption for maintenance SQL
//           (Rebuild/ForceRecount/dirtyMissing/audits);
//   - §2.6  TEST_MAX_BLOCK stop point: tail trio runs, clean exit, flag
//           stays true, restart at the same stop is a short path;
//   - 2.2b  recovery ordering (PR#402 audit registration): an interrupted
//           tail (rebuild done, recount missing) heals on restart;
//   - §2.7  CacheSync catch-up gating;
//   - §2.9  graceful shutdown: cancellation between batches, in-flight
//           batch completes and flushes, no dirty queue lost;
//   - §2.1b poison observability (PR#404 registration for #405): fetch vs
//           process classification, block numbers in errors, bounded
//           fail-fast retry in the initial-sync path.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/config"
)

// ---- fixtures -------------------------------------------------------------

// syncBlockFixture builds a block whose transactions carry the given
// [type, value] operation tuples (same shape as robustnessBlock). The
// operations list must stay []interface{} -- the block processor asserts
// that exact type when unpacking.
func syncBlockFixture(num int64, ts string, ops ...[]interface{}) map[string]interface{} {
	operations := make([]interface{}, 0, len(ops))
	for _, op := range ops {
		operations = append(operations, op)
	}
	transactions := make([]interface{}, 0, 1)
	if len(operations) > 0 {
		transactions = append(transactions, map[string]interface{}{"operations": operations})
	}
	return map[string]interface{}{
		"block_num":       float64(num),
		"block_id":        fmt.Sprintf("%040x", num),
		"previous":        genesisHash,
		"timestamp":       ts,
		"transactions":    transactions,
		"transaction_ids": []interface{}{},
	}
}

// commentOp is a root-post comment operation tuple.
func commentOp(author, permlink string) []interface{} {
	return []interface{}{"comment_operation", map[string]interface{}{
		"author":          author,
		"permlink":        permlink,
		"parent_author":   "",
		"parent_permlink": "",
		"title":           "t",
		"body":            "b",
		"json_metadata":   `{"tags":["life"]}`,
	}}
}

// voteOp is a vote operation tuple.
func voteOp(voter, author, permlink string) []interface{} {
	return []interface{}{"vote_operation", map[string]interface{}{
		"voter":    voter,
		"author":   author,
		"permlink": permlink,
		"weight":   10000,
	}}
}

// syncTestProvider builds a fake provider serving the given blocks with a
// fixed last-irreversible and a get_content batch that echoes a valid
// steemd post for every request.
func syncTestProvider(blocks map[int64]map[string]interface{}, lib int64) *fakeSteemProvider {
	provider := &fakeSteemProvider{
		lastIrreversible: func(context.Context) (int64, error) { return lib, nil },
		getBlocksRange: func(_ context.Context, from, to int64) ([]map[string]interface{}, error) {
			out := make([]map[string]interface{}, 0, to-from+1)
			for n := from; n <= to; n++ {
				b, ok := blocks[n]
				if !ok {
					return nil, fmt.Errorf("no fixture for block %d", n)
				}
				out = append(out, b)
			}
			return out, nil
		},
	}
	provider.getContentBatch = func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i, p := range posts {
			out[i] = steemdPostFor(p[0], p[1], "sync body")
		}
		return out, nil
	}
	return provider
}

// readFlags reads the persisted hive_state markers straight from SQL.
func readFlags(t *testing.T, database *db.DB, ctx context.Context) (isInitial, recountDone sql.NullBool) {
	t.Helper()
	row := database.DB.WithContext(ctx).
		Raw("SELECT is_initial_sync, recount_done FROM hive_state LIMIT 1").Row()
	if err := row.Scan(&isInitial, &recountDone); err != nil {
		t.Fatalf("read hive_state flags: %v", err)
	}
	return isInitial, recountDone
}

// runSyncInBackground runs s.Run in a goroutine and returns the channel it
// will report on. Tests cancel the run context when appropriate and then
// must always drain the channel before their cleanup closes the database.
func runSyncInBackground(s *Sync, ctx context.Context) chan error {
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	return done
}

// waitRunReturn waits for Run to report with a deadline.
func waitRunReturn(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		t.Fatal("Sync.Run did not return in time")
		return nil
	}
}

// waitFor polls cond until true or the deadline expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- §2.2 persisted is_initial_sync flag ---------------------------------

// TestInitialSyncFlagPersistence drives a full initial sync (2 blocks) via
// Sync.Run, asserts the persisted flag flips false with the tail trio
// complete, then proves the flag -- not the feed_cache heuristic -- decides
// the phase: after truncating hive_feed_cache (the fragile legacy input),
// a restart must NOT re-enter initial sync.
func TestInitialSyncFlagPersistence(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob")

	blocks := map[int64]map[string]interface{}{
		1: syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1")),
		2: followPluginBlock(2, "2026-01-01T00:00:03",
			customJSONOp("alice", `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)),
	}
	provider := syncTestProvider(blocks, 2)
	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, SyncInterval: 1}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := runSyncInBackground(s, runCtx)

	// Wait for the initial sync to complete (flag flips false).
	waitFor(t, "is_initial_sync=false after initial sync", func() bool {
		isInitial, recount := readFlags(t, database, ctx)
		return isInitial.Valid && !isInitial.Bool && recount.Valid && recount.Bool
	})

	// Tail trio side effects: feed cache rebuilt from the root post, and
	// the follow op produced no notification (initial-sync gate).
	var feed int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").Count(&feed)
	if feed != 1 {
		t.Errorf("feed_cache rows after initial sync = %d, want 1", feed)
	}
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").Count(&notifs)
	if notifs != 0 {
		t.Errorf("hive_notifs rows after initial sync = %d, want 0 (isInitialSync gate)", notifs)
	}

	cancel()
	if err := waitRunReturn(t, done); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// The fragile legacy heuristic would now say "initial sync": the feed
	// cache is empty. The persisted flag must win.
	if err := database.DB.WithContext(ctx).Exec("TRUNCATE hive_feed_cache").Error; err != nil {
		t.Fatalf("truncate feed cache: %v", err)
	}

	s2, err := NewSync(cfg, database, syncTestProvider(blocks, 2))
	if err != nil {
		t.Fatalf("NewSync (restart): %v", err)
	}
	runCtx2, cancel2 := context.WithCancel(ctx)
	done2 := runSyncInBackground(s2, runCtx2)
	time.Sleep(2 * time.Second) // let the recovery path run
	cancel2()
	if err := waitRunReturn(t, done2); err != nil {
		t.Fatalf("Run (restart) returned error: %v", err)
	}

	isInitial, _ := readFlags(t, database, ctx)
	if !isInitial.Valid || isInitial.Bool {
		t.Errorf("is_initial_sync after restart = %+v, want false (persisted flag wins over emptied feed cache)", isInitial)
	}
	// Recovery must not have re-run the tail trio (feed cache still empty).
	var feedAfter int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").Count(&feedAfter)
	if feedAfter != 0 {
		t.Errorf("feed_cache rows after restart recovery = %d, want 0 (no Rebuild re-run)", feedAfter)
	}
}

// TestInitialSyncVoteGateUnderPersistedFlag pins that the per-vote cache
// queueing gate still holds for blocks processed under initial-sync
// semantics (§2.2 acceptance ①): a vote op queues nothing during initial
// sync, one queue entry in live mode.
func TestInitialSyncVoteGateUnderPersistedFlag(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob")

	provider := syncTestProvider(nil, 100)
	bp := NewBlockProcessor(database, repo, provider)

	blk := syncBlockFixture(10, "2026-01-01T00:00:00", voteOp("bob", "alice", "p1"))

	if err := bp.ProcessBlock(ctx, blk, true); err != nil {
		t.Fatalf("ProcessBlock initial: %v", err)
	}
	if n := bp.CachedPost().PendingQueueLen(); n != 0 {
		t.Errorf("dirty queue after initial-sync vote op = %d, want 0", n)
	}

	blk2 := syncBlockFixture(11, "2026-01-01T00:00:03", voteOp("bob", "alice", "p1"))
	if err := bp.ProcessBlock(ctx, blk2, false); err != nil {
		t.Fatalf("ProcessBlock live: %v", err)
	}
	if n := bp.CachedPost().PendingQueueLen(); n != 1 {
		t.Errorf("dirty queue after live vote op = %d, want 1", n)
	}
}

// TestResolveInitialSyncFallback covers the legacy-database path: with the
// persisted markers unset (NULL), the phase derives from feed_cache
// emptiness exactly once and materializes both markers.
func TestResolveInitialSyncFallback(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice")
	provider := syncTestProvider(nil, 0)
	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}

	// Empty feed cache -> initial sync, materialized.
	flags, err := s.resolveInitialSyncState(ctx)
	if err != nil {
		t.Fatalf("resolveInitialSyncState: %v", err)
	}
	if !flags.IsInitialSync.Valid || !flags.IsInitialSync.Bool {
		t.Fatalf("derived is_initial_sync = %+v, want true", flags.IsInitialSync)
	}
	got, recount := readFlags(t, database, ctx)
	if !got.Valid || !got.Bool {
		t.Errorf("persisted is_initial_sync = %+v, want true (materialized)", got)
	}
	if recount.Valid && recount.Bool {
		t.Errorf("recount_done = %+v, want false while initial sync pending", recount)
	}

	// Populated feed cache (legacy completed DB) -> not initial, presumed
	// healthy: recount_done materialized true (HIVE_FORCE_FOLLOW_RECOUNT
	// remains the manual override).
	acct := &models.Account{}
	if err := database.DB.WithContext(ctx).Where("name = ?", "alice").First(acct).Error; err != nil {
		t.Fatalf("load alice: %v", err)
	}
	database.DB.WithContext(ctx).
		Exec("INSERT INTO hive_feed_cache (account_id, post_id, created_at) VALUES (?, 1, '2026-01-01')", acct.ID)
	database.DB.WithContext(ctx).
		Exec("UPDATE hive_state SET is_initial_sync = NULL, recount_done = NULL")

	flags2, err := s.resolveInitialSyncState(ctx)
	if err != nil {
		t.Fatalf("resolveInitialSyncState (2nd): %v", err)
	}
	if !flags2.IsInitialSync.Valid || flags2.IsInitialSync.Bool {
		t.Fatalf("derived is_initial_sync (2nd) = %+v, want false", flags2.IsInitialSync)
	}
	got2, recount2 := readFlags(t, database, ctx)
	if !got2.Valid || got2.Bool {
		t.Errorf("persisted is_initial_sync (2nd) = %+v, want false", got2)
	}
	if !recount2.Valid || !recount2.Bool {
		t.Errorf("persisted recount_done (2nd) = %+v, want true (legacy DB presumed healthy)", recount2)
	}
}

// ---- §2.6 TEST_MAX_BLOCK stop point ---------------------------------------

// TestTestMaxBlockStopPoint pins the stop-point semantics: the tail trio
// runs at the cap, the process exits cleanly (Run returns nil on its own),
// the persisted flag stays TRUE, and a restart at the same stop point is a
// short path (trio skipped). Bumping the cap resumes the initial sync.
func TestTestMaxBlockStopPoint(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice")

	blocks := map[int64]map[string]interface{}{
		1: syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1")),
		2: syncBlockFixture(2, "2026-01-01T00:00:03", commentOp("alice", "p2")),
		3: syncBlockFixture(3, "2026-01-01T00:00:06", commentOp("alice", "p3")),
	}
	provider := syncTestProvider(blocks, 3)

	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, TestMaxBlock: 2}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}
	// Returns on its own at the stop point -- no cancellation involved.
	if err := s.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if head := s.headBlockNum(ctx); head != 2 {
		t.Errorf("head after stop = %d, want 2", head)
	}
	isInitial, recount := readFlags(t, database, ctx)
	if !isInitial.Valid || !isInitial.Bool {
		t.Errorf("is_initial_sync after TEST_MAX_BLOCK stop = %+v, want true (not flipped)", isInitial)
	}
	if !recount.Valid || !recount.Bool {
		t.Errorf("recount_done after TEST_MAX_BLOCK stop = %+v, want true (tail trio ran)", recount)
	}
	var feed int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").Count(&feed)
	if feed != 2 {
		t.Errorf("feed_cache rows after stop = %d, want 2 (tail trio ran)", feed)
	}

	// Restart at the same stop point: short path, trio skipped.
	if err := database.DB.WithContext(ctx).Exec("TRUNCATE hive_feed_cache").Error; err != nil {
		t.Fatalf("truncate feed cache: %v", err)
	}
	s2, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync (restart): %v", err)
	}
	if err := s2.Run(ctx); err != nil {
		t.Fatalf("Run (restart at same stop): %v", err)
	}
	var feedAfter int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").Count(&feedAfter)
	if feedAfter != 0 {
		t.Errorf("feed_cache rows after restart at same stop = %d, want 0 (trio skipped)", feedAfter)
	}
	if head := s2.headBlockNum(ctx); head != 2 {
		t.Errorf("head after restart at same stop = %d, want 2", head)
	}

	// Bumping the cap resumes the initial sync in fast-sync semantics and
	// re-runs the tail trio at the new stop point.
	cfg3 := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, TestMaxBlock: 3}}
	s3, err := NewSync(cfg3, database, provider)
	if err != nil {
		t.Fatalf("NewSync (resume): %v", err)
	}
	if err := s3.Run(ctx); err != nil {
		t.Fatalf("Run (resume to 3): %v", err)
	}
	if head := s3.headBlockNum(ctx); head != 3 {
		t.Errorf("head after resume = %d, want 3", head)
	}
	var feed3 int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").Count(&feed3)
	if feed3 != 3 {
		t.Errorf("feed_cache rows after resume = %d, want 3 (trio re-ran at new stop)", feed3)
	}
	isInitial3, _ := readFlags(t, database, ctx)
	if !isInitial3.Valid || !isInitial3.Bool {
		t.Errorf("is_initial_sync after resume = %+v, want still true", isInitial3)
	}
}

// ---- recovery ordering (PR#402 audit registration) ------------------------

// TestInterruptedTailRecovery simulates the audit failure shape: the tail
// trio's Rebuild completed (feed cache populated) but ForceRecount never
// ran (recount_done unset, counts zeroed). The restart must re-run the
// missing tail instead of entering the normal loop with counts stuck at 0.
func TestInterruptedTailRecovery(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob")

	blocks := map[int64]map[string]interface{}{
		1: syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1")),
		2: followPluginBlock(2, "2026-01-01T00:00:03",
			customJSONOp("alice", `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)),
	}
	provider := syncTestProvider(blocks, 2)

	// Complete a full initial sync at a TEST_MAX_BLOCK stop (flag stays
	// true by design).
	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, TestMaxBlock: 2}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}
	if err := s.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Corrupt into the audit shape: recount marker lost, follow counts
	// zeroed (as if recount never ran), feed cache still populated.
	if err := database.DB.WithContext(ctx).Exec(
		"UPDATE hive_state SET recount_done = false").Error; err != nil {
		t.Fatalf("clear recount_done: %v", err)
	}
	if err := database.DB.WithContext(ctx).Exec(
		"UPDATE hive_accounts SET followers = 0, following = 0").Error; err != nil {
		t.Fatalf("zero counts: %v", err)
	}

	// Restart at the same stop point: 0 blocks to sync, but the missing
	// tail must heal -- the trio re-runs and the counts recover.
	s2, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync (restart): %v", err)
	}
	if err := s2.Run(ctx); err != nil {
		t.Fatalf("Run (recovery): %v", err)
	}

	bob := getAccount(t, database, ctx, "bob")
	alice := getAccount(t, database, ctx, "alice")
	if bob.Followers != 1 {
		t.Errorf("bob.followers after recovery = %d, want 1", bob.Followers)
	}
	if alice.Following != 1 {
		t.Errorf("alice.following after recovery = %d, want 1", alice.Following)
	}
	_, recount := readFlags(t, database, ctx)
	if !recount.Valid || !recount.Bool {
		t.Errorf("recount_done after recovery = %+v, want true", recount)
	}
}

// ---- §2.3 light restart recovery ------------------------------------------

// TestRestartRecoveryAfterCompletion kills-and-restarts an initialized
// database with an artificial cache gap: the startup recovery heals the
// gap incrementally (RecoverMissingPosts) WITHOUT re-running the full
// Rebuild (a deleted feed-cache row stays deleted).
func TestRestartRecoveryAfterCompletion(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice")

	blocks := map[int64]map[string]interface{}{
		1: syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1")),
		2: syncBlockFixture(2, "2026-01-01T00:00:03", commentOp("alice", "p2")),
	}
	provider := syncTestProvider(blocks, 2)
	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, SyncInterval: 1}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := runSyncInBackground(s, runCtx)
	waitFor(t, "initial sync completion", func() bool {
		isInitial, _ := readFlags(t, database, ctx)
		return isInitial.Valid && !isInitial.Bool
	})
	cancel()
	if err := waitRunReturn(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Simulate a lost flush after completion: hive_posts row with no cache
	// entry. Also drop one feed-cache row: a full Rebuild would restore it,
	// the light recovery must not.
	var lastPost models.Post
	if err := database.DB.WithContext(ctx).Order("id DESC").First(&lastPost).Error; err != nil {
		t.Fatalf("load last post: %v", err)
	}
	gapPost := &models.Post{Author: "alice", Permlink: "gap", Category: "life",
		CreatedAt: time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true}
	if err := database.DB.WithContext(ctx).Create(gapPost).Error; err != nil {
		t.Fatalf("seed gap post: %v", err)
	}
	if err := database.DB.WithContext(ctx).
		Where("post_id = ?", lastPost.ID).
		Delete(&models.FeedCache{}).Error; err != nil {
		t.Fatalf("drop feed row: %v", err)
	}
	var feedRows int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").Count(&feedRows)
	if feedRows != 1 {
		t.Fatalf("feed rows after surgery = %d, want 1", feedRows)
	}

	// Restart: light recovery heals the cache gap...
	s2, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync (restart): %v", err)
	}
	runCtx2, cancel2 := context.WithCancel(ctx)
	done2 := runSyncInBackground(s2, runCtx2)
	waitFor(t, "cache gap healed by recovery", func() bool {
		var n int64
		database.DB.WithContext(ctx).
			Table("hive_posts_cache").Where("post_id = ?", gapPost.ID).Count(&n)
		return n == 1
	})
	cancel2()
	if err := waitRunReturn(t, done2); err != nil {
		t.Fatalf("Run (restart): %v", err)
	}

	// ...but did not re-run the full Rebuild (dropped feed row stays gone).
	var feedAfter int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").Count(&feedAfter)
	if feedAfter != 1 {
		t.Errorf("feed rows after recovery = %d, want 1 (no full Rebuild on restart)", feedAfter)
	}
	// No re-entry into initial sync either.
	isInitial, _ := readFlags(t, database, ctx)
	if !isInitial.Valid || isInitial.Bool {
		t.Errorf("is_initial_sync after recovery = %+v, want false", isInitial)
	}
}

// ---- §2.5 statement-timeout exemption --------------------------------------

// TestMaintenanceStatementTimeoutExemption proves the exemption end to end
// on a pool whose DSN carries a 5ms statement timeout: a plain slow
// statement is cancelled, the same statement inside
// db.WithoutStatementTimeout succeeds, and the maintenance routines
// (ForceRecount, FeedCache.Rebuild) run through the exemption.
func TestMaintenanceStatementTimeoutExemption(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob")

	// Second pool on the same schema with a hostile 5ms statement timeout
	// (injected via the DSN exactly like production's 30s). Closed before
	// the schema-resetting cleanup runs (deferred later => runs first).
	tight, err := db.New(&config.DatabaseConfig{
		URL:              os.Getenv("HIVE_TEST_DATABASE_URL"),
		MaxOpenConns:     2,
		MaxIdleConns:     1,
		ConnMaxLifetime:  time.Hour,
		ConnMaxIdleTime:  10 * time.Minute,
		StatementTimeout: 5 * time.Millisecond,
	}, "ERROR")
	if err != nil {
		t.Fatalf("open tight-timeout pool: %v", err)
	}
	defer func() { _ = tight.Close() }()

	// Control: the pool really carries the timeout -- a 200ms statement
	// dies with the statement-timeout error.
	err = tight.DB.WithContext(ctx).Exec("SELECT pg_sleep(0.2)").Error
	if err == nil {
		t.Fatal("plain pg_sleep(0.2) on a 5ms-timeout pool unexpectedly succeeded")
	}
	t.Logf("control timeout error (driver phrasing): %v", err)

	// Exempt: the same statement inside the helper succeeds.
	if err := db.WithoutStatementTimeout(ctx, tight.DB, func(tx *gorm.DB) error {
		return tx.Exec("SELECT pg_sleep(0.2)").Error
	}); err != nil {
		t.Fatalf("pg_sleep inside WithoutStatementTimeout: %v", err)
	}

	// Maintenance routines on the hostile pool succeed (they run inside the
	// exemption; on a large database their statements exceed any session
	// guard -- that is what §2.5 fixes).
	repo := db.NewRepository(tight.DB)
	follows := NewFollowIndexer(repo, testLogger())
	if err := follows.ForceRecount(ctx); err != nil {
		t.Errorf("ForceRecount on 5ms-timeout pool: %v", err)
	}
	if err := db.NewFeedCacheRepository(repo).Rebuild(ctx, true); err != nil {
		t.Errorf("FeedCache.Rebuild on 5ms-timeout pool: %v", err)
	}
}

// ---- §2.7 CacheSync gating -------------------------------------------------

func TestCacheSyncGating(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	old := time.Now().UTC().AddDate(0, 0, -100)
	tx := database.DB.WithContext(ctx).Begin()
	tx.Exec(`INSERT INTO hive_posts_cache_temp (post_id, author, permlink, created_at)
	         VALUES (1, 'a', 'p1', ?)`, old)
	tx.Commit()

	countTemp := func() int64 {
		var n int64
		database.DB.WithContext(ctx).Table("hive_posts_cache_temp").Count(&n)
		return n
	}

	cs := NewCacheSync(database.DB)

	// Unknown distance (fresh instance): paused.
	cs.Sync(ctx)
	if n := countTemp(); n != 1 {
		t.Fatalf("temp rows with unknown behind-distance = %d, want 1 (paused)", n)
	}

	// Catch-up beyond the default threshold (100): paused.
	cs.SetBehind(101)
	cs.Sync(ctx)
	if n := countTemp(); n != 1 {
		t.Fatalf("temp rows during catch-up = %d, want 1 (paused)", n)
	}

	// At the threshold: live, prune runs.
	cs.SetBehind(100)
	cs.Sync(ctx)
	if n := countTemp(); n != 0 {
		t.Fatalf("temp rows when live at threshold = %d, want 0 (pruned)", n)
	}

	// Configurable threshold: 0 means only a fully caught-up indexer prunes.
	tx = database.DB.WithContext(ctx).Begin()
	tx.Exec(`INSERT INTO hive_posts_cache_temp (post_id, author, permlink, created_at)
	         VALUES (2, 'a', 'p2', ?)`, old)
	tx.Commit()
	cs.SetCatchupThreshold(0)
	cs.SetBehind(1)
	cs.Sync(ctx)
	if n := countTemp(); n != 1 {
		t.Fatalf("temp rows behind=1 with zero threshold = %d, want 1 (paused)", n)
	}
	cs.SetBehind(0)
	cs.Sync(ctx)
	if n := countTemp(); n != 0 {
		t.Fatalf("temp rows behind=0 with zero threshold = %d, want 0 (pruned)", n)
	}
}

// ---- §2.9 graceful shutdown -------------------------------------------------

// TestGracefulShutdownFlushesInFlightBatch cancels the run context DURING
// the fetch of the second block batch. The fetch succeeds, so the in-flight
// batch must still complete -- its block transactions commit and the dirty
// queues flush -- before Run returns nil. Nothing queued is lost.
func TestGracefulShutdownFlushesInFlightBatch(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice")

	blocks := map[int64]map[string]interface{}{
		1: syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1")),
		2: syncBlockFixture(2, "2026-01-01T00:00:03", commentOp("alice", "p2")),
		3: syncBlockFixture(3, "2026-01-01T00:00:06", commentOp("alice", "p3")),
		4: syncBlockFixture(4, "2026-01-01T00:00:09", commentOp("alice", "p4")),
	}

	runCtx, cancel := context.WithCancel(ctx)
	provider := &fakeSteemProvider{
		lastIrreversible: func(context.Context) (int64, error) { return 4, nil },
		getBlocksRange: func(c context.Context, from, to int64) ([]map[string]interface{}, error) {
			out := make([]map[string]interface{}, 0, to-from+1)
			for n := from; n <= to; n++ {
				out = append(out, blocks[n])
			}
			if from == 3 {
				// Shutdown lands while this batch is being fetched: the
				// fetch still succeeds, so the batch is "in flight" and
				// must complete.
				cancel()
			}
			return out, nil
		},
	}
	provider.getContentBatch = func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i, p := range posts {
			out[i] = steemdPostFor(p[0], p[1], "shutdown body")
		}
		return out, nil
	}

	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 2, SyncInterval: 1}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}
	done := runSyncInBackground(s, runCtx)

	if err := waitRunReturn(t, done); err != nil {
		t.Fatalf("Run after cancellation returned error: %v (want nil: cancellation is a clean exit)", err)
	}

	// The in-flight batch [3,4] completed and committed.
	if head := s.headBlockNum(context.Background()); head != 4 {
		t.Errorf("head after shutdown = %d, want 4 (in-flight batch must complete)", head)
	}
	// No dirty queue residue: the batch-end flush drained everything.
	if n := s.blockProcessor.CachedPost().PendingQueueLen(); n != 0 {
		t.Errorf("cached-post dirty queue after shutdown = %d, want 0", n)
	}
	if n := s.blockProcessor.Follows().PendingDeltasLen(); n != 0 {
		t.Errorf("follow delta queue after shutdown = %d, want 0", n)
	}
	// All four posts made it into the cache (flush ran on the detached
	// batch context, not the cancelled one).
	var cached int64
	database.DB.WithContext(context.Background()).
		Table("hive_posts_cache").Where("author = ?", "alice").Count(&cached)
	if cached != 4 {
		t.Errorf("cached posts after shutdown = %d, want 4 (flush completed post-cancel)", cached)
	}
}

// ---- §2.1b poison observability (minimal #405 scope) ------------------------

// TestSyncBlocksErrorClassification pins the fetch/process distinction and
// the block numbers wrapped into the errors.
func TestSyncBlocksErrorClassification(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice")

	// Fetch failure: provider errors on the range.
	provider := &fakeSteemProvider{
		lastIrreversible: func(context.Context) (int64, error) { return 1, nil },
		getBlocksRange: func(context.Context, int64, int64) ([]map[string]interface{}, error) {
			return nil, errors.New("rpc boom")
		},
	}
	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}
	err = s.syncBlocks(ctx, 7, 9, true)
	if !errors.Is(err, errFetchBlocks) {
		t.Fatalf("fetch error classification: got %v, want errFetchBlocks", err)
	}
	if !strings.Contains(err.Error(), "failed to fetch blocks 7-9") {
		t.Errorf("fetch error missing block range: %v", err)
	}
	if kind := syncFailureKind(err); kind != "fetch" {
		t.Errorf("syncFailureKind(fetch) = %q, want fetch", kind)
	}

	// Process failure: re-feeding an already indexed block fails at the
	// block insert (duplicate PK) and must carry the block number.
	block1 := syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1"))
	okProvider := syncTestProvider(map[int64]map[string]interface{}{1: block1}, 1)
	s2, err := NewSync(cfg, database, okProvider)
	if err != nil {
		t.Fatalf("NewSync (2nd): %v", err)
	}
	if err := s2.syncBlocks(ctx, 1, 1, true); err != nil {
		t.Fatalf("first syncBlocks: %v", err)
	}
	err = s2.syncBlocks(ctx, 1, 1, true)
	if !errors.Is(err, errProcessBlock) {
		t.Fatalf("process error classification: got %v, want errProcessBlock", err)
	}
	if !strings.Contains(err.Error(), "failed to process block 1") {
		t.Errorf("process error missing block number: %v", err)
	}
	if kind := syncFailureKind(err); kind != "process" {
		t.Errorf("syncFailureKind(process) = %q, want process", kind)
	}
}

// TestInitialSyncBoundedRetryPoison pins the initial-sync retry semantics:
// transient failures ride out (bounded, logged with the block number and
// consecutive-failure count), a permanently failing block gives up after
// poisonBlockThreshold attempts (fail-fast preserved).
func TestInitialSyncBoundedRetryPoison(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice")

	block1 := syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1"))
	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, SyncInterval: 0}}

	// Permanent failure: gives up after exactly poisonBlockThreshold
	// attempts of the same block.
	attempts := 0
	alwaysFail := &fakeSteemProvider{
		lastIrreversible: func(context.Context) (int64, error) { return 1, nil },
		getBlocksRange: func(context.Context, int64, int64) ([]map[string]interface{}, error) {
			attempts++
			return nil, errors.New("rpc down")
		},
	}
	s, err := NewSync(cfg, database, alwaysFail)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}
	err = s.syncBlocksBoundedRetry(ctx, 1, 0)
	if err == nil {
		t.Fatal("bounded retry returned nil for a permanently failing block")
	}
	if !strings.Contains(err.Error(), "block 1") || !strings.Contains(err.Error(), "10 attempts") {
		t.Errorf("bounded-retry error lacks block/attempt context: %v", err)
	}
	if attempts != poisonBlockThreshold {
		t.Errorf("attempts = %d, want %d", attempts, poisonBlockThreshold)
	}

	// Transient failure: two failures then success completes the sync.
	failures := 0
	flakey := &fakeSteemProvider{
		lastIrreversible: func(context.Context) (int64, error) { return 1, nil },
		getBlocksRange: func(context.Context, int64, int64) ([]map[string]interface{}, error) {
			failures++
			if failures <= 2 {
				return nil, errors.New("transient rpc hiccup")
			}
			return []map[string]interface{}{block1}, nil
		},
	}
	flakey.getContentBatch = func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i, p := range posts {
			out[i] = steemdPostFor(p[0], p[1], "retry body")
		}
		return out, nil
	}
	s2, err := NewSync(cfg, database, flakey)
	if err != nil {
		t.Fatalf("NewSync (flakey): %v", err)
	}
	if err := s2.syncBlocksBoundedRetry(ctx, 1, 0); err != nil {
		t.Fatalf("bounded retry on transient failures: %v", err)
	}
	if head := s2.headBlockNum(ctx); head != 1 {
		t.Errorf("head after transient failures = %d, want 1", head)
	}
}
