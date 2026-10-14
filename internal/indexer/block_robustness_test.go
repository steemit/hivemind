package indexer

// Block-processing robustness tests (planned PR#404, KR3 W3):
//   - P1-1: op error classification -- data errors skip the op (block
//     commits), infrastructure errors fail the whole block (transaction
//     rolls back; the sync layer retries it and no op is lost);
//   - P1-3: same-block account registration runs BEFORE custom_json ops;
//   - P1-15: block read-modify-write sequences ride the block transaction;
//   - 2.10: notifications written during block processing ride the block
//     transaction -- a rollback leaves no residue in hive_notifs.

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

// tooLongAuthor exceeds hive_posts.author VARCHAR(16); the post insert
// fails with a genuine PostgreSQL error mid-block, standing in for a
// transient infrastructure failure after earlier ops already wrote rows.
const tooLongAuthor = "authorlongerthan16"

func countWhere(t *testing.T, database *db.DB, table, where string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := database.DB.Table(table).Where(where, args...).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestInfraErrorFailsBlockRetrySucceeds pins the P1-1 acceptance: a block
// whose processing hits an infrastructure error fails whole (nothing
// partially applied), and reprocessing the identical block after the
// transient condition clears succeeds with every op landed -- the sync
// loop's retry semantics (syncIrreversible re-processes from hive_blocks
// head after its 3s backoff).
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
//   - the follow envelope is the block 8,000,245 shape
//     (["follow",{"follower":"baerdric","following":"cathynsons","what":["blog"]}])
//     with the follower swapped to the same-block-created account. A block
//     in which the newly created account itself follows someone was not
//     found in a 3,000-block scan (mined/created accounts rarely act inside
//     their creation block), so the two real payloads are combined here.
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
// while processing a block ride the block transaction, so when a later op
// in the same block fails with an infrastructure error (here: a genuine
// PostgreSQL error from an over-long author column) the rollback leaves no
// notification (or any other block write) behind.
func TestBlockRollbackLeavesNoNotifyResidue(t *testing.T) {
	t.Run("follow notif", func(t *testing.T) {
		database, repo, cleanup := withMigratedDB(t)
		defer cleanup()

		seedAccounts(t, database, "alice", "bob")
		bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

		const blockNum = int64(16000000)
		block := robustnessBlock(blockNum, "2017-01-01T00:00:00", []string{"trx-1"},
			[]interface{}{
				// Writes the follow row AND its notification inside the tx.
				[]interface{}{"custom_json_operation", customJSONOp("alice", `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)},
				// Fails mid-block with a real DB error (author longer than
				// VARCHAR(16)).
				[]interface{}{"comment_operation", map[string]interface{}{
					"author": tooLongAuthor, "permlink": "boom",
					"parent_author": "", "parent_permlink": "steem",
					"json_metadata": "",
				}},
			},
		)

		if err := bp.ProcessBlock(context.Background(), block, false); err == nil {
			t.Fatal("mid-block DB error must fail the block")
		}
		// The genesis block (num 0) is schema-seeded; only this block's row
		// must be absent. Every table the block touched must be empty.
		if n := countWhere(t, database, "hive_blocks", "num = ?", blockNum); n != 0 {
			t.Errorf("hive_blocks rows after rollback = %d, want 0 (no residue)", n)
		}
		for table := range map[string]struct{}{"hive_notifs": {}, "hive_follows": {}, "hive_posts": {}} {
			if n := countWhere(t, database, table, "1=1"); n != 0 {
				t.Errorf("%s rows after rollback = %d, want 0 (no residue)", table, n)
			}
		}
		// Reprocessing the identical block once the failing op is gone
		// (i.e. the data fixed itself / the transient error cleared)
		// succeeds and re-writes the notification.
		fixed := robustnessBlock(blockNum+1, "2017-01-01T00:00:03", []string{"trx-1"},
			[]interface{}{
				[]interface{}{"custom_json_operation", customJSONOp("alice", `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`)},
			},
		)
		if err := bp.ProcessBlock(context.Background(), fixed, false); err != nil {
			t.Fatalf("ProcessBlock after transient error: %v", err)
		}
		if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeFollow); n != 1 {
			t.Fatalf("follow notifs after successful reprocess = %d, want 1", n)
		}
	})

	t.Run("community register and subscribe notifs", func(t *testing.T) {
		database, repo, cleanup := withMigratedDB(t)
		defer cleanup()

		seedAccounts(t, database, "alice")
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
				// Actor subscribes: subscription row + subscribe notification.
				[]interface{}{"custom_json_operation", customJSONOp("alice", `["subscribe",{"community":"hive-174017"}]`)},
				// Mid-block infrastructure failure.
				[]interface{}{"comment_operation", map[string]interface{}{
					"author": tooLongAuthor, "permlink": "boom",
					"parent_author": "", "parent_permlink": "steem",
					"json_metadata": "",
				}},
			},
		)

		if err := bp.ProcessBlock(context.Background(), block, false); err == nil {
			t.Fatal("mid-block DB error must fail the block")
		}
		// Genesis block aside (num 0), every table the block touched --
		// including the in-transaction community registration and its
		// notifications -- must be empty.
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
	})
}

// TestSetLastReadRidesBlockTransaction pins the P1-15 tx channel for the
// notify plugin's setLastRead write: the hive_accounts.lastread_at update
// executes on the block transaction, so a later failure rolls it back too.
func TestSetLastReadRidesBlockTransaction(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	seedAccounts(t, database, "alice")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	const blockNum = int64(16000000)
	block := robustnessBlock(blockNum, "2017-01-01T00:00:00", []string{"trx-1"},
		[]interface{}{
			[]interface{}{"custom_json_operation", map[string]interface{}{
				"id":                     "notify",
				"required_auths":         []interface{}{},
				"required_posting_auths": []interface{}{"alice"},
				"json":                   `["setLastRead",{"date":"2016-12-31T23:59:59"}]`,
			}},
			[]interface{}{"comment_operation", map[string]interface{}{
				"author": tooLongAuthor, "permlink": "boom",
				"parent_author": "", "parent_permlink": "steem",
				"json_metadata": "",
			}},
		},
	)

	if err := bp.ProcessBlock(context.Background(), block, false); err == nil {
		t.Fatal("mid-block DB error must fail the block")
	}
	// lastread_at is NOT NULL DEFAULT '1970-01-01'; the rolled-back write
	// must leave it at the epoch default.
	acct := getAccount(t, database, context.Background(), "alice")
	if !acct.LastreadAt.Equal(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("lastread_at = %v after rollback, want epoch default (write rode the block tx)", acct.LastreadAt)
	}

	// Same op in a block without the failure commits the update.
	ok := robustnessBlock(blockNum+1, "2017-01-01T00:00:03", []string{"trx-1"},
		[]interface{}{
			[]interface{}{"custom_json_operation", map[string]interface{}{
				"id":                     "notify",
				"required_auths":         []interface{}{},
				"required_posting_auths": []interface{}{"alice"},
				"json":                   `["setLastRead",{"date":"2016-12-31T23:59:59"}]`,
			}},
		},
	)
	if err := bp.ProcessBlock(context.Background(), ok, false); err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}
	acct = getAccount(t, database, context.Background(), "alice")
	if !acct.LastreadAt.Equal(time.Date(2016, 12, 31, 23, 59, 59, 0, time.UTC)) {
		t.Fatalf("lastread_at = %v after commit, want 2016-12-31T23:59:59", acct.LastreadAt)
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
