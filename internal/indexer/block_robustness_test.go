package indexer

// Block-processing robustness tests (planned PR#404, KR3 W3; hardened in the
// PR#406 review round):
//   - P1-1: op error classification -- data errors skip the op (block
//     commits), infrastructure errors fail the whole block (transaction
//     rolls back; the sync layer retries it and no op lost);
//   - P1-1: follow count deltas accumulate in FollowIndexer memory OUTSIDE
//     the block transaction; a rolled-back block must also restore them or
//     the retry doubles the counts (SnapshotDeltas wiring);
//   - P1-2: payment validation mirrors legacy Payments._validated exactly
//     (SBD-only, @author/permlink memo, existing post) so ordinary chain
//     data can never produce a post_id=0 payment row (FK poison block);
//   - P1-3: same-block account registration runs BEFORE custom_json ops;
//   - P1-15: block read-modify-write sequences ride the block transaction;
//   - 2.10: notifications written during block processing ride the block
//     transaction -- a rollback leaves no residue in hive_notifs.
//
// Failure injection (review round): the rollback tests cancel the block
// context from GORM callbacks (cancelAfterNotif / cancelBeforeAccountUpdate)
// at a precise point INSIDE the custom-ops phase, AFTER earlier ops already
// wrote rows into the open transaction -- the "write then fail" shape the
// first version of these tests never exercised (its injection fired in the
// op loop, before registration/custom-ops/notifications ever ran, so the
// "no residue" assertions were hollow).

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// TestIsInfraError pins the P1-1 classifier: only errors carrying an
// opValidationError (the data-error marker introduced with community ops in
// PR#403) are data errors; everything else -- including wrapped DB errors
// and context cancellation -- is infrastructure and must fail the block.
func TestIsInfraError(t *testing.T) {
	dbErr := fmt.Errorf("failed to check existing follow: %w", gorm.ErrInvalidDB)
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not infra", nil, false},
		{"opValidationError is data", opValidationErr("invalid action"), false},
		{"wrapped opValidationError is data", fmt.Errorf("follow op: %w", opValidationErr("cannot follow self")), false},
		{"plain error is infra", fmt.Errorf("failed to create follow: duplicate"), true},
		{"gorm error is infra", dbErr, true},
		{"context canceled is infra", context.Canceled, true},
		{"context deadline is infra", fmt.Errorf("op: %w", context.DeadlineExceeded), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsInfraError(c.err); got != c.want {
				t.Fatalf("IsInfraError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// robustnessBlock builds a block map with explicit transactions. Each tx
// argument is the raw [type, value] operation tuple list of one transaction.
func robustnessBlock(num int64, ts string, trxIDs []string, txs ...[]interface{}) map[string]interface{} {
	transactions := make([]interface{}, 0, len(txs))
	for _, ops := range txs {
		transactions = append(transactions, map[string]interface{}{"operations": ops})
	}
	ids := make([]interface{}, 0, len(trxIDs))
	for _, id := range trxIDs {
		ids = append(ids, id)
	}
	return map[string]interface{}{
		"block_num":       float64(num),
		"block_id":        fmt.Sprintf("%040x", num),
		"previous":        genesisHash,
		"timestamp":       ts,
		"transactions":    transactions,
		"transaction_ids": ids,
	}
}

func countWhere(t *testing.T, database *db.DB, table, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := database.DB.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// cancelAfterNotif is a deterministic mid-block failure injector: registered
// as a GORM create-callback, it ARMS when an INSERT into armTable completes
// (e.g. hive_follows -- the follow op's main write) and cancels the block
// context right AFTER the NEXT INSERT into hive_notifs completes (that op's
// notification). The arming op is therefore fully written into the open
// transaction, and the FOLLOWING op in the same ProcessOps batch fails on its
// first query with context.Canceled -- the infrastructure class. Single-shot
// (done), so reprocessing the block with a fresh context is unaffected.
type cancelAfterNotif struct {
	cancel   func()
	armTable string
	armed    bool
	done     bool
}

func (h *cancelAfterNotif) register(gdb *gorm.DB) {
	gdb.Callback().Create().After("gorm:create").Register("test:cancel-after-notif", func(tx *gorm.DB) {
		if h.done || tx.Statement == nil || tx.Statement.Schema == nil {
			return
		}
		switch tx.Statement.Schema.Table {
		case h.armTable:
			h.armed = true
		case "hive_notifs":
			if h.armed {
				h.done = true
				h.cancel()
			}
		}
	})
}

// cancelBeforeAccountUpdate cancels the context immediately BEFORE the n-th
// (1-based) UPDATE of hive_accounts executes, failing that statement itself
// with context.Canceled while the earlier account updates stay in the open
// transaction ("write then fail" for the setLastRead path).
type cancelBeforeAccountUpdate struct {
	cancel func()
	n      int
	seen   int
	fired  bool
}

func (h *cancelBeforeAccountUpdate) register(gdb *gorm.DB) {
	gdb.Callback().Update().Before("gorm:update").Register("test:cancel-before-acct-update", func(tx *gorm.DB) {
		if h.fired || tx.Statement == nil || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "hive_accounts" {
			return
		}
		h.seen++
		if h.seen == h.n {
			h.fired = true
			h.cancel()
		}
	})
}

// assertFollowCountsMatchRows checks the PR#406 audit P1-1 acceptance
// invariant: every account's followers/following columns equal the recount
// from hive_follows (state IN (1,3)) -- exactly what ForceRecount computes.
// A block whose rolled-back follow deltas were re-applied by its retry would
// double the columns and fail here.
func assertFollowCountsMatchRows(t *testing.T, database *db.DB) {
	t.Helper()
	var accounts []models.Account
	if err := database.DB.Find(&accounts).Error; err != nil {
		t.Fatalf("load accounts: %v", err)
	}
	if len(accounts) == 0 {
		t.Fatal("no accounts to check")
	}
	for _, acct := range accounts {
		var followers, following int64
		if err := database.DB.Model(&models.Follow{}).
			Where("following = ? AND state IN (1, 3)", acct.ID).
			Count(&followers).Error; err != nil {
			t.Fatalf("count followers of %s: %v", acct.Name, err)
		}
		if err := database.DB.Model(&models.Follow{}).
			Where("follower = ? AND state IN (1, 3)", acct.ID).
			Count(&following).Error; err != nil {
			t.Fatalf("count following of %s: %v", acct.Name, err)
		}
		if acct.Followers != followers {
			t.Errorf("%s.followers = %d, want %d (recount from hive_follows)", acct.Name, acct.Followers, followers)
		}
		if acct.Following != following {
			t.Errorf("%s.following = %d, want %d (recount from hive_follows)", acct.Name, acct.Following, following)
		}
	}
}

// TestInfraErrorFailsBlockRetrySucceeds pins the P1-1 acceptance: a block
// whose processing hits an infrastructure error fails whole (nothing
// partially applied), and reprocessing the identical block after the
// transient condition clears succeeds with every op landed -- the sync
// loop's retry semantics (syncIrreversible re-processes from hive_blocks
// head after its 3s backoff). The pre-cancelled context here fails at the
// outermost point (transaction acquisition); the mid-block in-transaction
// rollbacks are exercised by the cancelAfterNotif tests below. After the
// retry the follow count deltas are flushed and the counts must equal the
// recount from hive_follows (audit P1-1: no doubled deltas).
func TestInfraErrorFailsBlockRetrySucceeds(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	seedAccounts(t, database, "alice", "bob")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	const blockNum = int64(16000000)
	block := robustnessBlock(blockNum, "2017-01-01T00:00:00",
		[]string{"trx-id-1", "trx-id-2"},
		[]interface{}{
			[]interface{}{"account_create_operation", map[string]interface{}{"creator": "steem", "new_account_name": "guthran"}},
			[]interface{}{"custom_json_operation", customJSONOp("guthran", `["follow",{"follower":"guthran","following":"alice","what":["blog"]}]`)},
		},
		[]interface{}{
			[]interface{}{"comment_operation", map[string]interface{}{
				"author": "bob", "permlink": "first-post",
				"title": "t", "body": "b",
				"parent_author": "", "parent_permlink": "steem",
				"json_metadata": "",
			}},
		},
	)

	// Attempt 1: transient infrastructure failure (cancelled context --
	// the same class the DB pool surfaces on connection loss / timeout).
	ctxFail, cancel := context.WithCancel(context.Background())
	cancel()
	err := bp.ProcessBlock(ctxFail, block, false)
	if err == nil {
		t.Fatal("ProcessBlock with cancelled ctx must fail (infra error)")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap context.Canceled, got: %v", err)
	}
	if n := countWhere(t, database, "hive_blocks", "num = ?", blockNum); n != 0 {
		t.Fatalf("hive_blocks rows after failed attempt = %d, want 0 (nothing committed)", n)
	}
	if n := countWhere(t, database, "hive_accounts", "name = ?", "guthran"); n != 0 {
		t.Fatalf("guthran registered after failed attempt (partial application)")
	}
	if n := countWhere(t, database, "hive_follows", "1=1"); n != 0 {
		t.Fatalf("hive_follows rows after failed attempt = %d, want 0", n)
	}
	if n := countWhere(t, database, "hive_posts", "author = ?", "bob"); n != 0 {
		t.Fatalf("hive_posts rows after failed attempt = %d, want 0", n)
	}
	if n := countWhere(t, database, "hive_trxid_block_num", "block_num = ?", blockNum); n != 0 {
		t.Fatalf("trxid rows after failed attempt = %d, want 0", n)
	}

	// Attempt 2 (the retry): the identical block succeeds and every op is
	// present -- no op lost to the first failure.
	if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
		t.Fatalf("ProcessBlock retry: %v", err)
	}
	if n := countWhere(t, database, "hive_blocks", "num = ?", blockNum); n != 1 {
		t.Fatalf("hive_blocks rows after retry = %d, want 1", n)
	}
	if n := countWhere(t, database, "hive_accounts", "name = ?", "guthran"); n != 1 {
		t.Fatalf("guthran registration lost after retry")
	}
	if n := countWhere(t, database, "hive_follows", "1=1"); n != 1 {
		t.Fatalf("hive_follows rows after retry = %d, want 1 (guthran -> alice)", n)
	}
	if n := countWhere(t, database, "hive_posts", "author = ?", "bob"); n != 1 {
		t.Fatalf("hive_posts rows after retry = %d, want 1", n)
	}
	if n := countWhere(t, database, "hive_trxid_block_num", "block_num = ?", blockNum); n != 2 {
		t.Fatalf("trxid rows after retry = %d, want 2", n)
	}
	// The 0->1 follow transition wrote its notification (live mode).
	if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeFollow); n != 1 {
		t.Fatalf("follow notifs after retry = %d, want 1", n)
	}

	// Audit P1-1: the failed attempt left no pending deltas (it never
	// entered the transaction); the retry accumulates them exactly once,
	// the flush applies them, and the count columns must equal the recount
	// from hive_follows. (The mid-block variant below proves the restore on
	// rollback.)
	if n := bp.Follows().PendingDeltasLen(); n != 2 {
		t.Fatalf("pending follow deltas after retry = %d, want 2 (one follower + one following entry)", n)
	}
	if err := bp.Follows().Flush(context.Background()); err != nil {
		t.Fatalf("flush follow deltas: %v", err)
	}
	if n := bp.Follows().PendingDeltasLen(); n != 0 {
		t.Fatalf("pending follow deltas after flush = %d, want 0", n)
	}
	assertFollowCountsMatchRows(t, database)
}

// TestDataErrorSkipsOpBlockCommits pins the other half of P1-1: a
// deterministic chain-data failure (self-follow) is skipped with a Warn
// while the rest of the block commits.
func TestDataErrorSkipsOpBlockCommits(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	seedAccounts(t, database, "alice", "bob")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	const blockNum = int64(16000000)
	block := robustnessBlock(blockNum, "2017-01-01T00:00:00", []string{"trx-1"},
		[]interface{}{
			// Data error: legacy rejects self-follows; the op is skipped.
			[]interface{}{"custom_json_operation", customJSONOp("alice", `["follow",{"follower":"alice","following":"alice","what":["blog"]}]`)},
			// Valid op in the same block must still land.
			[]interface{}{"custom_json_operation", customJSONOp("alice", `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)},
		},
	)

	if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
		t.Fatalf("data errors must not fail the block: %v", err)
	}
	if n := countWhere(t, database, "hive_blocks", "num = ?", blockNum); n != 1 {
		t.Fatalf("hive_blocks rows = %d, want 1 (block committed)", n)
	}
	var follows []models.Follow
	if err := database.DB.Find(&follows).Error; err != nil {
		t.Fatalf("load follows: %v", err)
	}
	if len(follows) != 1 {
		t.Fatalf("hive_follows rows = %d, want 1 (self-follow skipped, valid follow landed)", len(follows))
	}
	if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeFollow); n != 1 {
		t.Fatalf("follow notifs = %d, want 1 (only the valid follow)", n)
	}
}

// Golden payloads for TestRegisterBeforeCustomOpsSameBlockFollow, fetched
// 2026-10-14 from api.steemit.com (condenser_api.get_block):
//   - the account_create value is steem block 9,000,078 verbatim except the
//     bulky key-auth objects the indexer never reads ("steem" creates
//     "guthran" for 35.000 STEEM);
//   - the follow envelope is a canonical on-chain follow-plugin shape
//     (["follow",{"follower":"...","following":"cathynsons","what":["blog"]}]),
//     matching the byte-verified goldens captured in custom_op_follow_test.go
//     (block 14,000,000 family), with the follower swapped to the
//     same-block-created account. A block in which the newly created
//     account itself follows someone was not found in a 3,000-block scan
//     (mined/created accounts rarely act inside their creation block), so
//     the two real payloads are combined here.
var goldenAccountCreateValue = map[string]interface{}{
	"creator":          "steem",
	"fee":              "35.000 STEEM",
	"new_account_name": "guthran",
	"json_metadata":    "",
	"memo_key":         "STM82thKnytDjPaQ1nqxjB2NG3YY2LAaPeK2vLZMp1z6cshq7epep",
}

const goldenSameBlockFollowJSON = `["follow",{"follower":"guthran","following":"good-karma","what":["blog"]}]`

// TestRegisterBeforeCustomOpsSameBlockFollow pins the P1-3 registration
// order against legacy blocks.py (Accounts.register runs before
// CustomOp.process_ops): an account created by account_create in a block
// can be the follower of a custom_json follow op in that same block. Before
// the fix, the follow was skipped ("follower account not found") because
// custom ops ran before registration and pool-bound reads could not see the
// uncommitted account row.
func TestRegisterBeforeCustomOpsSameBlockFollow(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	seedAccounts(t, database, "good-karma")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	block := robustnessBlock(9000078, "2017-01-07T04:11:51", []string{"trx-1"},
		[]interface{}{
			[]interface{}{"account_create_operation", goldenAccountCreateValue},
			[]interface{}{"custom_json_operation", customJSONOp("guthran", goldenSameBlockFollowJSON)},
		},
	)

	if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}

	guthran := getAccount(t, database, context.Background(), "guthran")
	goodKarma := getAccount(t, database, context.Background(), "good-karma")
	row := getFollowRow(t, database, context.Background(), guthran.ID, goodKarma.ID)
	if row == nil {
		t.Fatal("same-block account_create + follow must land the follow row (register before custom ops)")
	}
	if row.State != models.FollowStateBlog {
		t.Errorf("follow state = %d, want %d", row.State, models.FollowStateBlog)
	}
	// The follow notification (live mode) also rode the block transaction.
	if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeFollow); n != 1 {
		t.Fatalf("follow notifs = %d, want 1", n)
	}
}

// TestBlockRollbackLeavesNoNotifyResidue pins 2.10: notifications written
// while processing a block ride the block transaction, so when a LATER op in
// the same block fails with an infrastructure error the rollback leaves no
// notification (or any other block write) behind. Review-round hardening:
// the failure is injected mid-custom-ops-phase with cancelAfterNotif, i.e.
// AFTER the first op already wrote its follow/community/notification rows
// into the open transaction -- the first version of this test failed in the
// op loop, before custom ops ever ran, so these assertions never had a
// residue to check (hollow assertions).
func TestBlockRollbackLeavesNoNotifyResidue(t *testing.T) {
	t.Run("follow notif", func(t *testing.T) {
		database, repo, cleanup := withMigratedDB(t)
		defer cleanup()

		seedAccounts(t, database, "alice", "bob")
		bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

		const blockNum = int64(16000000)
		block := robustnessBlock(blockNum, "2017-01-01T00:00:00", []string{"trx-1"},
			[]interface{}{
				// Op 1: alice follows bob -- writes the follow row AND its
				// notification inside the tx.
				[]interface{}{"custom_json_operation", customJSONOp("alice", `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)},
				// Op 2: bob follows alice -- its first query fails with
				// context.Canceled once the hook cancels the block ctx.
				[]interface{}{"custom_json_operation", customJSONOp("bob", `["follow",{"follower":"bob","following":"alice","what":["blog"]}]`)},
			},
		)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hook := &cancelAfterNotif{cancel: cancel, armTable: "hive_follows"}
		hook.register(database.DB)

		err := bp.ProcessBlock(ctx, block, false)
		if err == nil {
			t.Fatal("mid-custom-ops infra error must fail the block")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error should wrap context.Canceled, got: %v", err)
		}
		// Op 1's follow row and notification WERE written into the tx before
		// the failure; the rollback must still leave nothing behind. (The
		// genesis block is schema-seeded; only this block's row must be
		// absent.)
		if n := countWhere(t, database, "hive_blocks", "num = ?", blockNum); n != 0 {
			t.Errorf("hive_blocks rows after rollback = %d, want 0 (no residue)", n)
		}
		for table := range map[string]struct{}{"hive_notifs": {}, "hive_follows": {}} {
			if n := countWhere(t, database, table, "1=1"); n != 0 {
				t.Errorf("%s rows after rollback = %d, want 0 (no residue)", table, n)
			}
		}

		// The retry (sync re-processes the identical block with a fresh
		// context): both follows and both notifications land, and the count
		// columns match the follow rows after the delta flush.
		if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
			t.Fatalf("ProcessBlock retry: %v", err)
		}
		if n := countWhere(t, database, "hive_follows", "1=1"); n != 2 {
			t.Fatalf("hive_follows rows after retry = %d, want 2", n)
		}
		if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeFollow); n != 2 {
			t.Fatalf("follow notifs after retry = %d, want 2", n)
		}
		if err := bp.Follows().Flush(context.Background()); err != nil {
			t.Fatalf("flush follow deltas: %v", err)
		}
		assertFollowCountsMatchRows(t, database)
	})

	t.Run("community register and subscribe notifs", func(t *testing.T) {
		database, repo, cleanup := withMigratedDB(t)
		defer cleanup()

		seedAccounts(t, database, "alice", "bob")
		bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

		// Post-START_BLOCK/START_DATE block (community ops gated on
		// blockNum > START_BLOCK; community registration gated on
		// blockDate > START_DATE).
		const blockNum = int64(38000000)
		block := robustnessBlock(blockNum, "2019-11-01T00:00:00", []string{"trx-1"},
			[]interface{}{
				// Registers hive-174017 as a community (owner role +
				// new_community notification, all inside the tx).
				[]interface{}{"account_create_operation", map[string]interface{}{"creator": "steem", "new_account_name": "hive-174017"}},
				// Op 1: alice subscribes -- subscription row, subscribers
				// bump and subscribe notification written inside the tx.
				[]interface{}{"custom_json_operation", customJSONOpWithID("community", "alice", `["subscribe",{"community":"hive-174017"}]`)},
				// Op 2: bob subscribes -- fails with context.Canceled after
				// op 1's writes (the hook cancels after the subscribe notif).
				[]interface{}{"custom_json_operation", customJSONOpWithID("community", "bob", `["subscribe",{"community":"hive-174017"}]`)},
			},
		)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		hook := &cancelAfterNotif{cancel: cancel, armTable: "hive_subscriptions"}
		hook.register(database.DB)

		if err := bp.ProcessBlock(ctx, block, false); err == nil {
			t.Fatal("mid-custom-ops infra error must fail the block")
		}
		// The community registration, subscription, subscriber count and
		// every notification were written in-transaction before the failure;
		// the rollback must leave all of them empty.
		if n := countWhere(t, database, "hive_blocks", "num = ?", blockNum); n != 0 {
			t.Errorf("hive_blocks rows after rollback = %d, want 0 (no residue)", n)
		}
		for table := range map[string]struct{}{
			"hive_notifs": {}, "hive_communities": {},
			"hive_roles": {}, "hive_subscriptions": {},
		} {
			if n := countWhere(t, database, table, "1=1"); n != 0 {
				t.Errorf("%s rows after rollback = %d, want 0 (no residue)", table, n)
			}
		}
		if n := countWhere(t, database, "hive_accounts", "name = ?", "hive-174017"); n != 0 {
			t.Errorf("hive-174017 account after rollback = %d, want 0 (no residue)", n)
		}

		// The retry: community, both subscriptions and all notifications
		// land; subscribers equals the subscription rows (count check).
		if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
			t.Fatalf("ProcessBlock retry: %v", err)
		}
		var community models.Community
		if err := database.DB.Where("name = ?", "hive-174017").First(&community).Error; err != nil {
			t.Fatalf("load community after retry: %v", err)
		}
		if n := countWhere(t, database, "hive_subscriptions", "community_id = ?", community.ID); n != 2 {
			t.Fatalf("subscriptions after retry = %d, want 2", n)
		}
		if community.Subscribers != 2 {
			t.Errorf("community.subscribers = %d, want 2 (== subscription rows)", community.Subscribers)
		}
		if n := countWhere(t, database, "hive_notifs", "1=1"); n != 3 {
			t.Fatalf("notifs after retry = %d, want 3 (new_community + 2 subscribe)", n)
		}
	})
}

