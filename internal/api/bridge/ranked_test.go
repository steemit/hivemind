package bridge

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// TestGetAccountPosts_ParamValidationBeforeDB verifies that parameter
// validation happens before any DB access: a zero-value RankedAPI (nil
// repo/cursor) must return a clean PublicError, not panic.
//
// This is the evolved form of the 2026-08-16 security-audit regression guard
// (feed sort used to delegate to itself with unchanged params — infinite
// recursion / stack overflow). All sorts are now implemented inline in the
// switch, so self-recursion is structurally impossible; the remaining guard
// is that bad input fails fast.
func TestGetAccountPosts_ParamValidationBeforeDB(t *testing.T) {
	r := &RankedAPI{}
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name   string
		params map[string]interface{}
		errMsg string
	}{
		{
			name:   "invalid sort",
			params: map[string]interface{}{"sort": "bogus", "account": "alice"},
			errMsg: "invalid sort type",
		},
		{
			name:   "missing sort",
			params: map[string]interface{}{"account": "alice"},
			errMsg: "missing required parameter: sort",
		},
		{
			name:   "missing account",
			params: map[string]interface{}{"sort": "feed"},
			errMsg: "missing required parameter: account",
		},
		{
			name:   "invalid account name",
			params: map[string]interface{}{"sort": "feed", "account": "ALICE"},
			errMsg: "invalid account",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest("POST", "/", nil)

			params, _ := json.Marshal(tc.params)
			result, err := r.GetAccountPosts(ctx, params)
			if err == nil {
				t.Fatalf("expected error containing %q, got result=%v", tc.errMsg, result)
			}
			if !strings.Contains(err.Error(), tc.errMsg) {
				t.Errorf("error should contain %q, got: %v", tc.errMsg, err)
			}
		})
	}
}

// TestGetAccountPosts_Sorts exercises every account sort against a real
// Postgres schema: blog (with reblog attribution), feed (follows join with
// string_agg rebloggers), posts, comments, replies, and payout (temp table),
// plus the author-blocked short-circuit.
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set:
//
//	HIVE_TEST_DATABASE_URL=postgresql://test:test@localhost:5433/hive go test -run TestGetAccountPosts_Sorts ./internal/api/bridge/...
func TestGetAccountPosts_Sorts(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping account posts sorts test")
	}
	dbURL := newTestDB(t)

	resetSchema(t, dbURL)
	if err := db.RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	defer sqlDB.Close()

	seedAccountPosts(t, gdb)

	repo := db.NewRepository(gdb)
	wrapped := &db.DB{DB: gdb}
	api := NewRankedAPI(repo, wrapped, nil, "hive-123456")

	call := func(sort string) []map[string]interface{} {
		t.Helper()
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		params, _ := json.Marshal(map[string]interface{}{"sort": sort, "account": "alice", "limit": 10})
		result, err := api.GetAccountPosts(ctx, params)
		if err != nil {
			t.Fatalf("sort %q: %v", sort, err)
		}
		posts, ok := result.([]map[string]interface{})
		if !ok {
			t.Fatalf("sort %q: expected []map, got %T", sort, result)
		}
		return posts
	}

	permlinks := func(posts []map[string]interface{}) []string {
		out := make([]string, 0, len(posts))
		for _, p := range posts {
			out = append(out, p["permlink"].(string))
		}
		return out
	}
	expectList := func(sort string, got []string, want []string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("sort %q: expected %v, got %v", sort, want, got)
		}
	}

	// blog: own post + reblogged post, reblog attributed to alice.
	blog := call("blog")
	expectList("blog", permlinks(blog), []string{"post-b", "post-a"})
	for _, p := range blog {
		if p["permlink"] == "post-b" {
			rby, ok := p["reblogged_by"].([]string)
			if !ok || len(rby) != 1 || rby[0] != "alice" {
				t.Errorf("blog: post-b should be reblogged_by [alice], got %v", p["reblogged_by"])
			}
		}
	}

	// feed: posts from follows (bob's post-b), reblogger = bob himself is
	// excluded from reblogged_by (he's the author).
	feed := call("feed")
	expectList("feed", permlinks(feed), []string{"post-b"})
	if len(feed) > 0 {
		if _, has := feed[0]["reblogged_by"]; has {
			t.Errorf("feed: author reblogger should be excluded, got %v", feed[0]["reblogged_by"])
		}
	}

	// posts: alice's own top-level posts only.
	expectList("posts", permlinks(call("posts")), []string{"post-a"})

	// comments: alice's comments only.
	expectList("comments", permlinks(call("comments")), []string{"comment-x"})

	// replies: replies to alice's posts (carol's reply-y).
	expectList("replies", permlinks(call("replies")), []string{"reply-y"})

	// payout: reads hive_posts_cache_temp; legacy does not filter by depth
	// here (author's unpaid posts AND comments, payout descending).
	expectList("payout", permlinks(call("payout")), []string{"post-a", "comment-x"})

	// author-blocked account returns empty.
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	params, _ := json.Marshal(map[string]interface{}{"sort": "posts", "account": "mallory", "limit": 10})
	result, err := api.GetAccountPosts(ctx, params)
	if err != nil {
		t.Fatalf("blocked author: %v", err)
	}
	if blocked, ok := result.([]map[string]interface{}); !ok || len(blocked) != 0 {
		t.Errorf("blocked author should return empty, got %v", result)
	}
}

