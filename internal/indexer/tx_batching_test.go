package indexer

// Batch-transaction semantics tests (planned PR#408, KR3 W4; 02-indexer.md
// §四 批次4):
//   - §4.1 a failure of ANY block in a tx batch rolls back the WHOLE batch
//     (legacy process_multi chunk-rollback parity) and the retry re-processes
//     the batch from its first block with every op landed;
//   - §4.1 HIVE_INDEXER_TX_BATCH_BLOCKS=1 restores per-block transactions
//     (the degenerate mode): blocks before the failure stay committed;
//   - §4.1 follow count deltas ride the batch transaction (legacy
//     Follow.flush(trx=False) inside process_multi): after a COMMITTED batch
//     the counts are applied with no separate flush, after a ROLLED-BACK
//     batch no deltas survive (batch-level snapshot/restore), and a retried
//     batch never doubles them;
//   - §4.1 poison observability keeps identifying the actually-failing
//     block, not the rolled-back batch's start;
//   - §4.3 the account flush resolves ids in ONE batched query per chunk
//     (no per-account SELECT) and produces the same columns (incl. rank) as
//     the per-account lookups it replaced.

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm/logger"

	"github.com/steemit/hivemind/pkg/config"
)

// txBatchTestProvider is syncTestProvider plus a get_accounts fn (the
// trailing account flush between batch transactions calls it in live mode;
// without it the fake fails loud).
func txBatchTestProvider(blocks map[int64]map[string]interface{}, lib int64) *fakeSteemProvider {
	provider := syncTestProvider(blocks, lib)
	provider.getAccounts = func(_ context.Context, names []string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(names))
		for i, n := range names {
			out[i] = steemdAccount(n)
		}
		return out, nil
	}
	return provider
}

// poisonOrphanCommentOp is the deterministic poison block payload: a comment
// whose parent does not exist fails the whole block with an infrastructure
// error (PR#406 orphan fail-loud -- legacy crashes on the same data).
func poisonOrphanCommentOp(author string) []interface{} {
	return []interface{}{"comment_operation", map[string]interface{}{
		"author":          author,
		"permlink":        "orphan-reply",
		"parent_author":   "ghost",
		"parent_permlink": "missing-parent",
		"title":           "t",
		"body":            "b",
		"json_metadata":   "",
	}}
}