// TestSetLastReadRidesBlockTransaction pins the P1-15 tx channel for the
// notify plugin's setLastRead write: the hive_accounts.lastread_at update
// executes on the block transaction, so a later failure rolls it back too.
// Review-round hardening: the failure is injected AFTER alice's setLastRead
// write (the second hive_accounts update -- bob's -- fails with a cancelled
// context via cancelBeforeAccountUpdate), so the rollback genuinely has an
// in-transaction write to undo; the first version failed in the op loop and
// never reached the setLastRead write at all.
func TestSetLastReadRidesBlockTransaction(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	seedAccounts(t, database, "alice", "bob")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	setLastReadOp := func() map[string]interface{} {
		return map[string]interface{}{
			"id":                     "notify",
			"required_auths":         []interface{}{},
			"required_posting_auths": []interface{}{"AUTH"},
			"json":                   `["setLastRead",{"date":"2016-12-31T23:59:59"}]`,
		}
	}
	aliceOp := setLastReadOp()
	aliceOp["required_posting_auths"] = []interface{}{"alice"}
	bobOp := setLastReadOp()
	bobOp["required_posting_auths"] = []interface{}{"bob"}

	const blockNum = int64(16000000)
	block := robustnessBlock(blockNum, "2017-01-01T00:00:00", []string{"trx-1"},
		[]interface{}{
			[]interface{}{"custom_json_operation", aliceOp},
			// Bob's setLastRead UPDATE is the 2nd hive_accounts update and
			// fails with context.Canceled (infra) -- after alice's write.
			[]interface{}{"custom_json_operation", bobOp},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook := &cancelBeforeAccountUpdate{cancel: cancel, n: 2}
	hook.register(database.DB)

	if err := bp.ProcessBlock(ctx, block, false); err == nil {
		t.Fatal("mid-custom-ops infra error must fail the block")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("error should wrap context.Canceled, got: %v", err)
	}
	// lastread_at is NOT NULL DEFAULT '1970-01-01'; the rolled-back write
	// must leave BOTH accounts at the epoch default (alice's update was in
	// the transaction; bob's never executed).
	for _, name := range []string{"alice", "bob"} {
		acct := getAccount(t, database, context.Background(), name)
		if !acct.LastreadAt.Equal(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("%s lastread_at = %v after rollback, want epoch default (write rode the block tx)", name, acct.LastreadAt)
		}
	}

	// The identical block with a fresh context commits both updates.
	if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
		t.Fatalf("ProcessBlock retry: %v", err)
	}
	for _, name := range []string{"alice", "bob"} {
		acct := getAccount(t, database, context.Background(), name)
		if !acct.LastreadAt.Equal(time.Date(2016, 12, 31, 23, 59, 59, 0, time.UTC)) {
			t.Fatalf("%s lastread_at = %v after commit, want 2016-12-31T23:59:59", name, acct.LastreadAt)
		}
	}
}

// TestReblogDuplicateSkippedOtherFailuresPropagate pins the PR#406 audit
// P2-7 classification of reblog create failures: a duplicate (account,
// post) pair keeps the legacy ON CONFLICT DO NOTHING semantics (silent
// skip, block commits, no second feed-cache row), while a non-duplicate DB
// failure must propagate as an infrastructure error. The duplicate is the
// on-chain double-reblog shape; the propagated class is exercised by every
// other canceled-context test in this file.
func TestReblogDuplicateSkippedOtherFailuresPropagate(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob")
	post := &models.Post{
		Author:    "bob",
		Permlink:  "hello-world",
		CreatedAt: time.Unix(1458845200, 0).UTC(),
		Depth:     0,
		IsValid:   true,
	}
	if err := database.DB.WithContext(ctx).Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	reblogBlock := func(num int64) map[string]interface{} {
		return robustnessBlock(num, "2017-01-01T00:00:00", []string{"trx-1"},
			[]interface{}{
				[]interface{}{"custom_json_operation", customJSONOp("alice",
					`["reblog",{"account":"alice","author":"bob","permlink":"hello-world"}]`)},
			},
		)
	}

	// First reblog lands.
	if err := bp.ProcessBlock(ctx, reblogBlock(16000000), false); err != nil {
		t.Fatalf("first reblog: %v", err)
	}
	// The same reblog again (a later block): the unique violation on
	// hive_reblogs_ux1 must be skipped like legacy ON CONFLICT DO NOTHING,
	// not fail the block.
	if err := bp.ProcessBlock(ctx, reblogBlock(16000001), false); err != nil {
		t.Fatalf("duplicate reblog must be skipped, got: %v", err)
	}
	if n := countWhere(t, database, "hive_reblogs", "1=1"); n != 1 {
		t.Errorf("hive_reblogs rows = %d, want 1 (duplicate skipped)", n)
	}
	if n := countWhere(t, database, "hive_feed_cache", "post_id = ?", post.ID); n != 1 {
		t.Errorf("hive_feed_cache rows = %d, want 1 (duplicate skipped before feed insert)", n)
	}
}

// TestSameBlockPaymentSeesSameBlockPost pins the P1-15 read channel for the
// payment path: a transfer whose memo points at a post created earlier in
// the SAME block resolves the post through the block transaction and
// increments its promoted amount (legacy reads within its process_multi
// transaction). With pool-bound reads the lookup would miss the
// uncommitted post.
func TestSameBlockPaymentSeesSameBlockPost(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	// 'null' is one of the accounts the schema migration seeds (miners,
	// null, temp, initminer -- production shape), so only alice is seeded.
	seedAccounts(t, database, "alice")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	block := robustnessBlock(16000000, "2017-01-01T00:00:00", []string{"trx-1"},
		[]interface{}{
			[]interface{}{"comment_operation", map[string]interface{}{
				"author": "alice", "permlink": "promote-me",
				"title": "t", "body": "b",
				"parent_author": "", "parent_permlink": "life",
				"json_metadata": "",
			}},
			[]interface{}{"transfer_operation", map[string]interface{}{
				"from": "alice", "to": "null",
				"amount": "5.000 SBD", "memo": "@alice/promote-me",
			}},
		},
	)

	if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}

	var post models.Post
	if err := database.DB.Where("author = ? AND permlink = ?", "alice", "promote-me").
		First(&post).Error; err != nil {
		t.Fatalf("load post: %v", err)
	}
	if post.Promoted != 5.0 {
		t.Errorf("promoted = %v, want 5 (payment saw the same-block post through the tx)", post.Promoted)
	}
	if n := countWhere(t, database, "hive_payments", "1=1"); n != 1 {
		t.Fatalf("hive_payments rows = %d, want 1", n)
	}
}

