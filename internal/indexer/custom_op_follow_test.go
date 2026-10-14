package indexer

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/config"
)

// Real follow-plugin custom_json payloads captured from the Steem chain via
// condenser_api.get_block (scanned 2026-10-14). Used as golden fixtures so
// the envelope parser is validated against actual on-chain shapes, not
// approximations:
//
//	block 14000000 (2017-07-25T18:58:15), auth carelesstrader:
//	  ["follow",{"follower":"carelesstrader","following":"jesta","what":["blog"]}]
//	block 14000000, auth mardah.resonance:
//	  ["follow",{"follower":"mardah.resonance","following":"macdonald4433","what":[]}]
//	block 14000007 (2017-07-25T18:58:36), auth ryanbiddulph:
//	  ["follow",{"follower":"ryanbiddulph","following":"matrixdweller","what":["ignore"]}]
//	block 14000000, auth sergei.rubtsoff:
//	  ["reblog",{"account":"sergei.rubtsoff","author":"warn-a-brutha","permlink":"gta-5-thug-life-17---drive-by-firetruck-gta-v-online---funny-gta-video-1500895822-6480067"}]
const (
	goldenFollowBlogJSON   = `["follow",{"follower":"carelesstrader","following":"jesta","what":["blog"]}]`
	goldenFollowClearJSON  = `["follow",{"follower":"mardah.resonance","following":"macdonald4433","what":[]}]`
	goldenFollowIgnoreJSON = `["follow",{"follower":"ryanbiddulph","following":"matrixdweller","what":["ignore"]}]`
	goldenReblogJSON       = `["reblog",{"account":"sergei.rubtsoff","author":"warn-a-brutha","permlink":"gta-5-thug-life-17---drive-by-firetruck-gta-v-online---funny-gta-video-1500895822-6480067"}]`
)

// TestParseFollowPluginOp pins the envelope parsing against real chain
// shapes plus the degenerate forms legacy custom_op.py skips.
func TestParseFollowPluginOp(t *testing.T) {
	cases := []struct {
		name      string
		json      string
		blockNum  int64
		wantOK    bool
		wantCmd   string
		wantField string // payload["follower"] (or "account" for reblog)
		wantVal   string
	}{
		{
			name:      "chain follow what=[blog] (block 14000000)",
			json:      goldenFollowBlogJSON,
			blockNum:  14000000,
			wantOK:    true,
			wantCmd:   "follow",
			wantField: "follower",
			wantVal:   "carelesstrader",
		},
		{
			name:      "chain follow what=[] (block 14000000)",
			json:      goldenFollowClearJSON,
			blockNum:  14000000,
			wantOK:    true,
			wantCmd:   "follow",
			wantField: "follower",
			wantVal:   "mardah.resonance",
		},
		{
			name:      "chain follow what=[ignore] (block 14000007)",
			json:      goldenFollowIgnoreJSON,
			blockNum:  14000007,
			wantOK:    true,
			wantCmd:   "follow",
			wantField: "follower",
			wantVal:   "ryanbiddulph",
		},
		{
			name:      "chain reblog envelope (block 14000000)",
			json:      goldenReblogJSON,
			blockNum:  14000000,
			wantOK:    true,
			wantCmd:   "reblog",
			wantField: "account",
			wantVal:   "sergei.rubtsoff",
		},
		{
			name:      "bare object before 6M wrapped as follow (legacy compat)",
			json:      `{"follower":"alice","following":"bob","what":["blog"]}`,
			blockNum:  5999999,
			wantOK:    true,
			wantCmd:   "follow",
			wantField: "follower",
			wantVal:   "alice",
		},
		{
			name:     "bare object at/after 6M skipped (legacy drops it)",
			json:     `{"follower":"alice","following":"bob","what":["blog"]}`,
			blockNum: 6000000,
			wantOK:   false,
		},
		{
			name:     "one-element array",
			json:     `["follow"]`,
			blockNum: 14000000,
			wantOK:   false,
		},
		{
			name:     "three-element array",
			json:     `["follow",{"follower":"a"},2]`,
			blockNum: 14000000,
			wantOK:   false,
		},
		{
			name:     "payload not an object",
			json:     `["follow","payload"]`,
			blockNum: 14000000,
			wantOK:   false,
		},
		{
			name:     "command not a string",
			json:     `[1,{"follower":"a"}]`,
			blockNum: 14000000,
			wantOK:   false,
		},
		{
			name:     "malformed json",
			json:     `not json`,
			blockNum: 14000000,
			wantOK:   false,
		},
		{
			name:     "json null",
			json:     `null`,
			blockNum: 14000000,
			wantOK:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, payload, ok := parseFollowPluginOp(c.json, c.blockNum)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if !c.wantOK {
				return
			}
			if cmd != c.wantCmd {
				t.Errorf("cmd = %q, want %q", cmd, c.wantCmd)
			}
			if got, _ := payload[c.wantField].(string); got != c.wantVal {
				t.Errorf("payload[%s] = %q, want %q", c.wantField, got, c.wantVal)
			}
		})
	}
}

