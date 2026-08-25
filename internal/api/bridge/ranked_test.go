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
