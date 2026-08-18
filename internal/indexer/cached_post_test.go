package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// steemdPostFor builds a get_content-style post map.
func steemdPostFor(author, permlink, body string) map[string]interface{} {
	return map[string]interface{}{
		"author":                author,
		"permlink":              permlink,
		"category":              "life",
		"depth":                 0,
		"created":               "2026-01-02T03:04:05",
		"last_update":           "2026-01-02T03:04:05",
		"title":                 "Title",
		"body":                  body,
		"json_metadata":         `{"tags":["life","trip"]}`,
		"cashout_time":          "2026-01-09T03:04:05",
		"last_payout":           "1969-12-31T23:59:59",
		"max_accepted_payout":   "1000.000 SBD",
		"percent_steem_dollars": 10000,
		"beneficiaries":         []interface{}{},
		"total_payout_value":    "1.000 SBD",
		"curator_payout_value":  "0.000 SBD",
		"pending_payout_value":  "2.000 SBD",
		"net_rshares":           "3000",
		"children":              1,
		"author_reputation":     "1000000000000",
		"active_votes": []interface{}{
			map[string]interface{}{"voter": "bob", "rshares": "3000", "percent": "10000", "reputation": "0", "time": "2026-01-02T04:00:00"},
		},
	}
}

// TestCachedPost_InsertFlushDualWrite: comment op → Insert → Flush writes
// the post to BOTH cache tables with identical values, plus tags.
func TestCachedPost_InsertFlushDualWrite(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	// Seed account + post (simulating ProcessComment's hive_posts write).
	accountRepo := db.NewAccountRepository(repo)
	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0), Reputation: 25})
	tx.Commit()
	if a, _ := accountRepo.GetByName(ctx, "alice"); a == nil {
		t.Fatal("account seed failed")
	}

	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Depth: 0, IsValid: true}
	if err := database.DB.WithContext(ctx).Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}

	// Fake steemd: one post, fetched by author/permlink pair.
	steemd := steemdPostFor("alice", "p1", "hello @bob")
	provider := &fakeSteemProvider{getContentBatch: func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i, p := range posts {
			if p[0] == "alice" && p[1] == "p1" {
				out[i] = steemd
			} else {
				out[i] = map[string]interface{}{} // blank = not found
			}
		}
		return out, nil
	}}

	cp := NewCachedPost(database.DB, provider)
	cp.Insert("alice", "p1", post.ID)
	if cp.PendingQueueLen() != 1 {
		t.Fatalf("queue len = %d", cp.PendingQueueLen())
	}

	counts, err := cp.Flush(ctx, false)
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if counts["insert"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	if cp.PendingQueueLen() != 0 {
		t.Fatalf("queue not drained: %d", cp.PendingQueueLen())
	}

	// Both tables must have identical rows.
	var main, temp models.PostCache
	if err := database.DB.WithContext(ctx).
		Where("post_id = ?", post.ID).First(&main).Error; err != nil {
		t.Fatalf("main row missing: %v", err)
	}
	if err := database.DB.WithContext(ctx).
		Table("hive_posts_cache_temp").
		Where("post_id = ?", post.ID).First(&temp).Error; err != nil {
		t.Fatalf("temp row missing: %v", err)
	}
	if main.Title != "Title" || main.Body != "hello @bob" {
		t.Errorf("main row = %+v", main)
	}
	if main.Title != temp.Title || main.RShares != temp.RShares ||
		main.SCTrend != temp.SCTrend || main.Body != temp.Body {
		t.Errorf("dual-write mismatch: main=%+v temp=%+v", main, temp)
	}
	if main.RShares != 3000 || main.Children != 1 {
		t.Errorf("computed fields wrong: rshares=%d children=%d", main.RShares, main.Children)
	}
	if main.AuthorRep != 52 {
		t.Errorf("author_rep = %v", main.AuthorRep)
	}
	if main.Votes != "bob,3000,10000,25.0" {
		t.Errorf("votes csv = %q", main.Votes)
	}

	// Tags written (life + trip, deduped, first 5).
	var tags []string
	database.DB.WithContext(ctx).
		Table("hive_post_tags").Select("tag").
		Where("post_id = ?", post.ID).Scan(&tags)
	if len(tags) != 2 {
		t.Errorf("tags = %v", tags)
	}

	// lastID bumped.
	last, _ := cp.LastID(ctx)
	if last != post.ID {
		t.Errorf("lastID = %d, want %d", last, post.ID)
	}
}