// TestFollowStateFromWhat pins the legacy positional state computation
// (follow.py _validated_op: first() checked for 'blog', second() for
// 'ignore'). Expected values cross-verified against the legacy Python code
// with funcy installed.
func TestFollowStateFromWhat(t *testing.T) {
	cases := []struct {
		name string
		what []interface{}
		want int16
	}{
		{"empty list (on-chain unfollow shape)", []interface{}{}, 0},
		{"blog (on-chain follow shape)", []interface{}{"blog"}, 1},
		{"ignore only yields 0 (legacy positional quirk)", []interface{}{"ignore"}, 0},
		{"blog then ignore", []interface{}{"blog", "ignore"}, 3},
		{"ignore then blog yields 0 (legacy positional quirk)", []interface{}{"ignore", "blog"}, 0},
		{"empty string then ignore", []interface{}{"", "ignore"}, 2},
		{"blog duplicated", []interface{}{"blog", "blog"}, 1},
		{"non-string first element", []interface{}{float64(1), "ignore"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := followStateFromWhat(c.what); got != c.want {
				t.Errorf("followStateFromWhat(%v) = %d, want %d", c.what, got, c.want)
			}
		})
	}
}

// seedAccounts inserts bare account rows for the given names.
func seedAccounts(t *testing.T, database *db.DB, names ...string) {
	t.Helper()
	tx := database.DB.Begin()
	for _, n := range names {
		if err := tx.Create(&models.Account{Name: n, CreatedAt: time.Unix(1458845200, 0).UTC()}).Error; err != nil {
			tx.Rollback()
			t.Fatalf("seed account %s: %v", n, err)
		}
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatalf("commit seed accounts: %v", err)
	}
}

// customJSONOp builds the normalized custom_json_operation value map the
// block processor hands to CustomOpProcessor.ProcessOps.
func customJSONOp(auth, jsonStr string) map[string]interface{} {
	return map[string]interface{}{
		"id":                     "follow",
		"required_auths":         []interface{}{},
		"required_posting_auths": []interface{}{auth},
		"json":                   jsonStr,
	}
}

// genesisHash is the seeded block-0 hash (see 0001_init_schema.up.sql);
// hive_blocks.prev carries an FK to hive_blocks.hash, so fixture blocks
// point their prev at the genesis row.
const genesisHash = "0000000000000000000000000000000000000000"

// followPluginBlock builds a minimal block map carrying the given
// custom_json operations in a single transaction.
func followPluginBlock(num int64, ts string, ops ...map[string]interface{}) map[string]interface{} {
	operations := make([]interface{}, 0, len(ops))
	for _, op := range ops {
		operations = append(operations, []interface{}{"custom_json_operation", op})
	}
	return map[string]interface{}{
		"block_num":       float64(num),
		"block_id":        fmt.Sprintf("%040x", num),
		"previous":        genesisHash,
		"timestamp":       ts,
		"transactions":    []interface{}{map[string]interface{}{"operations": operations}},
		"transaction_ids": []interface{}{},
	}
}

func getAccount(t *testing.T, database *db.DB, ctx context.Context, name string) models.Account {
	t.Helper()
	var acct models.Account
	if err := database.DB.WithContext(ctx).Where("name = ?", name).First(&acct).Error; err != nil {
		t.Fatalf("account %s: %v", name, err)
	}
	return acct
}

// getFollowRow returns the hive_follows row for the pair, or nil when absent.
func getFollowRow(t *testing.T, database *db.DB, ctx context.Context, follower, following int64) *models.Follow {
	t.Helper()
	var f models.Follow
	err := database.DB.WithContext(ctx).
		Where("follower = ? AND following = ?", follower, following).First(&f).Error
	if err == gorm.ErrRecordNotFound {
		return nil
	}
	if err != nil {
		t.Fatalf("query follow row: %v", err)
	}
	return &f
}