// TestTxBatchMidBlockFailureRollsBackWholeBatch pins the §4.1 rollback
// boundary: with a 5-block tx batch whose block 3 fails, NOTHING of the
// batch may survive (blocks 1-2 "succeeded" into the open transaction but
// must roll back with it, exactly like legacy process_multi's chunk), the
// follow deltas accumulated by block 2 must be discarded, and the poison
// error must identify block 3 (not the batch start). The retry of the whole
// batch then lands every op with the follow counts applied by the
// IN-TRANSACTION flush -- no separate Follow.Flush call is made anywhere.
func TestTxBatchMidBlockFailureRollsBackWholeBatch(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob")

	followTuple := []interface{}{"custom_json_operation", customJSONOp("alice",
		`["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)}
	blocks := map[int64]map[string]interface{}{
		1: syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1")),
		2: syncBlockFixture(2, "2026-01-01T00:00:03", followTuple),
		3: syncBlockFixture(3, "2026-01-01T00:00:06", poisonOrphanCommentOp("alice")),
		4: syncBlockFixture(4, "2026-01-01T00:00:09", commentOp("bob", "p4")),
		5: syncBlockFixture(5, "2026-01-01T00:00:12", commentOp("alice", "p5")),
	}

	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 2, TxBatchBlocks: 5}}
	s, err := NewSync(cfg, database, txBatchTestProvider(blocks, 5))
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}

	// Attempt 1: block 3 fails (orphan comment -- infrastructure class).
	err = s.syncBlocks(ctx, 1, 5, false)
	if err == nil {
		t.Fatal("syncBlocks must fail on the orphan-comment block")
	}
	var pbe *processBlockError
	if !errors.As(err, &pbe) {
		t.Fatalf("error is not a processBlockError: %v", err)
	}
	if pbe.block != 3 {
		t.Errorf("processBlockError.block = %d, want 3 (the actually-failing block)", pbe.block)
	}
	if !errors.Is(err, errProcessBlock) {
		t.Errorf("error must keep the errProcessBlock sentinel: %v", err)
	}
	if kind := syncFailureKind(err); kind != "process" {
		t.Errorf("syncFailureKind = %q, want process", kind)
	}
	// Poison observability: the retry target is the failing block, while
	// head+1 (the batch start) is 1 -- the two diverge under batch rollback.
	if got := poisonTargetBlock(0, err); got != 3 {
		t.Errorf("poisonTargetBlock = %d, want 3", got)
	}

	// The WHOLE batch rolled back: blocks 1-2 ("succeeded" in-transaction)
	// left nothing behind either.
	if n := countWhere(t, database, "hive_blocks", "num BETWEEN 1 AND 5"); n != 0 {
		t.Fatalf("hive_blocks rows after batch failure = %d, want 0 (whole-batch rollback)", n)
	}
	for table := range map[string]struct{}{"hive_posts": {}, "hive_follows": {}, "hive_notifs": {}} {
		if n := countWhere(t, database, table, "1=1"); n != 0 {
			t.Errorf("%s rows after batch failure = %d, want 0 (whole-batch rollback)", table, n)
		}
	}
	// Block 2's follow deltas must not survive the rollback (batch-level
	// snapshot/restore) -- otherwise the retry below re-accumulates them and
	// the in-transaction flush doubles the counts.
	if n := s.blockProcessor.Follows().PendingDeltasLen(); n != 0 {
		t.Fatalf("pending follow deltas after batch failure = %d, want 0 (restored to batch-start snapshot)", n)
	}

	// Attempt 2 (the retry): the same range with block 3 fixed. The whole
	// batch re-processes from block 1 and every op lands.
	fixed := map[int64]map[string]interface{}{}
	for n, b := range blocks {
		fixed[n] = b
	}
	fixed[3] = syncBlockFixture(3, "2026-01-01T00:00:06", commentOp("alice", "p3"))
	s2, err := NewSync(cfg, database, txBatchTestProvider(fixed, 5))
	if err != nil {
		t.Fatalf("NewSync (retry): %v", err)
	}
	if err := s2.syncBlocks(ctx, 1, 5, false); err != nil {
		t.Fatalf("syncBlocks retry: %v", err)
	}

	if head := s2.headBlockNum(ctx); head != 5 {
		t.Fatalf("head after retry = %d, want 5", head)
	}
	if n := countWhere(t, database, "hive_posts", "1=1"); n != 4 {
		t.Fatalf("hive_posts after retry = %d, want 4 (p1, p3, p4, p5)", n)
	}
	alice := getAccount(t, database, ctx, "alice")
	bob := getAccount(t, database, ctx, "bob")
	if getFollowRow(t, database, ctx, alice.ID, bob.ID) == nil {
		t.Fatal("follow row missing after retry")
	}
	// The follow counts were applied by the IN-TRANSACTION flush of the
	// committed batch -- this test never calls Follow.Flush, so nonzero
	// counts here prove FlushTx rode the batch transaction (PR#404
	// registration: deltas are atomic with the block writes).
	if alice.Following != 1 || bob.Followers != 1 {
		t.Fatalf("counts after retry = alice.following %d / bob.followers %d, want 1/1 (in-transaction flush)", alice.Following, bob.Followers)
	}
	if n := s2.blockProcessor.Follows().PendingDeltasLen(); n != 0 {
		t.Fatalf("pending follow deltas after committed batch = %d, want 0 (flushed in tx)", n)
	}
	assertFollowCountsMatchRows(t, database)
	// The live-mode follow notification landed exactly once.
	if n := countWhere(t, database, "hive_notifs", "1=1"); n != 1 {
		t.Fatalf("hive_notifs after retry = %d, want 1 (the follow notif)", n)
	}
}

// TestTxBatchSizeOneDegenerate pins the HIVE_INDEXER_TX_BATCH_BLOCKS=1
// compatibility mode: every block is its own transaction (plus its own
// in-transaction follow flush), so a failure at block 3 leaves blocks 1-2
// COMMITTED and only the failing block retries.
func TestTxBatchSizeOneDegenerate(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob")

	followTuple := []interface{}{"custom_json_operation", customJSONOp("alice",
		`["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)}
	blocks := map[int64]map[string]interface{}{
		1: syncBlockFixture(1, "2026-01-01T00:00:00", commentOp("alice", "p1")),
		2: syncBlockFixture(2, "2026-01-01T00:00:03", followTuple),
		3: syncBlockFixture(3, "2026-01-01T00:00:06", poisonOrphanCommentOp("alice")),
		4: syncBlockFixture(4, "2026-01-01T00:00:09", commentOp("bob", "p4")),
	}

	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, TxBatchBlocks: 1}}
	s, err := NewSync(cfg, database, txBatchTestProvider(blocks, 4))
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}

	err = s.syncBlocks(ctx, 1, 4, false)
	var pbe *processBlockError
	if !errors.As(err, &pbe) || pbe.block != 3 {
		t.Fatalf("error = %v, want processBlockError for block 3", err)
	}
	// Degenerate per-block boundary: blocks 1-2 stay committed, block 3
	// rolled back alone, block 4 was never attempted.
	if head := s.headBlockNum(ctx); head != 2 {
		t.Fatalf("head after block-3 failure = %d, want 2 (per-block transactions)", head)
	}
	if n := countWhere(t, database, "hive_blocks", "num IN (3, 4)"); n != 0 {
		t.Fatalf("blocks 3-4 rows = %d, want 0", n)
	}
	// Block 2's follow committed with its own in-transaction flush.
	alice := getAccount(t, database, ctx, "alice")
	bob := getAccount(t, database, ctx, "bob")
	if alice.Following != 1 || bob.Followers != 1 {
		t.Fatalf("counts after block-2 commit = %d/%d, want 1/1 (per-block in-tx flush)", alice.Following, bob.Followers)
	}
	if n := s.blockProcessor.Follows().PendingDeltasLen(); n != 0 {
		t.Fatalf("pending deltas after per-block failure = %d, want 0", n)
	}

	// Retry from the failed block (the sync loop's head+1) completes 3-4.
	fixed := map[int64]map[string]interface{}{}
	for n, b := range blocks {
		fixed[n] = b
	}
	fixed[3] = syncBlockFixture(3, "2026-01-01T00:00:06", commentOp("alice", "p3"))
	s2, err := NewSync(cfg, database, txBatchTestProvider(fixed, 4))
	if err != nil {
		t.Fatalf("NewSync (retry): %v", err)
	}
	if err := s2.syncBlocks(ctx, 3, 4, false); err != nil {
		t.Fatalf("syncBlocks retry: %v", err)
	}
	if head := s2.headBlockNum(ctx); head != 4 {
		t.Fatalf("head after retry = %d, want 4", head)
	}
	if n := countWhere(t, database, "hive_posts", "1=1"); n != 3 {
		t.Fatalf("hive_posts after retry = %d, want 3", n)
	}
	assertFollowCountsMatchRows(t, database)
}