// seedAccountPosts builds the fixture for TestGetAccountPosts_Sorts:
//
//	alice: post-a (depth 0), comment-x (depth 1 on bob's post)
//	bob:   post-b (depth 0), reblogged by alice into her blog
//	carol: reply-y (depth 1 on alice's post-a)
//	mallory: blocked author (hive_posts_status list_type 3)
//	alice follows bob; feed_cache rows for alice (post-a, post-b) and bob
//	(post-b); hive_posts_cache + hive_posts_cache_temp rows for payouts.
func seedAccountPosts(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()

	for _, name := range []string{"alice", "bob", "carol", "mallory"} {
		if err := gdb.Create(&models.Account{Name: name, CreatedAt: now}).Error; err != nil {
			t.Fatalf("seed account %s: %v", name, err)
		}
	}

	mk := func(id int64, parent int64, author, permlink string, depth int16) models.Post {
		p := models.Post{
			Author:    author,
			Permlink:  permlink,
			Category:  "test",
			CreatedAt: now,
			Depth:     depth,
		}
		p.ID = id
		if parent != 0 {
			p.ParentID.Valid = true
			p.ParentID.Int64 = parent
		}
		return p
	}
	posts := []models.Post{
		mk(1, 0, "alice", "post-a", 0),
		mk(2, 0, "bob", "post-b", 0),
		mk(3, 2, "alice", "comment-x", 1),
		mk(4, 1, "carol", "reply-y", 1),
		mk(5, 0, "mallory", "post-m", 0),
	}
	for i := range posts {
		if err := gdb.Create(&posts[i]).Error; err != nil {
			t.Fatalf("seed post %d: %v", posts[i].ID, err)
		}
	}

	// hive_accounts ids for follows/feed_cache.
	var alice, bob models.Account
	if err := gdb.Where("name = ?", "alice").First(&alice).Error; err != nil {
		t.Fatalf("load alice: %v", err)
	}
	if err := gdb.Where("name = ?", "bob").First(&bob).Error; err != nil {
		t.Fatalf("load bob: %v", err)
	}

	// alice follows bob (state 1 = blog follow).
	if err := gdb.Create(&models.Follow{FollowerID: alice.ID, FollowingID: bob.ID, State: 1, CreatedAt: now}).Error; err != nil {
		t.Fatalf("seed follow: %v", err)
	}

	// Blog: alice's feed_cache has her own post and bob's reblogged post;
	// bob's feed_cache has his own post (which surfaces in alice's feed).
	feeds := []models.FeedCache{
		{PostID: 1, AccountID: alice.ID, CreatedAt: now.Add(-2 * time.Hour)},
		{PostID: 2, AccountID: alice.ID, CreatedAt: now.Add(-1 * time.Hour)},
		{PostID: 2, AccountID: bob.ID, CreatedAt: now.Add(-3 * time.Hour)},
	}
	for i := range feeds {
		if err := gdb.Create(&feeds[i]).Error; err != nil {
			t.Fatalf("seed feed: %v", err)
		}
	}

	// Cache rows (bridge loader requires them) + temp table for payout sort.
	cache := func(postID int64, author, permlink string, payout float64) models.PostCache {
		return models.PostCache{
			PostID: postID, Author: author, Permlink: permlink, Category: "test",
			Title: "t-" + permlink, CreatedAt: now, PayoutAt: now.Add(24 * time.Hour),
			UpdatedAt: now, Payout: payout,
		}
	}
	caches := []models.PostCache{
		cache(1, "alice", "post-a", 9),
		cache(2, "bob", "post-b", 5),
		cache(3, "alice", "comment-x", 1),
		cache(4, "carol", "reply-y", 1),
		cache(5, "mallory", "post-m", 1),
	}
	for i := range caches {
		if err := gdb.Create(&caches[i]).Error; err != nil {
			t.Fatalf("seed cache: %v", err)
		}
		temp := models.PostCacheTemp{
			PostID: caches[i].PostID, Author: caches[i].Author, Permlink: caches[i].Permlink,
			Category: caches[i].Category, Title: caches[i].Title, CreatedAt: now,
			PayoutAt: caches[i].PayoutAt, UpdatedAt: now, Payout: caches[i].Payout,
		}
		if err := gdb.Create(&temp).Error; err != nil {
			t.Fatalf("seed cache temp: %v", err)
		}
	}

	// mallory is author-blocked (list_type 3).
	if err := gdb.Create(&models.PostStatus{Author: "mallory", ListType: models.PostStatusUserBlock, CreatedAt: now}).Error; err != nil {
		t.Fatalf("seed post status: %v", err)
	}
}