// TestBlockProcessorSharesFollowIndexer pins audit P0-4: the block
// processor and its custom-op processor must hold the SAME FollowIndexer
// instance, so that count deltas accumulated from follow ops are the ones
// Sync flushes.
func TestBlockProcessorSharesFollowIndexer(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	if bp.Follows() == nil {
		t.Fatal("BlockProcessor.Follows() is nil")
	}
	if bp.customOps.followIndexer == nil {
		t.Fatal("CustomOpProcessor.followIndexer is nil")
	}
	if bp.Follows() != bp.customOps.followIndexer {
		t.Fatal("BlockProcessor and CustomOpProcessor must share one FollowIndexer instance (P0-4)")
	}
}

// TestFollowArrayEnvelopeGolden drives full ProcessBlock calls with the
// real chain payloads from blocks 14000000/14000007 and asserts the
// resulting hive_follows rows and flushed count deltas (P0-1 end to end):
//   - what=["blog"] creates a state-1 row and +1 deltas;
//   - what=[] (unfollow shape) on a never-followed pair creates no row and
//     no deltas (legacy idempotent short-circuit);
//   - what=["ignore"] alone computes state 0 (legacy positional quirk), so
//     it also creates no row and no deltas.
func TestFollowArrayEnvelopeGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database,
		"carelesstrader", "jesta",
		"mardah.resonance", "macdonald4433",
		"ryanbiddulph", "matrixdweller")

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	// Block 14000000: the blog follow and the cleared-what op, verbatim.
	block := followPluginBlock(14000000, "2017-07-25T18:58:15",
		customJSONOp("carelesstrader", goldenFollowBlogJSON),
		customJSONOp("mardah.resonance", goldenFollowClearJSON),
	)
	if err := bp.ProcessBlock(ctx, block, false); err != nil {
		t.Fatalf("ProcessBlock 14000000: %v", err)
	}
	// Block 14000007: the ignore op, verbatim.
	block7 := followPluginBlock(14000007, "2017-07-25T18:58:36",
		customJSONOp("ryanbiddulph", goldenFollowIgnoreJSON),
	)
	if err := bp.ProcessBlock(ctx, block7, false); err != nil {
		t.Fatalf("ProcessBlock 14000007: %v", err)
	}

	follower := getAccount(t, database, ctx, "carelesstrader")
	following := getAccount(t, database, ctx, "jesta")

	// Exactly one follow row overall: the blog follow.
	var total int64
	database.DB.WithContext(ctx).Table("hive_follows").Count(&total)
	if total != 1 {
		t.Fatalf("hive_follows rows = %d, want 1 (blog follow only)", total)
	}
	row := getFollowRow(t, database, ctx, follower.ID, following.ID)
	if row == nil {
		t.Fatal("missing follow row for carelesstrader -> jesta")
	}
	if row.State != models.FollowStateBlog {
		t.Errorf("state = %d, want %d (blog)", row.State, models.FollowStateBlog)
	}

	// The shared instance received the delta; flushing through the block
	// processor's accessor applies it.
	if bp.Follows().PendingDeltasLen() != 2 {
		t.Fatalf("pending delta entries = %d, want 2 (follower+following)", bp.Follows().PendingDeltasLen())
	}
	if err := bp.Follows().Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	follower = getAccount(t, database, ctx, "carelesstrader")
	following = getAccount(t, database, ctx, "jesta")
	if follower.Following != 1 || follower.Followers != 0 {
		t.Errorf("carelesstrader following/followers = %d/%d, want 1/0", follower.Following, follower.Followers)
	}
	if following.Followers != 1 || following.Following != 0 {
		t.Errorf("jesta followers/following = %d/%d, want 1/0", following.Followers, following.Following)
	}
	for _, name := range []string{"mardah.resonance", "macdonald4433", "ryanbiddulph", "matrixdweller"} {
		acct := getAccount(t, database, ctx, name)
		if acct.Following != 0 || acct.Followers != 0 {
			t.Errorf("%s following/followers = %d/%d, want 0/0", name, acct.Following, acct.Followers)
		}
	}

	// The 0->1 transition wrote one follow notification (live mode).
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").
		Where("type_id = ?", models.NotifyTypeFollow).Count(&notifs)
	if notifs != 1 {
		t.Errorf("follow notifs = %d, want 1", notifs)
	}
}