// TestTxBatchFollowDeltaConsistencyAcrossRetries drives repeated batch
// failures WITH follow ops in earlier blocks of the batch: every failed
// attempt must discard the whole batch's deltas, and the final successful
// attempt must apply them exactly once (a double-apply would fail the
// recount assertion -- the PR#406 audit P1 invariant at batch granularity).
func TestTxBatchFollowDeltaConsistencyAcrossRetries(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob", "carol")

	aliceFollowsBob := func(num int64, ts string) []interface{} {
		return []interface{}{"custom_json_operation", customJSONOp("alice",
			`["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)}
	}
	bobFollowsCarol := []interface{}{"custom_json_operation", customJSONOp("bob",
		`["follow",{"follower":"bob","following":"carol","what":["blog"]}]`)}

	poisonAt := int64(3)
	blocks := func(poison bool) map[int64]map[string]interface{} {
		out := map[int64]map[string]interface{}{
			1: syncBlockFixture(1, "2026-01-01T00:00:00", aliceFollowsBob(1, "2026-01-01T00:00:00")),
			2: syncBlockFixture(2, "2026-01-01T00:00:03", bobFollowsCarol),
			4: syncBlockFixture(4, "2026-01-01T00:00:09", commentOp("carol", "p4")),
		}
		if poison {
			out[3] = syncBlockFixture(3, "2026-01-01T00:00:06", poisonOrphanCommentOp("carol"))
		} else {
			out[3] = syncBlockFixture(3, "2026-01-01T00:00:06", commentOp("carol", "p3"))
		}
		return out
	}

	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, TxBatchBlocks: 4}}
	s, err := NewSync(cfg, database, txBatchTestProvider(blocks(true), 4))
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}

	// Two failing attempts: each rolls back the whole batch (both follow ops
	// included) and must leave zero pending deltas.
	for attempt := 1; attempt <= 2; attempt++ {
		if err := s.syncBlocks(ctx, 1, 4, false); err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded (block %d is poison)", attempt, poisonAt)
		}
		if n := s.blockProcessor.Follows().PendingDeltasLen(); n != 0 {
			t.Fatalf("pending deltas after failed attempt %d = %d, want 0", attempt, n)
		}
		if n := countWhere(t, database, "hive_follows", "1=1"); n != 0 {
			t.Fatalf("hive_follows rows after failed attempt %d = %d, want 0", attempt, n)
		}
		// The count columns were never touched: no flush ever committed.
		for _, name := range []string{"alice", "bob", "carol"} {
			acct := getAccount(t, database, ctx, name)
			if acct.Followers != 0 || acct.Following != 0 {
				t.Fatalf("%s counts = %d/%d after failed attempt %d, want 0/0", name, acct.Followers, acct.Following, attempt)
			}
		}
	}

	// Successful attempt (poison fixed): both follows land once, counts
	// equal the recount, no explicit Flush call anywhere in this test.
	s2, err := NewSync(cfg, database, txBatchTestProvider(blocks(false), 4))
	if err != nil {
		t.Fatalf("NewSync (retry): %v", err)
	}
	if err := s2.syncBlocks(ctx, 1, 4, false); err != nil {
		t.Fatalf("syncBlocks (final): %v", err)
	}
	if n := countWhere(t, database, "hive_follows", "1=1"); n != 2 {
		t.Fatalf("hive_follows rows = %d, want 2", n)
	}
	assertFollowCountsMatchRows(t, database)
	if n := s2.blockProcessor.Follows().PendingDeltasLen(); n != 0 {
		t.Fatalf("pending deltas after success = %d, want 0", n)
	}
}

// selectCountingLogger wraps the pool's gorm logger, counting SELECT
// statements that read hive_accounts (see the N+1 test -- the only reliable
// per-statement hook; raw-struct Scans bypass the Query callbacks).
type selectCountingLogger struct {
	logger.Interface
	selects *int
}

func (l selectCountingLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if sql, _ := fc(); strings.HasPrefix(sql, "SELECT") && strings.Contains(sql, "hive_accounts") {
		*l.selects++
	}
	l.Interface.Trace(ctx, begin, fc, err)
}

// TestAccountFlushBatchedIDResolutionEquivalence pins §4.3: the flush
// resolves every account id of a chunk in ONE query (no per-account SELECT
// on hive_accounts) and the resulting columns -- including rank, which the
// old per-account SELECT id fed -- are identical to the per-account-lookup
// semantics it replaced.
func TestAccountFlushBatchedIDResolutionEquivalence(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()
	seedAccounts(t, database, "alice", "bob", "carol")

	// Distinct vote weights give distinct ranks: alice=1, bob=2, carol=3.
	weights := map[string]float64{"alice": 300, "bob": 200, "carol": 100}
	for name, w := range weights {
		if err := database.DB.WithContext(ctx).
			Exec("UPDATE hive_accounts SET vote_weight = ? WHERE name = ?", w, name).Error; err != nil {
			t.Fatalf("seed vote weight %s: %v", name, err)
		}
	}
	// Load the rank cache up front so its lazy Reload does not pollute the
	// SELECT count below.
	sharedRankCache(database.DB).Reload(ctx)

	provider := &fakeSteemProvider{getAccounts: func(_ context.Context, names []string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(names))
		for i, n := range names {
			out[i] = steemdAccount(n)
		}
		return out, nil
	}}
	ai := NewAccountIndexer(repo, testLogger(), provider)
	ai.DirtySet([]string{"alice", "bob", "carol"})

	// Count SELECTs hitting hive_accounts during the flush via the gorm
	// logger's Trace hook (Scan-into-raw-struct statements do not traverse
	// the model Query callbacks, so a callback cannot see them). The batched
	// resolution is one; the old per-account path was one PER ACCOUNT (3).
	var selects int
	origLogger := database.DB.Logger
	database.DB.Logger = selectCountingLogger{origLogger, &selects}
	n, err := ai.FlushBatch(ctx, true)
	database.DB.Logger = origLogger
	if err != nil {
		t.Fatalf("FlushBatch: %v", err)
	}
	if n != 3 {
		t.Fatalf("FlushBatch processed %d, want 3", n)
	}
	if selects != 1 {
		t.Errorf("hive_accounts SELECTs during flush = %d, want 1 (batched id resolution; per-account lookups would issue 3)", selects)
	}

	// Column equivalence with the per-account-lookup semantics: rank comes
	// from the in-memory cache keyed by the SAME id the batched query
	// resolved, display_name from the steemd profile, post_count passthrough.
	for name, wantRank := range map[string]int64{"alice": 1, "bob": 2, "carol": 3} {
		acct := getAccount(t, database, ctx, name)
		if acct.Rank != wantRank {
			t.Errorf("%s.rank = %d, want %d (batched id resolution must feed the same rank)", name, acct.Rank, wantRank)
		}
		if !acct.DisplayName.Valid || acct.DisplayName.String != "Display Name" {
			t.Errorf("%s.display_name = %+v, want %q (profile sanitization unchanged)", name, acct.DisplayName, "Display Name")
		}
		if acct.PostCount != 42 {
			t.Errorf("%s.post_count = %d, want 42", name, acct.PostCount)
		}
	}
}

// TestPerfSmokeTxBatchOverhead is the §4.1 performance smoke: the same N
// empty blocks through syncBlocks with a 1-block tx batch vs a full-batch
// tx, reporting wall time and blocks/s for both. It proves the batch
// transaction is really one COMMIT per batch (the numbers go into the PR
// body). Skipped unless HIVE_PERF_SMOKE=1 -- wall-clock numbers have no
// place in CI assertions.
func TestPerfSmokeTxBatchOverhead(t *testing.T) {
	if os.Getenv("HIVE_PERF_SMOKE") == "" {
		t.Skip("set HIVE_PERF_SMOKE=1 to run the tx-batching performance smoke (timings are reported, not asserted)")
	}
	const n = 2000
	buildBlocks := func() map[int64]map[string]interface{} {
		out := make(map[int64]map[string]interface{}, n)
		base := time.Date(2016, 3, 24, 16, 0, 0, 0, time.UTC)
		for i := 1; i <= n; i++ {
			out[int64(i)] = syncBlockFixture(int64(i), base.Add(time.Duration(3*(i-1))*time.Second).Format("2006-01-02T15:04:05"))
		}
		return out
	}

	for _, txBatch := range []int{1, n} {
		t.Run(map[int]string{1: "tx_batch_1", n: "tx_batch_full"}[txBatch], func(t *testing.T) {
			database, _, cleanup := withMigratedDB(t)
			defer cleanup()
			ctx := context.Background()

			cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, TxBatchBlocks: txBatch}}
			s, err := NewSync(cfg, database, txBatchTestProvider(buildBlocks(), n))
			if err != nil {
				t.Fatalf("NewSync: %v", err)
			}
			start := time.Now()
			if err := s.syncBlocks(ctx, 1, n, true); err != nil {
				t.Fatalf("syncBlocks: %v", err)
			}
			dur := time.Since(start)
			if head := s.headBlockNum(ctx); head != n {
				t.Fatalf("head = %d, want %d", head, n)
			}
			t.Logf("tx_batch=%d: %d blocks in %v => %.0f blocks/s (write-side overall, empty blocks, initial-sync semantics)",
				txBatch, n, dur, float64(n)/dur.Seconds())
		})
	}
}