// TestGetRankedPosts_CursorSemantics pins the bridge ranked cursor against a
// real Postgres schema, mirroring legacy bridge cursor.pids_by_ranked:
//
//   - #20 depth=0: trending/hot exclude comments even with a higher score;
//   - #21 tiebreaker: pages seek with (field < sval OR (field = sval AND
//     post_id > seek_id)) so same-score posts are neither duplicated nor
//     lost across page boundaries (ORDER BY field DESC, post_id);
//   - promoted has a seek branch (second page differs from the first; the
//     category variant intentionally keeps comments, like legacy);
//   - payout sorts apply the 12-36h payout_at window and filter tags by
//     category;
//   - #22 author-level blocking (hive_posts_status list_type=3) removes the
//     blocked author's posts from the result set;
//   - community mode (tag='all' / 'hive-*'): non-community and grayed posts
//     are excluded, the 1337319 pseudo-community is excluded;
//   - first page of trending prepends admin pins (list_type=2) and pinned
//     posts of the default community;
//   - blog skips the account's own unreblogged community posts.
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set:
//
//	HIVE_TEST_DATABASE_URL=postgresql://test:test@localhost:5433/hive go test -run TestGetRankedPosts_CursorSemantics ./internal/api/bridge/...
func TestGetRankedPosts_CursorSemantics(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping ranked cursor semantics test")
	}
	dbURL := newTestDB(t)

	resetSchema(t, dbURL)
	if err := db.RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	defer sqlDB.Close()

	seedRankedFixture(t, gdb)

	repo := db.NewRepository(gdb)
	wrapped := &db.DB{DB: gdb}
	api := NewRankedAPI(repo, wrapped, nil, "")

	ranked := func(sort, tag, startAuthor, startPermlink string, limit int) []string {
		t.Helper()
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		req := map[string]interface{}{"sort": sort, "limit": limit}
		if tag != "" {
			req["tag"] = tag
		}
		if startAuthor != "" {
			req["start_author"] = startAuthor
		}
		if startPermlink != "" {
			req["start_permlink"] = startPermlink
		}
		params, _ := json.Marshal(req)
		result, err := api.GetRankedPosts(ctx, params)
		if err != nil {
			t.Fatalf("ranked %q tag=%q seek=%s/%s: %v", sort, tag, startAuthor, startPermlink, err)
		}
		posts, ok := result.([]map[string]interface{})
		if !ok {
			t.Fatalf("ranked %q: expected []map, got %T", sort, result)
		}
		out := make([]string, 0, len(posts))
		for _, p := range posts {
			out = append(out, p["permlink"].(string))
		}
		return out
	}
	expect := func(name string, got, want []string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: expected %v, got %v", name, want, got)
		}
	}

	// trending, first page: admin pin + default-community pin prepended to
	// the (tie-broken) score order. The limit applies to the SQL result only;
	// pins are prepended after it, so the first page can exceed the limit
	// (legacy behavior).
	expect("trending p1", ranked("trending", "", "", "", 3),
		[]string{"pin-admin", "pin-comm", "tie-a", "tie-b", "tie-c"})

	// Second page seeks at the last item of page 1 (tie-c, sc_trend tied at
	// 50 with tie-a/tie-b): the two-tuple condition must move strictly past
	// it. The old `sc_trend <= 50` seek re-served tie-a/tie-b here.
	expect("trending p2", ranked("trending", "", "alice", "tie-c", 3),
		[]string{"tie-d", "tie-e", "comm-a"})

	// Seeking mid-tie keeps the remaining tied posts and nothing earlier.
	expect("trending mid-tie seek", ranked("trending", "", "alice", "tie-b", 3),
		[]string{"tie-c", "tie-d", "tie-e"})

	// Third page continues without overlap.
	expect("trending p3", ranked("trending", "", "alice", "comm-a", 3),
		[]string{"comm-b", "comm-excluded", "pin-comm"})
	// Fourth page: the last trend=1 post (pin-admin) then the trend=0 tail.
	expect("trending p4", ranked("trending", "", "alice", "pin-comm", 3),
		[]string{"pin-admin", "pr-a", "pr-b"})

	// depth=0 (#20): comment-hi carries the highest sc_trend/sc_hot but is a
	// comment; it must never surface in trending/hot.
	for _, p := range append(
		ranked("trending", "", "", "", 20),
		ranked("hot", "", "", "", 20)...) {
		if p == "comment-hi" {
			t.Errorf("depth=0: comment-hi must not appear in trending/hot")
		}
	}

	// #22 author blocking: blocked-post (sc_trend 45, would rank between the
	// ties and tie-d) is filtered from every page.
	for _, p := range ranked("trending", "", "", "", 20) {
		if p == "blocked-post" {
			t.Errorf("author-blocked post must not appear in ranked results")
		}
	}

	// hot: same score shape as trending, but pins are NOT prepended (legacy
	// pins trending/created only).
	expect("hot p1", ranked("hot", "", "", "", 3),
		[]string{"tie-a", "tie-b", "tie-c"})

	// promoted: seek by promoted value; the category variant keeps comments
	// (legacy pids_by_category promoted has no depth filter).
	expect("promoted p1", ranked("promoted", "", "", "", 2),
		[]string{"pr-a", "pr-b"})
	expect("promoted p2", ranked("promoted", "", "carol", "pr-b", 2),
		[]string{"pr-c", "pr-comment"})

	// payout: 12-36h payout_at window + unpaid only; ordered by payout
	// (pay-early/pay-late fall outside the window, pay-done is paid out).
	expect("payout global", ranked("payout", "", "", "", 10),
		[]string{"pay-other", "pay-in"})

	// payout tag filter goes by category (pay-* posts carry category 'life'
	// but no hive_post_tags rows at all).
	expect("payout tag=life", ranked("payout", "life", "", "", 10),
		[]string{"pay-in"})
	expect("payout tag=my no observer", ranked("payout", "my", "", "", 10),
		[]string{})

	// muted: grayed posts with pending payout, from the main cache table.
	expect("muted", ranked("muted", "", "", "", 10),
		[]string{"muted-a"})

	// Community mode tag='all': community posts only, grayed and comments
	// excluded, the 1337319 pseudo-community excluded, and both pin
	// prepends apply (legacy `not cids` is falsy for the empty list).
	expect("trending tag=all", ranked("trending", "all", "", "", 10),
		[]string{"pin-admin", "pin-comm", "comm-a", "comm-b", "re-comm"})

	// Community mode tag='hive-123456': only that community's posts (grayed
	// and comments excluded); no admin pin (cids non-empty), community pin
	// resolved to the requested community (none pinned there).
	expect("trending tag=hive-123456", ranked("trending", "hive-123456", "", "", 10),
		[]string{"comm-a"})

	// Blog: the account's own unreblogged community post (comm-a) is
	// skipped; her own plain post and the reblogged community post stay.
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	params, _ := json.Marshal(map[string]interface{}{"sort": "blog", "account": "alice", "limit": 10})
	result, err := api.GetAccountPosts(ctx, params)
	if err != nil {
		t.Fatalf("blog: %v", err)
	}
	blog := result.([]map[string]interface{})
	got := make([]string, 0, len(blog))
	for _, p := range blog {
		got = append(got, p["permlink"].(string))
		if p["permlink"] == "re-comm" {
			rby, ok := p["reblogged_by"].([]string)
			if !ok || len(rby) != 1 || rby[0] != "alice" {
				t.Errorf("blog: re-comm should be reblogged_by [alice], got %v", p["reblogged_by"])
			}
		}
	}
	expect("blog skips unreblogged community post", got, []string{"re-comm", "tie-a"})
}