// TestCachedPost_UpdateTagDiff: an update with changed tags replaces them.
func TestCachedPost_UpdateTagDiff(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), IsValid: true}
	database.DB.WithContext(ctx).Create(post)

	v1 := steemdPostFor("alice", "p1", "v1")
	v2 := steemdPostFor("alice", "p1", "v2")
	v2["json_metadata"] = `{"tags":["life","newtag"]}`
	current := v1
	provider := &fakeSteemProvider{getContentBatch: func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i := range posts {
			out[i] = current
		}
		return out, nil
	}}

	cp := NewCachedPost(database.DB, provider)
	cp.Insert("alice", "p1", post.ID)
	if _, err := cp.Flush(ctx, false); err != nil {
		t.Fatalf("flush1: %v", err)
	}

	// Update with changed tags.
	current = v2
	cp.Update("alice", "p1", post.ID)
	counts, err := cp.Flush(ctx, false)
	if err != nil {
		t.Fatalf("flush2: %v", err)
	}
	if counts["update"] != 1 {
		t.Fatalf("counts = %v", counts)
	}

	var tags []string
	database.DB.WithContext(ctx).
		Table("hive_post_tags").Select("tag").
		Where("post_id = ?", post.ID).Scan(&tags)
	if len(tags) != 2 {
		t.Fatalf("tags after diff = %v", tags)
	}
	got := map[string]bool{}
	for _, tag := range tags {
		got[tag] = true
	}
	if !got["newtag"] || !got["life"] {
		t.Errorf("tags = %v, want life+newtag", tags)
	}

	// Body updated in both tables.
	var main models.PostCache
	database.DB.WithContext(ctx).Where("post_id = ?", post.ID).First(&main)
	if main.Body != "v2" {
		t.Errorf("body = %q, want v2", main.Body)
	}
}

// TestCachedPost_VoteAndDelete: vote dirties at upvote level; delete evicts
// rows from both tables + tags + queue.
func TestCachedPost_VoteAndDelete(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	tx.Create(&models.Account{Name: "bob", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), IsValid: true}
	database.DB.WithContext(ctx).Create(post)

	steemd := steemdPostFor("alice", "p1", "body")
	provider := &fakeSteemProvider{getContentBatch: func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i := range posts {
			out[i] = steemd
		}
		return out, nil
	}}
	cp := NewCachedPost(database.DB, provider)

	cp.Insert("alice", "p1", post.ID)
	if _, err := cp.Flush(ctx, false); err != nil {
		t.Fatalf("flush1: %v", err)
	}

	// Vote with unknown pid → resolved via noids lookup at flush.
	cp.Vote("alice", "p1", 0, "bob")
	counts, err := cp.Flush(ctx, false)
	if err != nil {
		t.Fatalf("flush2: %v", err)
	}
	if counts["upvote"] != 1 {
		t.Fatalf("counts = %v", counts)
	}

	// Delete evicts everything.
	cp.Vote("alice", "p1", post.ID, "carol") // queue an entry to verify dequeue
	if err := cp.Delete(ctx, post.ID, "alice", "p1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if cp.PendingQueueLen() != 0 {
		t.Errorf("queue should be empty after delete, got %d", cp.PendingQueueLen())
	}
	var n int64
	database.DB.WithContext(ctx).Table("hive_posts_cache").
		Where("post_id = ?", post.ID).Count(&n)
	if n != 0 {
		t.Errorf("main rows after delete = %d", n)
	}
	database.DB.WithContext(ctx).Table("hive_posts_cache_temp").
		Where("post_id = ?", post.ID).Count(&n)
	if n != 0 {
		t.Errorf("temp rows after delete = %d", n)
	}
	database.DB.WithContext(ctx).Table("hive_post_tags").
		Where("post_id = ?", post.ID).Count(&n)
	if n != 0 {
		t.Errorf("tag rows after delete = %d", n)
	}
}

// TestCachedPost_DeferredBlankPost: a blank steemd response re-queues the
// post instead of writing an empty row.
func TestCachedPost_DeferredBlankPost(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), IsValid: true}
	database.DB.WithContext(ctx).Create(post)

	provider := &fakeSteemProvider{getContentBatch: func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i := range posts {
			out[i] = map[string]interface{}{} // blank: not found
		}
		return out, nil
	}}
	cp := NewCachedPost(database.DB, provider)
	cp.Insert("alice", "p1", post.ID)
	if _, err := cp.Flush(ctx, false); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if cp.PendingQueueLen() != 1 {
		t.Errorf("blank post should be re-queued (DEFER), queue = %d", cp.PendingQueueLen())
	}
	var n int64
	database.DB.WithContext(ctx).Table("hive_posts_cache").Count(&n)
	if n != 0 {
		t.Errorf("no rows should be written for blank post, got %d", n)
	}
}

// TestCachedPost_RecoverMissing: posts in hive_posts but not in cache are
// inserted by RecoverMissingPosts.
func TestCachedPost_RecoverMissing(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), IsValid: true}
	database.DB.WithContext(ctx).Create(post)

	steemd := steemdPostFor("alice", "p1", "recovered")
	provider := &fakeSteemProvider{getContentBatch: func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i := range posts {
			out[i] = steemd
		}
		return out, nil
	}}
	cp := NewCachedPost(database.DB, provider)

	if err := cp.RecoverMissingPosts(ctx); err != nil {
		t.Fatalf("RecoverMissingPosts: %v", err)
	}

	var main models.PostCache
	if err := database.DB.WithContext(ctx).
		Where("post_id = ?", post.ID).First(&main).Error; err != nil {
		t.Fatalf("recovered row missing: %v", err)
	}
	if main.Body != "recovered" {
		t.Errorf("body = %q", main.Body)
	}
	var temp models.PostCacheTemp
	if err := database.DB.WithContext(ctx).
		Table("hive_posts_cache_temp").
		Where("post_id = ?", post.ID).First(&temp).Error; err != nil {
		t.Fatalf("recovered temp row missing: %v", err)
	}
}
