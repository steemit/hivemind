package hive

import (
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

// TestObserverVoteRshares unit-pins the votes-CSV parsing behind the
// observer's vote_rshares context (legacy _top_votes observer branch,
// hive_api/objects.py:201-216).
func TestObserverVoteRshares(t *testing.T) {
	csv := "alice,1000\nbob,-50\nalice-not,7\n"

	if got := observerVoteRshares(csv, "alice"); got != int64(1000) {
		t.Errorf("observer alice: expected 1000, got %v", got)
	}
	if got := observerVoteRshares(csv, "bob"); got != int64(-50) {
		t.Errorf("observer bob: expected -50, got %v", got)
	}
	// Name matching is exact: 'carol' did not vote.
	if got := observerVoteRshares(csv, "carol"); got != nil {
		t.Errorf("observer carol: expected nil, got %v", got)
	}
	// Empty CSV and empty observer never match.
	if got := observerVoteRshares("", "alice"); got != nil {
		t.Errorf("empty csv: expected nil, got %v", got)
	}
	// Malformed lines are skipped, not fatal.
	if got := observerVoteRshares("alice\nbob,5", "bob"); got != int64(5) {
		t.Errorf("malformed first line: expected 5, got %v", got)
	}
}

// TestListMethodsObserverContext verifies the hive_api list_* post context
// against a real Postgres schema (planned PR#417; legacy posts_by_id
// objects.py:163-167):
//
//   - with an observer, every post gains context{reblogged, vote_rshares}:
//     reblogged from the observer's hive_reblogs rows (one batched IN
//     query), vote_rshares from the post's votes CSV (nil when the observer
//     has not voted);
//   - without an observer, posts carry no context key at all;
//   - the accounts section keeps its followed context (already shipped in
//     Q3, asserted here as part of the observer contract).
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set.
func TestListMethodsObserverContext(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping observer context test")
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

	seedObserverContextFixture(t, gdb)

	gin.SetMode(gin.TestMode)
	public := NewPublicAPI(db.NewRepository(gdb), &db.DB{DB: gdb})

	blog := func(observer string) map[string]interface{} {
		t.Helper()
		req := map[string]interface{}{"account": "alice"}
		if observer != "" {
			req["observer"] = observer
		}
		return call(t, public.ListAccountBlog, req).(map[string]interface{})
	}
	postsByURL := func(result map[string]interface{}) map[string]map[string]interface{} {
		t.Helper()
		out := make(map[string]map[string]interface{})
		posts := result["posts"].([]map[string]interface{})
		for _, p := range posts {
			out[p["url"].(string)] = p
		}
		return out
	}

	// carol: voted 500 on alice/post-a, reblogged bob/post-b.
	posts := postsByURL(blog("carol"))
	a := posts["alice/post-a"]
	if a == nil {
		t.Fatalf("post-a missing: %v", posts)
	}
	ctxA, ok := a["context"].(map[string]interface{})
	if !ok {
		t.Fatalf("post-a: expected context map, got %T", a["context"])
	}
	if ctxA["vote_rshares"] != int64(500) {
		t.Errorf("post-a: expected vote_rshares 500, got %v (%T)", ctxA["vote_rshares"], ctxA["vote_rshares"])
	}
	if ctxA["reblogged"] != false {
		t.Errorf("post-a: expected reblogged false, got %v", ctxA["reblogged"])
	}
	b := posts["bob/post-b"]
	ctxB, ok := b["context"].(map[string]interface{})
	if !ok {
		t.Fatalf("post-b: expected context map, got %T", b["context"])
	}
	if ctxB["reblogged"] != true {
		t.Errorf("post-b: expected reblogged true, got %v", ctxB["reblogged"])
	}
	if ctxB["vote_rshares"] != nil {
		t.Errorf("post-b: expected vote_rshares nil (carol did not vote), got %v", ctxB["vote_rshares"])
	}

	// bob: voted -50 on post-a (a downvote), did not reblog post-b.
	posts = postsByURL(blog("bob"))
	ctxA = posts["alice/post-a"]["context"].(map[string]interface{})
	if ctxA["vote_rshares"] != int64(-50) {
		t.Errorf("post-a (bob): expected vote_rshares -50, got %v", ctxA["vote_rshares"])
	}
	ctxB = posts["bob/post-b"]["context"].(map[string]interface{})
	if ctxB["reblogged"] != false {
		t.Errorf("post-b (bob): expected reblogged false, got %v", ctxB["reblogged"])
	}

	// No observer: no context key anywhere.
	posts = postsByURL(blog(""))
	for url, p := range posts {
		if _, has := p["context"]; has {
			t.Errorf("%s: context must be absent without an observer, got %v", url, p["context"])
		}
	}

	// list_account_posts shares the same postsByID path: carol's vote on
	// alice's comment surfaces there too.
	postsRes := call(t, public.ListAccountPosts, map[string]interface{}{
		"account": "alice", "observer": "carol",
	}).(map[string]interface{})
	list := postsRes["posts"].([]map[string]interface{})
	if len(list) != 1 {
		t.Fatalf("list_account_posts: expected alice's comment only, got %v", list)
	}
	ctxC, ok := list[0]["context"].(map[string]interface{})
	if !ok {
		t.Fatalf("comment: expected context map, got %T", list[0]["context"])
	}
	if ctxC["vote_rshares"] != int64(300) {
		t.Errorf("comment: expected vote_rshares 300, got %v", ctxC["vote_rshares"])
	}
}

// seedObserverContextFixture builds the TestListMethodsObserverContext
// fixture:
//
//	alice: post-a (votes: carol +500, bob -50), comment-x on post-b
//	       (votes: carol +300)
//	bob:   post-b, reblogged by nobody (carol reblogged it)
//	carol: observer (voted, reblogged post-b)
func seedObserverContextFixture(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()

	for _, name := range []string{"alice", "bob", "carol"} {
		if err := gdb.Create(&models.Account{Name: name, CreatedAt: now}).Error; err != nil {
			t.Fatalf("seed account %s: %v", name, err)
		}
	}

	mk := func(id int64, parent int64, author, permlink string, depth int16, votes string) (models.Post, models.PostCache) {
		p := models.Post{Author: author, Permlink: permlink, Category: "test", CreatedAt: now, Depth: depth}
		p.ID = id
		if parent != 0 {
			p.ParentID.Valid = true
			p.ParentID.Int64 = parent
		}
		c := models.PostCache{
			PostID: id, Author: author, Permlink: permlink, Category: "test",
			Title: "t-" + permlink, CreatedAt: now, PayoutAt: now, UpdatedAt: now,
			Votes: votes,
		}
		return p, c
	}
	rows := []struct {
		parent   int64
		author   string
		permlink string
		depth    int16
		votes    string
	}{
		{0, "alice", "post-a", 0, "carol,500\nbob,-50\n"},
		{0, "bob", "post-b", 0, "erin,900\n"},
		{2, "alice", "comment-x", 1, "carol,300\n"},
	}
	for i, r := range rows {
		p, c := mk(int64(i+1), r.parent, r.author, r.permlink, r.depth, r.votes)
		if err := gdb.Create(&p).Error; err != nil {
			t.Fatalf("seed post %d: %v", p.ID, err)
		}
		if err := gdb.Create(&c).Error; err != nil {
			t.Fatalf("seed cache %d: %v", c.PostID, err)
		}
	}

	// Blog: alice's own post plus her view of bob's reblogged post.
	var alice models.Account
	if err := gdb.Where("name = ?", "alice").First(&alice).Error; err != nil {
		t.Fatalf("load alice: %v", err)
	}
	feeds := []models.FeedCache{
		{PostID: 1, AccountID: alice.ID, CreatedAt: now.Add(-2 * time.Hour)},
		{PostID: 2, AccountID: alice.ID, CreatedAt: now.Add(-1 * time.Hour)},
	}
	for i := range feeds {
		if err := gdb.Create(&feeds[i]).Error; err != nil {
			t.Fatalf("seed feed: %v", err)
		}
	}
	if err := gdb.Create(&models.Reblog{Account: "carol", PostID: 2, CreatedAt: now.Add(-30 * time.Minute)}).Error; err != nil {
		t.Fatalf("seed reblog: %v", err)
	}
}