// seedRankedFixture builds the TestGetRankedPosts_CursorSemantics fixture.
//
// Communities: hive-123456 (id 777). Posts (all with cache + cache_temp rows
// so both table routes resolve):
//
//	tie-a..tie-e (alice): sc_trend/sc_hot 50/50/50/40/30, depth 0
//	comment-hi (bob): depth 1, sc_trend 99 (must not surface in ranked)
//	blocked-post (dave): sc_trend 45, author user-blocked (list_type 3)
//	pr-a..pr-c (carol): promoted 9/8/7; pr-comment (bob): promoted 6, depth 1
//	pay-in (+13h, in window), pay-early (+11h), pay-late (+37h), pay-done
//	  (+13h but is_paidout), pay-other (+14h, category other, payout 11)
//	comm-a (alice, cid 777, trend 20), comm-b (bob, cid 888, trend 15),
//	  comm-gray (carol, cid 777, grayed), comm-comment (alice, depth 1,
//	  trend 25), comm-excluded (eve, cid 1337319), re-comm (eve, cid 999,
//	  reblogged by alice)
//	pin-comm (alice, cid 1317453 = legacy DEFAULT_CID, is_pinned),
//	pin-admin (bob, hive_posts_status list_type 2)
//	muted-a (bob, grayed, payout 5)
//
// Feed cache for alice's blog: tie-a (oldest), comm-a (skipped: own
// unreblogged community post), re-comm (newest, reblogged).
func seedRankedFixture(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()

	for _, name := range []string{"alice", "bob", "carol", "dave", "eve"} {
		if err := gdb.Create(&models.Account{Name: name, CreatedAt: now}).Error; err != nil {
			t.Fatalf("seed account %s: %v", name, err)
		}
	}
	if err := gdb.Create(&models.Community{ID: 777, Name: "hive-123456", Title: "Test", CreatedAt: now}).Error; err != nil {
		t.Fatalf("seed community: %v", err)
	}

	// id, parent, author, permlink, depth, community id (0 = NULL), pinned
	type postSpec struct {
		id, parent                   int64
		author, permlink             string
		depth                        int16
		community                    int64
		pinned                       bool
		trend, hot, payout, promoted float64
		payoutAt                     time.Time
		paidout, grayed              bool
		category                     string
	}
	specs := []postSpec{
		// Trending tie ladder.
		{id: 101, author: "alice", permlink: "tie-a", depth: 0, trend: 50, hot: 50, category: "life"},
		{id: 102, author: "alice", permlink: "tie-b", depth: 0, trend: 50, hot: 50},
		{id: 103, author: "alice", permlink: "tie-c", depth: 0, trend: 50, hot: 50},
		{id: 104, author: "alice", permlink: "tie-d", depth: 0, trend: 40, hot: 40},
		{id: 105, author: "alice", permlink: "tie-e", depth: 0, trend: 30, hot: 30},
		// Highest score but a comment.
		{id: 110, parent: 101, author: "bob", permlink: "comment-hi", depth: 1, trend: 99, hot: 99},
		// Author-blocked.
		{id: 120, author: "dave", permlink: "blocked-post", depth: 0, trend: 45, hot: 45},
		// Promoted ladder + promoted comment.
		{id: 131, author: "carol", permlink: "pr-a", depth: 0, promoted: 9},
		{id: 132, author: "carol", permlink: "pr-b", depth: 0, promoted: 8},
		{id: 133, author: "carol", permlink: "pr-c", depth: 0, promoted: 7},
		{id: 134, parent: 101, author: "bob", permlink: "pr-comment", depth: 1, promoted: 6},
		// Payout window ladder (category 'life' except pay-other).
		{id: 141, author: "alice", permlink: "pay-in", depth: 0, payout: 10, payoutAt: now.Add(13 * time.Hour), category: "life"},
		{id: 142, author: "alice", permlink: "pay-early", depth: 0, payout: 9, payoutAt: now.Add(11 * time.Hour), category: "life"},
		{id: 143, author: "alice", permlink: "pay-late", depth: 0, payout: 8, payoutAt: now.Add(37 * time.Hour), category: "life"},
		{id: 144, author: "alice", permlink: "pay-done", depth: 0, payout: 7, payoutAt: now.Add(13 * time.Hour), paidout: true, category: "life"},
		{id: 146, author: "alice", permlink: "pay-other", depth: 0, payout: 11, payoutAt: now.Add(14 * time.Hour), category: "other"},
		// Community set.
		{id: 151, author: "alice", permlink: "comm-a", depth: 0, community: 777, trend: 20, hot: 20},
		{id: 152, author: "bob", permlink: "comm-b", depth: 0, community: 888, trend: 15, hot: 15},
		{id: 153, author: "carol", permlink: "comm-gray", depth: 0, community: 777, grayed: true},
		{id: 154, parent: 151, author: "alice", permlink: "comm-comment", depth: 1, community: 777, trend: 25, hot: 25},
		{id: 155, author: "eve", permlink: "comm-excluded", depth: 0, community: 1337319, trend: 10, hot: 10},
		{id: 156, author: "eve", permlink: "re-comm", depth: 0, community: 999},
		// Pins.
		{id: 161, author: "alice", permlink: "pin-comm", depth: 0, community: 1317453, pinned: true, trend: 1, hot: 1},
		{id: 162, author: "bob", permlink: "pin-admin", depth: 0, trend: 1, hot: 1},
		// Muted (grayed + pending payout).
		{id: 171, author: "bob", permlink: "muted-a", depth: 0, payout: 5, grayed: true},
	}

	for _, s := range specs {
		p := models.Post{
			Author: s.author, Permlink: s.permlink, Category: "general",
			CreatedAt: now, Depth: s.depth, IsPinned: s.pinned,
		}
		p.ID = s.id
		if s.parent != 0 {
			p.ParentID.Valid = true
			p.ParentID.Int64 = s.parent
		}
		if s.community != 0 {
			p.CommunityID.Valid = true
			p.CommunityID.Int64 = s.community
		}
		if err := gdb.Create(&p).Error; err != nil {
			t.Fatalf("seed post %d: %v", s.id, err)
		}
		cache := models.PostCache{
			PostID: s.id, Author: s.author, Permlink: s.permlink,
			Category: "general", Title: "t-" + s.permlink,
			CreatedAt: now, PayoutAt: now, UpdatedAt: now,
			Depth: s.depth, IsPaidout: s.paidout, IsGrayed: s.grayed,
			SCTrend: s.trend, SCHot: s.hot, Payout: s.payout, Promoted: s.promoted,
		}
		if s.community != 0 {
			cache.CommunityID.Valid = true
			cache.CommunityID.Int64 = s.community
		}
		if !s.payoutAt.IsZero() {
			cache.PayoutAt = s.payoutAt
		}
		if s.category != "" {
			cache.Category = s.category
		}
		if err := gdb.Create(&cache).Error; err != nil {
			t.Fatalf("seed cache %d: %v", s.id, err)
		}
		temp := models.PostCacheTemp{
			PostID: cache.PostID, Author: cache.Author, Permlink: cache.Permlink,
			Category: cache.Category, Title: cache.Title, CreatedAt: now,
			PayoutAt: cache.PayoutAt, UpdatedAt: now, Depth: cache.Depth,
			IsPaidout: cache.IsPaidout, IsGrayed: cache.IsGrayed,
			SCTrend: cache.SCTrend, SCHot: cache.SCHot,
			Payout: cache.Payout, Promoted: cache.Promoted,
		}
		if s.community != 0 {
			temp.CommunityID.Valid = true
			temp.CommunityID.Int64 = s.community
		}
		if err := gdb.Create(&temp).Error; err != nil {
			t.Fatalf("seed cache temp %d: %v", s.id, err)
		}
	}

	// Moderation rows: dave is author-blocked; pin-admin carries a
	// list_type=2 admin pin.
	if err := gdb.Create(&models.PostStatus{Author: "dave", ListType: models.PostStatusUserBlock, CreatedAt: now}).Error; err != nil {
		t.Fatalf("seed post status (user block): %v", err)
	}
	if err := gdb.Create(&models.PostStatus{PostID: 162, ListType: models.PostStatusArticlePin, CreatedAt: now}).Error; err != nil {
		t.Fatalf("seed post status (pin): %v", err)
	}

	// Blog fixture: alice's feed (tie-a oldest, comm-a, re-comm newest) and
	// her reblog of re-comm.
	var alice models.Account
	if err := gdb.Where("name = ?", "alice").First(&alice).Error; err != nil {
		t.Fatalf("load alice: %v", err)
	}
	feeds := []models.FeedCache{
		{PostID: 101, AccountID: alice.ID, CreatedAt: now.Add(-3 * time.Hour)},
		{PostID: 151, AccountID: alice.ID, CreatedAt: now.Add(-2 * time.Hour)},
		{PostID: 156, AccountID: alice.ID, CreatedAt: now.Add(-1 * time.Hour)},
	}
	for i := range feeds {
		if err := gdb.Create(&feeds[i]).Error; err != nil {
			t.Fatalf("seed feed: %v", err)
		}
	}
	if err := gdb.Create(&models.Reblog{Account: "alice", PostID: 156, CreatedAt: now.Add(-1 * time.Hour)}).Error; err != nil {
		t.Fatalf("seed reblog: %v", err)
	}
}
