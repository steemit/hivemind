package condenser

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

func TestParseQueryParams(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		assert func(t *testing.T, p map[string]interface{}, err error)
	}{
		{
			name: "object form",
			raw:  `{"tag": "steem", "limit": 1}`,
			assert: func(t *testing.T, p map[string]interface{}, err error) {
				if err != nil || p["tag"] != "steem" || p["limit"] != float64(1) {
					t.Fatalf("got %v err=%v", p, err)
				}
			},
		},
		{
			name: "nested query form (nested_query_compat)",
			raw:  `[{"tag": "steem", "limit": 1}]`,
			assert: func(t *testing.T, p map[string]interface{}, err error) {
				if err != nil || p["tag"] != "steem" || p["limit"] != float64(1) {
					t.Fatalf("got %v err=%v", p, err)
				}
			},
		},
		{
			name: "positional form",
			raw:  `["steem", "alice", "plink", 5]`,
			assert: func(t *testing.T, p map[string]interface{}, err error) {
				if err != nil || p["tag"] != "steem" || p["start_author"] != "alice" ||
					p["start_permlink"] != "plink" || p["limit"] != float64(5) {
					t.Fatalf("got %v err=%v", p, err)
				}
			},
		},
		{
			name: "positional form with object start",
			raw:  `["steem", "alice"]`,
			assert: func(t *testing.T, p map[string]interface{}, err error) {
				if err != nil || p["tag"] != "steem" || p["start_author"] != "alice" {
					t.Fatalf("got %v err=%v", p, err)
				}
			},
		},
		{
			name: "empty array",
			raw:  `[]`,
			assert: func(t *testing.T, p map[string]interface{}, err error) {
				if err != nil || len(p) != 0 {
					t.Fatalf("got %v err=%v", p, err)
				}
			},
		},
		{
			name: "invalid form",
			raw:  `"steem"`,
			assert: func(t *testing.T, p map[string]interface{}, err error) {
				if err == nil {
					t.Fatalf("expected error, got %v", p)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseQueryParams(json.RawMessage(tc.raw), "tag", "start_author", "start_permlink", "limit")
			tc.assert(t, p, err)
		})
	}
}

func TestParseListParams(t *testing.T) {
	if _, err := parseListParams(json.RawMessage(`["a", "b"]`), 3, 2); err != nil {
		t.Errorf("min window: %v", err)
	}
	if _, err := parseListParams(json.RawMessage(`["a", "b", "c"]`), 3, 2); err != nil {
		t.Errorf("exact len: %v", err)
	}
	if _, err := parseListParams(json.RawMessage(`["a"]`), 3, 2); err == nil {
		t.Errorf("below min: expected error")
	}
	if _, err := parseListParams(json.RawMessage(`["a","b","c","d"]`), 3, 2); err == nil {
		t.Errorf("above max: expected error")
	}
	if _, err := parseListParams(json.RawMessage(`{"a":1}`), 3, 3); err == nil {
		t.Errorf("object form: expected error")
	}
}

// newTestDB creates a dedicated database (hive_condenser_test) for this package.
func newTestDB(t *testing.T) string {
	t.Helper()
	baseURL := os.Getenv("HIVE_TEST_DATABASE_URL")

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse db url: %v", err)
	}
	u.Path = "/postgres"
	maintDSN := u.String()

	raw, err := sql.Open("pgx", maintDSN)
	if err != nil {
		t.Fatalf("open maintenance conn: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP DATABASE IF EXISTS hive_condenser_test;`); err != nil {
		t.Fatalf("drop test db: %v", err)
	}
	if _, err := raw.Exec(`CREATE DATABASE hive_condenser_test;`); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	u.Path = "/hive_condenser_test"
	return u.String()
}

// call invokes a condenser API handler with raw JSON params.
func call(t *testing.T, handler func(*gin.Context, json.RawMessage) (interface{}, error), raw string) interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	result, err := handler(ctx, json.RawMessage(raw))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