// TestFollowIdempotentRepeat pins the P1-9 idempotent short-circuit: the
// same follow op replayed (even across flushes) leaves a single
// hive_follows row and applies the count delta exactly once.
func TestFollowIdempotentRepeat(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "carelesstrader", "jesta")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	// First follow (block 14000000 shape), then flush.
	if err := bp.ProcessBlock(ctx, followPluginBlock(14000000, "2017-07-25T18:58:15",
		customJSONOp("carelesstrader", goldenFollowBlogJSON)), false); err != nil {
		t.Fatalf("ProcessBlock first: %v", err)
	}
	if err := bp.Follows().Flush(ctx); err != nil {
		t.Fatalf("Flush first: %v", err)
	}

	// Identical follow replayed in a later block, then flush again.
	if err := bp.ProcessBlock(ctx, followPluginBlock(14000001, "2017-07-25T18:58:18",
		customJSONOp("carelesstrader", goldenFollowBlogJSON)), false); err != nil {
		t.Fatalf("ProcessBlock repeat: %v", err)
	}
	if err := bp.Follows().Flush(ctx); err != nil {
		t.Fatalf("Flush repeat: %v", err)
	}

	follower := getAccount(t, database, ctx, "carelesstrader")
	following := getAccount(t, database, ctx, "jesta")

	var total int64
	database.DB.WithContext(ctx).Table("hive_follows").Count(&total)
	if total != 1 {
		t.Errorf("hive_follows rows = %d, want 1 after repeated follow", total)
	}
	if follower.Following != 1 || following.Followers != 1 {
		t.Errorf("counts following/followers = %d/%d, want 1/1 (no double count)",
			follower.Following, following.Followers)
	}

	// Unfollow (real what=[] shape) drops the row to state 0 and undoes the
	// count; a second identical clear op is a no-op.
	if err := bp.ProcessBlock(ctx, followPluginBlock(14000002, "2017-07-25T18:58:21",
		customJSONOp("carelesstrader", `["follow",{"follower":"carelesstrader","following":"jesta","what":[]}]`)), false); err != nil {
		t.Fatalf("ProcessBlock clear: %v", err)
	}
	if err := bp.ProcessBlock(ctx, followPluginBlock(14000003, "2017-07-25T18:58:24",
		customJSONOp("carelesstrader", `["follow",{"follower":"carelesstrader","following":"jesta","what":[]}]`)), false); err != nil {
		t.Fatalf("ProcessBlock clear repeat: %v", err)
	}
	if err := bp.Follows().Flush(ctx); err != nil {
		t.Fatalf("Flush clear: %v", err)
	}

	row := getFollowRow(t, database, ctx, follower.ID, following.ID)
	if row == nil {
		t.Fatal("follow row missing after clear (legacy keeps a state-0 row)")
	}
	if row.State != models.FollowStateNone {
		t.Errorf("state after clear = %d, want 0", row.State)
	}
	follower = getAccount(t, database, ctx, "carelesstrader")
	following = getAccount(t, database, ctx, "jesta")
	if follower.Following != 0 || following.Followers != 0 {
		t.Errorf("counts after clear = %d/%d, want 0/0 (single unfollow delta)",
			follower.Following, following.Followers)
	}
}

