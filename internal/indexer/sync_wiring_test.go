package indexer

// Loop-level wiring tests for the sync runloop (planned PR#440, KR6),
// closing registered test gaps from PR#407/#409 (planned #405/#406):
//
//   - payoutSweep LOOP wiring: the direct payoutSweep test existed
//     (payment_promoted_test.go), but nothing pinned that syncIrreversible
//     actually CALLS the sweep each iteration -- a mutation deleting the
//     call escaped the suite (PR#409 registration, item 1). This test
//     drives Sync.Run against a caught-up chain and asserts the payout flag
//     flips WITHOUT touching payoutSweep directly.
//   - syncToCapAndExit on an ALREADY-INITIALIZED database: legacy sync.py:88
//     catches up to test_max_block in normal (non-initial) semantics and
//     returns; the flag-flip path was tested, this path was not (PR#407
//     registration, item 1).
//   - HIVE_FORCE_FOLLOW_RECOUNT: the manual full-recount override wired into
//     recoverFromRestart (PR#407 registration, item 1).

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/config"
)

// markDBInitialized stamps hive_state with the "already initialized" markers
// so Sync.Run takes the already-initialized boot path (light recovery +
// runloop) instead of the initial sync.
func markDBInitialized(t *testing.T, database *db.DB, ctx context.Context) {
	t.Helper()
	if err := database.DB.WithContext(ctx).Exec(
		"UPDATE hive_state SET is_initial_sync = false, recount_done = true").Error; err != nil {
		t.Fatalf("stamp initialized markers: %v", err)
	}
}

// isPaidout reads the is_paidout flag of one cache row.
func isPaidout(t *testing.T, database *db.DB, ctx context.Context, postID int64) bool {
	t.Helper()
	var paidout bool
	database.DB.WithContext(ctx).
		Table("hive_posts_cache").Select("is_paidout").
		Where("post_id = ?", postID).Scan(&paidout)
	return paidout
}

// --- payoutSweep loop wiring (PR#409 registration, item 1) -------------------

// TestPayoutSweepWiredIntoSyncLoop drives the REAL syncIrreversible loop
// (via Sync.Run, SyncInterval=0 for a tight tick) over a database whose head
// already matches the chain's last irreversible block, with one stale payout
// row (payout_at before the head block date). The sweep wired after each
// catch-up round must flip is_paidout through the cache flush. Removing the
// payoutSweep call from the loop (the mutation that escaped the suite) leaves
// the flag at false and fails this test.
func TestPayoutSweepWiredIntoSyncLoop(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice")
	markDBInitialized(t, database, ctx)

	// A post whose cache row says "payout passed, not yet flagged".
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true}
	if err := database.DB.WithContext(ctx).Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}
	if err := database.DB.WithContext(ctx).Exec(
		`INSERT INTO hive_posts_cache (post_id, author, permlink, category, payout_at, is_paidout)
		 VALUES (?, 'alice', 'p1', 'life', '2026-01-09T00:00:00', '0')`, post.ID).Error; err != nil {
		t.Fatalf("seed cache row: %v", err)
	}
	// Head block at the chain head; its date drives the sweep window.
	head := &models.Block{Num: 1000, Hash: "ab" + fmt.Sprintf("%038d", 0),
		CreatedAt: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)}
	if err := database.DB.WithContext(ctx).Create(head).Error; err != nil {
		t.Fatalf("seed head block: %v", err)
	}

	// Paid-out content served by the provider (cashout 1969 => paid out), so
	// the sweep's flush flips the flag. No new blocks to fetch (LIB == head).
	content := steemdPostFor("alice", "p1", "paid out via loop")
	content["cashout_time"] = "1969-12-31T23:59:59"
	content["last_payout"] = "2026-01-09T00:00:00"
	provider := syncTestProvider(nil, 1000)
	provider.getContentBatch = func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i := range posts {
			out[i] = content
		}
		return out, nil
	}

	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, SyncInterval: 0}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := runSyncInBackground(s, runCtx)

	// The loop (no blocks to sync, interval 0) must still sweep payouts.
	waitFor(t, "is_paidout flipped by the sync loop's payout sweep", func() bool {
		return isPaidout(t, database, ctx, post.ID)
	})

	cancel()
	if err := waitRunReturn(t, done); err != nil {
		t.Fatalf("Run returned error after cancellation: %v", err)
	}
}

// --- syncToCapAndExit on an initialized DB (PR#407 registration, item 1) -----