// TestBlockRollbackRestoresPendingFollowDeltas pins the PR#406 audit P1-1
// core: follow count deltas accumulate in FollowIndexer memory OUTSIDE the
// block transaction, so when a block fails and rolls back mid-custom-ops
// (AFTER a follow op already accumulated its deltas), the pending deltas
// must be restored to the pre-block snapshot -- otherwise the sync layer's
// retry of the identical block re-accumulates them and the next Flush
// doubles hive_accounts.followers/following. Verified end to end: pending
// count is zero right after the failed attempt, and after the retry + Flush
// the count columns equal the recount from hive_follows.
func TestBlockRollbackRestoresPendingFollowDeltas(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	seedAccounts(t, database, "alice", "bob")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	const blockNum = int64(16000000)
	block := robustnessBlock(blockNum, "2017-01-01T00:00:00", []string{"trx-1"},
		[]interface{}{
			// Op 1 fully applies: follow row + notif + FOUR pending delta
			// entries (alice following, bob followed; one per map).
			[]interface{}{"custom_json_operation", customJSONOp("alice", `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)},
			// Op 2 fails with context.Canceled after op 1 (hook cancels the
			// ctx right after op 1's notification insert).
			[]interface{}{"custom_json_operation", customJSONOp("bob", `["follow",{"follower":"bob","following":"alice","what":["blog"]}]`)},
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook := &cancelAfterNotif{cancel: cancel, armTable: "hive_follows"}
	hook.register(database.DB)

	if err := bp.ProcessBlock(ctx, block, false); err == nil {
		t.Fatal("mid-custom-ops infra error must fail the block")
	}
	// The block rolled back: op 1's follow row is gone and, critically, its
	// in-memory deltas must be gone too (restored to the pre-block
	// snapshot). Without the restore this is 4 and the retry doubles it.
	if n := bp.Follows().PendingDeltasLen(); n != 0 {
		t.Fatalf("pending follow deltas after rolled-back block = %d, want 0 (deltas must roll back with the block)", n)
	}

	// The retry: both follows land, the deltas are re-accumulated exactly
	// once, and the flushed counts equal the recount from hive_follows.
	if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
		t.Fatalf("ProcessBlock retry: %v", err)
	}
	if n := countWhere(t, database, "hive_follows", "1=1"); n != 2 {
		t.Fatalf("hive_follows rows after retry = %d, want 2", n)
	}
	if err := bp.Follows().Flush(context.Background()); err != nil {
		t.Fatalf("flush follow deltas: %v", err)
	}
	alice := getAccount(t, database, context.Background(), "alice")
	bob := getAccount(t, database, context.Background(), "bob")
	// alice follows bob; bob follows alice: both have 1 follower / 1 following.
	if alice.Followers != 1 || alice.Following != 1 {
		t.Errorf("alice counts = %d/%d, want 1/1 (doubled deltas would read 2)", alice.Followers, alice.Following)
	}
	if bob.Followers != 1 || bob.Following != 1 {
		t.Errorf("bob counts = %d/%d, want 1/1 (doubled deltas would read 2)", bob.Followers, bob.Following)
	}
	assertFollowCountsMatchRows(t, database)
}

// TestSameBlockCommunityCreateAndSubscribeLands pins the P2-5 SUCCESS path
// of the same-block community combination (the rollback subtest above only
// ever exercised its failure): an account_create of a hive-XXXXXX name
// registers the community (row + owner role + new_community notif) and a
// subscribe custom_json op in the SAME block lands the subscription row,
// the subscribers count and the subscribe notification.
func TestSameBlockCommunityCreateAndSubscribeLands(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	seedAccounts(t, database, "alice")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	const blockNum = int64(38000000)
	block := robustnessBlock(blockNum, "2019-11-01T00:00:00", []string{"trx-1"},
		[]interface{}{
			[]interface{}{"account_create_operation", map[string]interface{}{"creator": "steem", "new_account_name": "hive-174017"}},
			[]interface{}{"custom_json_operation", customJSONOpWithID("community", "alice", `["subscribe",{"community":"hive-174017"}]`)},
		},
	)

	if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}

	var community models.Community
	if err := database.DB.Where("name = ?", "hive-174017").First(&community).Error; err != nil {
		t.Fatalf("community row missing after same-block create+subscribe: %v", err)
	}
	if n := countWhere(t, database, "hive_roles", "community_id = ? AND role_id = ?", community.ID, models.RoleOwner); n != 1 {
		t.Fatalf("owner role rows = %d, want 1", n)
	}
	alice := getAccount(t, database, context.Background(), "alice")
	if n := countWhere(t, database, "hive_subscriptions", "community_id = ? AND account_id = ?", community.ID, alice.ID); n != 1 {
		t.Fatalf("alice subscription rows = %d, want 1", n)
	}
	if community.Subscribers != 1 {
		t.Errorf("community.subscribers = %d, want 1 (== subscription rows)", community.Subscribers)
	}
	if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeNewCommunity); n != 1 {
		t.Errorf("new_community notifs = %d, want 1", n)
	}
	if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeSubscribe); n != 1 {
		t.Errorf("subscribe notifs = %d, want 1", n)
	}
}

// TestPaymentTransferValidationLegacySkips pins the PR#406 audit P1-2
// fix: ProcessTransfer must mirror legacy Payments._validated exactly, so
// the routine chain-data cases (STEEM burns, plain-text/empty/malformed
// memos, unknown posts or authors, unregistered senders) SKIP the record --
// no hive_payments row is written. Before the fix these produced a payment
// row with post_id 0, violating hive_payments_fk3; the error is
// infrastructure-class, so the block retried forever (poison block) on
// perfectly ordinary data.
func TestPaymentTransferValidationLegacySkips(t *testing.T) {
	cases := []struct {
		name        string
		from        string
		amount      string
		memo        string
		seedPost    bool
		wantRows    int64
		wantPromote float64
	}{
		{
			name: "SBD with valid memo records payment and promotion",
			from: "alice", amount: "5.000 SBD", memo: "@alice/promote-me",
			seedPost: true, wantRows: 1, wantPromote: 5,
		},
		{
			name: "STEEM burn skipped entirely (legacy token gate)",
			from: "alice", amount: "5.000 STEEM", memo: "@alice/promote-me",
			seedPost: true, wantRows: 0, wantPromote: 0,
		},
		{
			name: "plain-text memo skipped (no payment row)",
			from: "alice", amount: "5.000 SBD", memo: "thanks for the fish",
			seedPost: true, wantRows: 0, wantPromote: 0,
		},
		{
			name: "empty memo skipped",
			from: "alice", amount: "5.000 SBD", memo: "",
			seedPost: true, wantRows: 0, wantPromote: 0,
		},
		{
			name: "two-slash memo skipped",
			from: "alice", amount: "5.000 SBD", memo: "@alice/promote-me/x",
			seedPost: true, wantRows: 0, wantPromote: 0,
		},
		{
			name: "memo without leading at skipped",
			from: "alice", amount: "5.000 SBD", memo: "alice/promote-me",
			seedPost: true, wantRows: 0, wantPromote: 0,
		},
		{
			name: "memo targeting unknown post skipped",
			from: "alice", amount: "5.000 SBD", memo: "@alice/no-such-post",
			seedPost: false, wantRows: 0, wantPromote: 0,
		},
		{
			name: "memo author not registered skipped",
			from: "alice", amount: "5.000 SBD", memo: "@ghost/promote-me",
			seedPost: false, wantRows: 0, wantPromote: 0,
		},
		{
			name: "unregistered sender skipped (data error)",
			from: "ghost", amount: "5.000 SBD", memo: "@alice/promote-me",
			seedPost: true, wantRows: 0, wantPromote: 0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			database, repo, cleanup := withMigratedDB(t)
			defer cleanup()

			// 'null' is seeded by the schema migration; alice holds the post.
			seedAccounts(t, database, "alice")
			if c.seedPost {
				post := &models.Post{
					Author:    "alice",
					Permlink:  "promote-me",
					CreatedAt: time.Unix(1458845200, 0).UTC(),
					Depth:     0,
					IsValid:   true,
				}
				if err := database.DB.Create(post).Error; err != nil {
					t.Fatalf("seed post: %v", err)
				}
			}
			bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

			const blockNum = int64(16000000)
			block := robustnessBlock(blockNum, "2017-01-01T00:00:00", []string{"trx-1"},
				[]interface{}{
					[]interface{}{"transfer_operation", map[string]interface{}{
						"from": c.from, "to": "null",
						"amount": c.amount, "memo": c.memo,
					}},
				},
			)

			// The block must COMMIT: a skipped transfer is ordinary chain
			// data, never a poison block.
			if err := bp.ProcessBlock(context.Background(), block, false); err != nil {
				t.Fatalf("ProcessBlock: %v (invalid memo/token must skip the record, not fail the block)", err)
			}
			if n := countWhere(t, database, "hive_blocks", "num = ?", blockNum); n != 1 {
				t.Fatalf("hive_blocks rows = %d, want 1 (block committed)", n)
			}
			if n := countWhere(t, database, "hive_payments", "1=1"); n != c.wantRows {
				t.Fatalf("hive_payments rows = %d, want %d", n, c.wantRows)
			}
			if c.seedPost {
				var post models.Post
				if err := database.DB.Where("author = ? AND permlink = ?", "alice", "promote-me").
					First(&post).Error; err != nil {
					t.Fatalf("load post: %v", err)
				}
				if post.Promoted != c.wantPromote {
					t.Errorf("promoted = %v, want %v", post.Promoted, c.wantPromote)
				}
			}
		})
	}
}