// TestProcessFollowValidationSemantics pins the legacy _validated_op rules
// against direct ProcessFollow calls: malformed payloads (missing or
// non-list `what`, missing accounts) are SKIPPED -- never treated as
// unfollow -- and self-follows / signer mismatches are rejected.
func TestProcessFollowValidationSemantics(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob", "carol")
	fi := NewFollowIndexer(repo, testLogger())

	scenarios := []struct {
		name    string
		account string
		op      map[string]interface{}
		wantErr bool
	}{
		{
			name:    "self-follow rejected (legacy: can't follow self)",
			account: "alice",
			op:      map[string]interface{}{"follower": "alice", "following": "alice", "what": []interface{}{"blog"}},
			wantErr: true,
		},
		{
			name:    "signer mismatch rejected (legacy: impersonation)",
			account: "alice",
			op:      map[string]interface{}{"follower": "bob", "following": "carol", "what": []interface{}{"blog"}},
			wantErr: true,
		},
		{
			name:    "missing what skipped silently (NOT an unfollow)",
			account: "alice",
			op:      map[string]interface{}{"follower": "alice", "following": "bob"},
		},
		{
			name:    "non-list what skipped silently",
			account: "alice",
			op:      map[string]interface{}{"follower": "alice", "following": "bob", "what": "blog"},
		},
		{
			name:    "missing following skipped silently",
			account: "alice",
			op:      map[string]interface{}{"follower": "alice", "what": []interface{}{"blog"}},
		},
		{
			name:    "unknown account rejected",
			account: "alice",
			op:      map[string]interface{}{"follower": "alice", "following": "nobody", "what": []interface{}{"blog"}},
			wantErr: true,
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			tx := database.DB.WithContext(ctx).Begin()
			err := fi.ProcessFollow(ctx, tx, s.account, s.op, time.Unix(1501008000, 0).UTC(), false)
			tx.Rollback()
			if s.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !s.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	// None of the rejected/skipped scenarios wrote rows or deltas.
	var rows int64
	database.DB.WithContext(ctx).Table("hive_follows").Count(&rows)
	if rows != 0 {
		t.Errorf("hive_follows rows = %d, want 0", rows)
	}
	if fi.PendingDeltasLen() != 0 {
		t.Errorf("pending deltas = %d, want 0", fi.PendingDeltasLen())
	}

	// A missing `what` must not undo an existing follow: seed a blog
	// follow, then send a what-less op and confirm nothing changed.
	alice := getAccount(t, database, ctx, "alice")
	bob := getAccount(t, database, ctx, "bob")
	tx := database.DB.WithContext(ctx).Begin()
	if err := fi.ProcessFollow(ctx, tx, "alice",
		map[string]interface{}{"follower": "alice", "following": "bob", "what": []interface{}{"blog"}},
		time.Unix(1501008000, 0).UTC(), false); err != nil {
		tx.Rollback()
		t.Fatalf("seed follow: %v", err)
	}
	if err := fi.ProcessFollow(ctx, tx, "alice",
		map[string]interface{}{"follower": "alice", "following": "bob"},
		time.Unix(1501008001, 0).UTC(), false); err != nil {
		tx.Rollback()
		t.Fatalf("what-less op: %v", err)
	}
	tx.Commit()

	row := getFollowRow(t, database, ctx, alice.ID, bob.ID)
	if row == nil || row.State != models.FollowStateBlog {
		t.Fatalf("follow row after what-less op = %+v, want state 1 (skip, not unfollow)", row)
	}
	if fi.PendingDeltasLen() != 2 {
		t.Errorf("pending deltas = %d, want 2 (only the seeded follow)", fi.PendingDeltasLen())
	}
}

// TestProcessFollowStateTransitions pins the legacy follow_op delta rules
// on state transitions, including the positional-state quirks.
func TestProcessFollowStateTransitions(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob", "carol", "dave")
	fi := NewFollowIndexer(repo, testLogger())
	apply := func(account, follower, following string, what []interface{}) {
		t.Helper()
		tx := database.DB.WithContext(ctx).Begin()
		if err := fi.ProcessFollow(ctx, tx, account,
			map[string]interface{}{"follower": follower, "following": following, "what": what},
			time.Unix(1501008000, 0).UTC(), false); err != nil {
			tx.Rollback()
			t.Fatalf("ProcessFollow %s -> %s %v: %v", follower, following, what, err)
		}
		tx.Commit()
	}

	alice := getAccount(t, database, ctx, "alice")
	bob := getAccount(t, database, ctx, "bob")
	carol := getAccount(t, database, ctx, "carol")
	dave := getAccount(t, database, ctx, "dave")

	// what=["ignore"] on a fresh pair computes state 0 -> legacy
	// short-circuit: no row at all (real chain shape, block 14000007).
	apply("carol", "carol", "dave", []interface{}{"ignore"})
	if row := getFollowRow(t, database, ctx, carol.ID, dave.ID); row != nil {
		t.Errorf("ignore-only op created a row: %+v, want none (legacy state-0 short-circuit)", row)
	}

	// 0 -> 1 (["blog"]): follow delta (+1 both directions).
	apply("alice", "alice", "bob", []interface{}{"blog"})
	if fi.PendingDeltasLen() != 2 {
		t.Fatalf("pending deltas after 0->1 = %d, want 2 (follow)", fi.PendingDeltasLen())
	}

	// 1 -> 3 (adding ignore on top of blog): ignore-only jump -> no delta.
	apply("alice", "alice", "bob", []interface{}{"blog", "ignore"})
	row := getFollowRow(t, database, ctx, alice.ID, bob.ID)
	if row == nil || row.State != models.FollowStateBoth {
		t.Fatalf("row after 1->3 = %+v, want state 3", row)
	}
	if fi.PendingDeltasLen() != 2 {
		t.Errorf("pending deltas after 1->3 = %d, want 2 (ignore-only jump adds none)", fi.PendingDeltasLen())
	}

	// 3 -> 1 (clearing ignore, keeping blog): ignore-only jump -> no delta.
	apply("alice", "alice", "bob", []interface{}{"blog"})
	row = getFollowRow(t, database, ctx, alice.ID, bob.ID)
	if row == nil || row.State != models.FollowStateBlog {
		t.Fatalf("row after 3->1 = %+v, want state 1", row)
	}
	if fi.PendingDeltasLen() != 2 {
		t.Errorf("pending deltas after 3->1 = %d, want 2 (ignore-only jump adds none)", fi.PendingDeltasLen())
	}

	// 1 -> 0: blog cleared -> unfollow delta (net counts back to zero).
	apply("alice", "alice", "bob", []interface{}{})
	if fi.PendingDeltasLen() != 2 {
		t.Errorf("pending deltas after 1->0 = %d, want 2 (net zero after unfollow)", fi.PendingDeltasLen())
	}

	// Flush: net-zero sequence leaves all counts at zero.
	if err := fi.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		acct := getAccount(t, database, ctx, name)
		if acct.Following != 0 || acct.Followers != 0 {
			t.Errorf("%s following/followers = %d/%d, want 0/0", name, acct.Following, acct.Followers)
		}
	}
}

// TestProcessFollowInitialSyncGate pins the legacy gate
// (`if not DbState.is_initial_sync()`): during initial sync the row is
// still written, but no count deltas accumulate and no notification is
// emitted (counts come from the terminal ForceRecount).
func TestProcessFollowInitialSyncGate(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob")
	fi := NewFollowIndexer(repo, testLogger())

	tx := database.DB.WithContext(ctx).Begin()
	if err := fi.ProcessFollow(ctx, tx, "alice",
		map[string]interface{}{"follower": "alice", "following": "bob", "what": []interface{}{"blog"}},
		time.Unix(1501008000, 0).UTC(), true); err != nil {
		tx.Rollback()
		t.Fatalf("ProcessFollow: %v", err)
	}
	tx.Commit()

	alice := getAccount(t, database, ctx, "alice")
	bob := getAccount(t, database, ctx, "bob")
	if row := getFollowRow(t, database, ctx, alice.ID, bob.ID); row == nil || row.State != models.FollowStateBlog {
		t.Fatalf("row = %+v, want state 1 (rows are written during initial sync)", row)
	}
	if fi.PendingDeltasLen() != 0 {
		t.Errorf("pending deltas = %d, want 0 during initial sync", fi.PendingDeltasLen())
	}
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").Count(&notifs)
	if notifs != 0 {
		t.Errorf("notifs = %d, want 0 during initial sync", notifs)
	}
}

// TestFollowBareObjectCompat pins the pre-6M legacy compat: a bare payload
// object before block 6,000,000 is treated as ["follow", payload]; from
// block 6,000,000 on it is skipped, exactly as legacy does.
func TestFollowBareObjectCompat(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob", "carol", "dave")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	// Timestamps are synthetic but chain-plausible: interpolating from real
	// anchors (genesis block 1 = 2016-03-24T16:05:00, real block 14000000 =
	// 2017-07-25T18:58:15 -> ~3.0 s/block) puts block 6,000,000 around
	// 2016-10-18 -- NOT 2017-01 as an earlier revision of this fixture had.
	pre := `{"follower":"alice","following":"bob","what":["blog"]}`
	if err := bp.ProcessBlock(ctx, followPluginBlock(5999999, "2016-10-18T10:19:57",
		customJSONOp("alice", pre)), false); err != nil {
		t.Fatalf("ProcessBlock pre-6M: %v", err)
	}
	alice := getAccount(t, database, ctx, "alice")
	bob := getAccount(t, database, ctx, "bob")
	if row := getFollowRow(t, database, ctx, alice.ID, bob.ID); row == nil || row.State != models.FollowStateBlog {
		t.Fatalf("pre-6M bare object follow row = %+v, want state 1", row)
	}

	post := `{"follower":"carol","following":"dave","what":["blog"]}`
	if err := bp.ProcessBlock(ctx, followPluginBlock(6000000, "2016-10-18T10:20:00",
		customJSONOp("carol", post)), false); err != nil {
		t.Fatalf("ProcessBlock post-6M: %v", err)
	}
	carol := getAccount(t, database, ctx, "carol")
	dave := getAccount(t, database, ctx, "dave")
	if row := getFollowRow(t, database, ctx, carol.ID, dave.ID); row != nil {
		t.Errorf("post-6M bare object created a row: %+v, want none (legacy drops it)", row)
	}
	if bp.Follows().PendingDeltasLen() != 2 {
		t.Errorf("pending deltas = %d, want 2 (only the pre-6M follow)", bp.Follows().PendingDeltasLen())
	}
}

// TestReblogArrayEnvelopeGolden pins that the envelope rewrite keeps
// routing real ["reblog", {...}] payloads to the reblog path. Only the
// envelope routing and basic writes are asserted here; deeper reblog
// semantics (impersonation checks, reblog notifications) are tracked as
// separate audit items.
func TestReblogArrayEnvelopeGolden(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "sergei.rubtsoff", "warn-a-brutha")
	post := &models.Post{
		Author:    "warn-a-brutha",
		Permlink:  "gta-5-thug-life-17---drive-by-firetruck-gta-v-online---funny-gta-video-1500895822-6480067",
		Category:  "video",
		CreatedAt: time.Date(2017, 7, 24, 12, 0, 0, 0, time.UTC),
		Depth:     0,
		IsValid:   true,
	}
	if err := database.DB.WithContext(ctx).Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	if err := bp.ProcessBlock(ctx, followPluginBlock(14000000, "2017-07-25T18:58:15",
		customJSONOp("sergei.rubtsoff", goldenReblogJSON)), false); err != nil {
		t.Fatalf("ProcessBlock reblog: %v", err)
	}

	var reblogs int64
	database.DB.WithContext(ctx).Table("hive_reblogs").Count(&reblogs)
	if reblogs != 1 {
		t.Fatalf("hive_reblogs rows = %d, want 1", reblogs)
	}
	var feed int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").
		Where("post_id = ?", post.ID).Count(&feed)
	if feed != 1 {
		t.Errorf("hive_feed_cache rows for post = %d, want 1 (live-mode insert)", feed)
	}

	// Standard delete-reblog shape removes the row again.
	del := `["reblog",{"account":"sergei.rubtsoff","author":"warn-a-brutha","permlink":"gta-5-thug-life-17---drive-by-firetruck-gta-v-online---funny-gta-video-1500895822-6480067","delete":"delete"}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(14000001, "2017-07-25T18:58:18",
		customJSONOp("sergei.rubtsoff", del)), false); err != nil {
		t.Fatalf("ProcessBlock reblog delete: %v", err)
	}
	database.DB.WithContext(ctx).Table("hive_reblogs").Count(&reblogs)
	if reblogs != 0 {
		t.Errorf("hive_reblogs rows after delete = %d, want 0", reblogs)
	}
}

// TestProcessBlockInitialSyncNotifGate pins the isInitialSync wiring at the
// ProcessBlock level (PR#401 review P1-1): a follow 0->1 transition processed
// as part of INITIAL SYNC must NOT write a hive_notifs row nor queue count
// deltas (legacy follow.py gates on `not DbState.is_initial_sync()`), while
// the same transition in live mode must notify. Legacy initial sync never
// wrote follow notifications; before the sync.go wiring fix the gate never
// fired in production because ProcessBlock was hardcoded to isInitialSync
// false.
func TestProcessBlockInitialSyncNotifGate(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob", "carol", "dave")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	countFollowNotifs := func() int64 {
		var n int64
		database.DB.WithContext(ctx).Table("hive_notifs").
			Where("type_id = ?", models.NotifyTypeFollow).Count(&n)
		return n
	}

	initial := `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(14000000, "2017-07-25T18:58:15",
		customJSONOp("alice", initial)), true); err != nil {
		t.Fatalf("ProcessBlock initial-sync: %v", err)
	}
	alice := getAccount(t, database, ctx, "alice")
	bob := getAccount(t, database, ctx, "bob")
	if row := getFollowRow(t, database, ctx, alice.ID, bob.ID); row == nil || row.State != models.FollowStateBlog {
		t.Fatalf("initial-sync follow row = %+v, want state 1 (rows ARE written during initial sync)", row)
	}
	if n := countFollowNotifs(); n != 0 {
		t.Errorf("follow notifs after initial-sync block = %d, want 0 (initial sync never notifies)", n)
	}
	if d := bp.Follows().PendingDeltasLen(); d != 0 {
		t.Errorf("pending deltas after initial-sync block = %d, want 0 (counts come from ForceRecount)", d)
	}

	live := `["follow",{"follower":"carol","following":"dave","what":["blog"]}]`
	if err := bp.ProcessBlock(ctx, followPluginBlock(14000001, "2017-07-25T18:58:18",
		customJSONOp("carol", live)), false); err != nil {
		t.Fatalf("ProcessBlock live: %v", err)
	}
	if n := countFollowNotifs(); n != 1 {
		t.Errorf("follow notifs after live block = %d, want 1 (0->1 transition notifies)", n)
	}
	if d := bp.Follows().PendingDeltasLen(); d != 2 {
		t.Errorf("pending deltas after live block = %d, want 2 (follower+following)", d)
	}
}