// TestMiscBlogTagsMethods covers the condenser misc/blog/tags methods
// against a real Postgres schema. Requires HIVE_TEST_DATABASE_URL.
func TestMiscBlogTagsMethods(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping condenser integration test")
	}
	dbURL := newTestDB(t)
	raw, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	if _, err := raw.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	raw.Close()
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

	seedCondenserFixture(t, gdb)

	gin.SetMode(gin.TestMode)
	repo := db.NewRepository(gdb)
	wrapped := &db.DB{DB: gdb}
	misc := NewMiscAPI(repo, wrapped, nil)
	blog := NewBlogAPI(repo, wrapped)
	tags := NewTagsAPI(repo, wrapped)

	// get_discussions_by_comments: alice's comments only.
	comments := call(t, misc.GetDiscussionsByComments,
		`{"start_author": "alice", "limit": 10}`).([]map[string]interface{})
	if len(comments) != 1 || comments[0]["permlink"] != "comment-x" {
		t.Fatalf("by_comments: got %v", comments)
	}
	// Nested query form must parse identically (parameter shape unification).
	commentsNested := call(t, misc.GetDiscussionsByComments,
		`[{"start_author": "alice", "limit": 10}]`).([]map[string]interface{})
	if len(commentsNested) != len(comments) {
		t.Fatalf("by_comments nested form: got %d, want %d", len(commentsNested), len(comments))
	}

	// get_replies_by_last_update: replies to alice's posts.
	replies := call(t, misc.GetRepliesByLastUpdate,
		`["alice", "", 10]`).([]map[string]interface{})
	if len(replies) != 1 || replies[0]["permlink"] != "reply-y" {
		t.Fatalf("replies_by_last_update: got %v", replies)
	}

	// get_discussions_by_author_before_date: alice's posts, no reblogs.
	before := call(t, misc.GetDiscussionsByAuthorBeforeDate,
		`["alice", "", "", 10]`).([]map[string]interface{})
	if len(before) != 1 || before[0]["permlink"] != "post-a" {
		t.Fatalf("by_author_before_date: got %v", before)
	}

	// get_blog: entry_id pagination; entry ids descend from the start index.
	blogFull := call(t, blog.GetBlog, `["alice", -0, 0]`).([]map[string]interface{})
	// With limit 0 -> limit = start_index+1 = 1: only the newest entry.
	if len(blogFull) != 1 {
		t.Fatalf("get_blog: expected 1 entry, got %v", blogFull)
	}
	if blogFull[0]["entry_id"] != int(1) || blogFull[0]["blog"] != "alice" {
		t.Fatalf("get_blog entry: got %v", blogFull[0])
	}
	if c, ok := blogFull[0]["comment"].(map[string]interface{}); !ok || c["permlink"] != "post-b" {
		t.Fatalf("get_blog comment: got %v", blogFull[0]["comment"])
	}

	blogAll := call(t, blog.GetBlog, `["alice", 0, 2]`).([]map[string]interface{})
	if len(blogAll) != 2 {
		t.Fatalf("get_blog all: expected 2 entries, got %v", blogAll)
	}
	if blogAll[0]["entry_id"] != int(1) || blogAll[1]["entry_id"] != int(0) {
		t.Fatalf("get_blog entry ids descending: got %v", blogAll)
	}
	if blogAll[0]["reblogged_on"] == "1970-01-01T00:00:00" {
		t.Errorf("get_blog: reblogged entry should carry a timestamp")
	}

	// get_blog_entries: lightweight shape.
	entries := call(t, blog.GetBlogEntries, `["alice", 0, 2]`).([]map[string]interface{})
	if len(entries) != 2 {
		t.Fatalf("get_blog_entries: got %v", entries)
	}
	if _, has := entries[0]["comment"]; has {
		t.Errorf("get_blog_entries: comment key should be stripped")
	}
	if entries[0]["author"] != "bob" || entries[0]["permlink"] != "post-b" {
		t.Fatalf("get_blog_entries[0]: got %v", entries[0])
	}

	// get_trending_tags: aggregate over pending posts.
	tt := call(t, tags.GetTrendingTags, `["", 10]`).([]map[string]interface{})
	if len(tt) != 1 || tt[0]["name"] != "test" {
		t.Fatalf("get_trending_tags: got %v", tt)
	}
	if tt[0]["top_posts"] != int64(2) || tt[0]["comments"] != int64(2) {
		t.Fatalf("get_trending_tags counts: got %v", tt[0])
	}

	// get_state stays an explicit refusal.
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	if _, err := misc.GetState(ctx, json.RawMessage(`["trending"]`)); err == nil {
		t.Errorf("get_state must return an explicit not-implemented error")
	}
}

// seedCondenserFixture builds alice/bob/carol accounts, posts (1 root +
// reblog for the blog, 1 comment, 1 reply), cache rows and feed entries.
func seedCondenserFixture(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()

	for _, name := range []string{"alice", "bob", "carol"} {
		if err := gdb.Create(&models.Account{Name: name, CreatedAt: now}).Error; err != nil {
			t.Fatalf("seed account %s: %v", name, err)
		}
	}
	var aliceID int64
	if err := gdb.Raw("SELECT id FROM hive_accounts WHERE name = 'alice'").Scan(&aliceID).Error; err != nil {
		t.Fatalf("load alice: %v", err)
	}

	mk := func(id int64, parent int64, author, permlink string, depth int16) models.Post {
		p := models.Post{Author: author, Permlink: permlink, Category: "test", CreatedAt: now, Depth: depth}
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
	}
	for i := range posts {
		if err := gdb.Create(&posts[i]).Error; err != nil {
			t.Fatalf("seed post: %v", err)
		}
	}

	caches := []models.PostCache{
		{PostID: 1, Author: "alice", Permlink: "post-a", Category: "test", Title: "A", CreatedAt: now, PayoutAt: now, UpdatedAt: now, Depth: 0, Payout: 3},
		{PostID: 2, Author: "bob", Permlink: "post-b", Category: "test", Title: "B", CreatedAt: now, PayoutAt: now, UpdatedAt: now, Depth: 0, Payout: 5},
		{PostID: 3, Author: "alice", Permlink: "comment-x", Category: "test", Title: "C", CreatedAt: now, PayoutAt: now, UpdatedAt: now, Depth: 1, Payout: 1},
		{PostID: 4, Author: "carol", Permlink: "reply-y", Category: "test", Title: "D", CreatedAt: now, PayoutAt: now, UpdatedAt: now, Depth: 1, Payout: 1},
	}
	for i := range caches {
		if err := gdb.Create(&caches[i]).Error; err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	}

	feeds := []models.FeedCache{
		{PostID: 1, AccountID: aliceID, CreatedAt: now.Add(-2 * time.Hour)},
		{PostID: 2, AccountID: aliceID, CreatedAt: now.Add(-1 * time.Hour)},
	}
	for i := range feeds {
		if err := gdb.Create(&feeds[i]).Error; err != nil {
			t.Fatalf("seed feed: %v", err)
		}
	}
}