// TestSyncToCapAndExitOnInitializedDB pins the second TEST_MAX_BLOCK path:
// an already-initialized database catches up to the cap in NORMAL semantics
// (is_initial_sync=false down to block processing -- a follow op produces its
// notification, which initial-sync semantics suppress), Run returns nil on
// its own at the cap, and a restart at the same cap fetches nothing.
func TestSyncToCapAndExitOnInitializedDB(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob")
	markDBInitialized(t, database, ctx)

	blocks := map[int64]map[string]interface{}{
		1: syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1")),
		2: followPluginBlock(2, "2026-01-01T00:00:03",
			customJSONOp("alice", `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)),
		3: syncBlockFixture(3, "2026-01-01T00:00:06", commentOp("alice", "p2")),
	}
	var fetches atomic.Int64
	provider := syncTestProvider(blocks, 3)
	wrappedGetBlocks := provider.getBlocksRange
	provider.getBlocksRange = func(c context.Context, from, to int64) ([]map[string]interface{}, error) {
		fetches.Add(1)
		return wrappedGetBlocks(c, from, to)
	}

	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, TestMaxBlock: 3}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}
	// Returns on its own at the cap -- no cancellation, no runloop entry.
	if err := s.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if head := s.headBlockNum(ctx); head != 3 {
		t.Errorf("head after cap catch-up = %d, want 3", head)
	}
	// Normal semantics discriminator: the follow op produced a notification
	// (initial-sync semantics would suppress it).
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").Count(&notifs)
	if notifs != 1 {
		t.Errorf("hive_notifs after cap catch-up = %d, want 1 (normal semantics: follow notif NOT suppressed)", notifs)
	}
	// The initialized markers must survive the cap path untouched.
	isInitial, recount := readFlags(t, database, ctx)
	if !isInitial.Valid || isInitial.Bool {
		t.Errorf("is_initial_sync after cap catch-up = %+v, want false", isInitial)
	}
	if !recount.Valid || !recount.Bool {
		t.Errorf("recount_done after cap catch-up = %+v, want true (cap path does not touch the tail markers)", recount)
	}

	// Restart at the already-reached cap: short path, no block fetches.
	fetchesBefore := fetches.Load()
	s2, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync (restart at cap): %v", err)
	}
	if err := s2.Run(ctx); err != nil {
		t.Fatalf("Run (restart at cap): %v", err)
	}
	if got := fetches.Load() - fetchesBefore; got != 0 {
		t.Errorf("block range fetches on restart at reached cap = %d, want 0 (head >= cap exits immediately)", got)
	}
	if head := s2.headBlockNum(ctx); head != 3 {
		t.Errorf("head after restart at cap = %d, want 3", head)
	}
}

// --- HIVE_FORCE_FOLLOW_RECOUNT (PR#407 registration, item 1) -----------------

// TestForceFollowRecount pins the manual override consumed in
// recoverFromRestart: with recount_done already TRUE (so the interrupted-tail
// rerun does not apply), a follow row and deliberately wrong counts, the flag
// must trigger a full recount that recomputes the counts from hive_follows --
// and without the flag the same recovery must leave the wrong counts alone.
func TestForceFollowRecount(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob")
	markDBInitialized(t, database, ctx)

	alice := getAccount(t, database, ctx, "alice")
	bob := getAccount(t, database, ctx, "bob")
	if err := database.DB.WithContext(ctx).Create(&models.Follow{
		FollowerID: alice.ID, FollowingID: bob.ID, State: models.FollowStateBlog,
		CreatedAt: time.Unix(1458845200, 0).UTC(),
	}).Error; err != nil {
		t.Fatalf("seed follow row: %v", err)
	}
	// Deliberately wrong counts: the recount must overwrite them with the
	// counts derived from hive_follows (bob: 1 follower, alice: 1 following).
	if err := database.DB.WithContext(ctx).Exec(
		"UPDATE hive_accounts SET followers = 5, following = 7 WHERE name IN ('alice','bob')").Error; err != nil {
		t.Fatalf("corrupt counts: %v", err)
	}

	flags := &db.SyncFlags{
		IsInitialSync: sql.NullBool{Bool: false, Valid: true},
		RecountDone:   sql.NullBool{Bool: true, Valid: true},
	}

	// Control: without the flag the recovery leaves the wrong counts alone.
	sOff, err := NewSync(&config.Config{Indexer: config.IndexerConfig{MaxBatch: 50}}, database, syncTestProvider(nil, 0))
	if err != nil {
		t.Fatalf("NewSync (flag off): %v", err)
	}
	if err := sOff.recoverFromRestart(ctx, flags); err != nil {
		t.Fatalf("recoverFromRestart (flag off): %v", err)
	}
	if got := getAccount(t, database, ctx, "bob").Followers; got != 5 {
		t.Fatalf("control: bob.followers = %d with flag off, want 5 (no recount without the override)", got)
	}

	// Override: HIVE_FORCE_FOLLOW_RECOUNT recomputes from hive_follows.
	sOn, err := NewSync(&config.Config{Indexer: config.IndexerConfig{
		MaxBatch: 50, ForceFollowRecount: true}}, database, syncTestProvider(nil, 0))
	if err != nil {
		t.Fatalf("NewSync (flag on): %v", err)
	}
	if err := sOn.recoverFromRestart(ctx, flags); err != nil {
		t.Fatalf("recoverFromRestart (flag on): %v", err)
	}
	if got := getAccount(t, database, ctx, "bob").Followers; got != 1 {
		t.Errorf("bob.followers after forced recount = %d, want 1", got)
	}
	if got := getAccount(t, database, ctx, "alice").Following; got != 1 {
		t.Errorf("alice.following after forced recount = %d, want 1", got)
	}
}