// TestSyncBlocksInitialSyncWiring pins the sync.go wiring itself (PR#401
// review P1-1): syncBlocks must forward its isInitialSync argument into
// ProcessBlock. It previously hardcoded false, which would have made the
// follow_indexer gate dead code in production -- every historical follow
// 0->1 transition during initial sync would emit a hive_notifs row (legacy
// initial sync never writes them), polluting the 20M-row comparison and
// bloating the table.
func TestSyncBlocksInitialSyncWiring(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob", "carol", "dave")

	blockInitial := followPluginBlock(14000000, "2017-07-25T18:58:15",
		customJSONOp("alice", `["follow",{"follower":"alice","following":"bob","what":["blog"]}]`))
	blockLive := followPluginBlock(14000001, "2017-07-25T18:58:18",
		customJSONOp("carol", `["follow",{"follower":"carol","following":"dave","what":["blog"]}]`))

	provider := &fakeSteemProvider{getBlocksRange: func(_ context.Context, from, _ int64) ([]map[string]interface{}, error) {
		if from == 14000000 {
			return []map[string]interface{}{blockInitial}, nil
		}
		return []map[string]interface{}{blockLive}, nil
	}}
	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}

	// initialSync() calls syncBlocks(..., true).
	if err := s.syncBlocks(ctx, 14000000, 14000000, true); err != nil {
		t.Fatalf("syncBlocks initial: %v", err)
	}
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").Count(&notifs)
	if notifs != 0 {
		t.Fatalf("notifs after initial-sync syncBlocks = %d, want 0", notifs)
	}

	// syncIrreversible's steady-state loop calls syncBlocks(..., false).
	if err := s.syncBlocks(ctx, 14000001, 14000001, false); err != nil {
		t.Fatalf("syncBlocks live: %v", err)
	}
	database.DB.WithContext(ctx).Table("hive_notifs").Count(&notifs)
	if notifs != 1 {
		t.Fatalf("notifs after live syncBlocks = %d, want 1", notifs)
	}
}

// TestFollowSameBlockDuplicate pins the same-block idempotency: two identical
// follow ops in ONE block collapse to a single hive_follows row, one count
// delta and one notification, because the second op reads the row the first
// op wrote within the same block transaction (read-your-own-writes
// short-circuit).
func TestFollowSameBlockDuplicate(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "carelesstrader", "jesta")
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	if err := bp.ProcessBlock(ctx, followPluginBlock(14000000, "2017-07-25T18:58:15",
		customJSONOp("carelesstrader", goldenFollowBlogJSON),
		customJSONOp("carelesstrader", goldenFollowBlogJSON)), false); err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}

	var rows int64
	database.DB.WithContext(ctx).Table("hive_follows").Count(&rows)
	if rows != 1 {
		t.Errorf("hive_follows rows = %d, want 1 (same-block duplicate collapses)", rows)
	}
	if d := bp.Follows().PendingDeltasLen(); d != 2 {
		t.Errorf("pending deltas = %d, want 2 (single follow delta pair)", d)
	}
	if err := bp.Follows().Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	follower := getAccount(t, database, ctx, "carelesstrader")
	following := getAccount(t, database, ctx, "jesta")
	if follower.Following != 1 || following.Followers != 1 {
		t.Errorf("counts = following %d / followers %d, want 1/1 (counted once)",
			follower.Following, following.Followers)
	}
	var notifs int64
	database.DB.WithContext(ctx).Table("hive_notifs").
		Where("type_id = ?", models.NotifyTypeFollow).Count(&notifs)
	if notifs != 1 {
		t.Errorf("follow notifs = %d, want 1 (0->1 notified once)", notifs)
	}
}

// TestNewCustomOpProcessorNilFollowIndexer pins the fail-fast contract: a nil
// followIndexer must panic at construction (English message) rather than let
// a nil instance through and fail per-op, or worse, be papered over with a
// privately-built instance whose deltas would never flush (audit P0-4).
func TestNewCustomOpProcessorNilFollowIndexer(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for nil followIndexer, got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value = %v (%T), want string", r, r)
		}
		if !strings.Contains(msg, "followIndexer") {
			t.Errorf("panic message %q does not mention followIndexer", msg)
		}
	}()
	NewCustomOpProcessor(db.NewRepository(nil), nil, nil, testLogger())
}
