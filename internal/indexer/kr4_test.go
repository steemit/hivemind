package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/steemit/hivemind/internal/models"
)

func TestCacheSync_Prune(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	old := time.Now().UTC().AddDate(0, 0, -100) // outside 90-day window
	recent := time.Now().UTC().AddDate(0, 0, -1)
	tx := database.DB.WithContext(ctx).Begin()
	tx.Exec(`INSERT INTO hive_posts_cache_temp (post_id, author, permlink, created_at)
	         VALUES (1, 'a', 'p1', ?), (2, 'a', 'p2', ?)`, old, recent)
	tx.Commit()

	cs := NewCacheSync(database.DB)
	cs.Sync(ctx)

	var n int64
	database.DB.WithContext(ctx).Table("hive_posts_cache_temp").Count(&n)
	if n != 1 {
		t.Fatalf("temp rows after prune = %d, want 1 (recent kept)", n)
	}
	var pid int64
	database.DB.WithContext(ctx).
		Table("hive_posts_cache_temp").Select("post_id").Scan(&pid)
	if pid != 2 {
		t.Errorf("surviving row = %d, want 2", pid)
	}
}

func TestAuditCacheMissing(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Depth: 0, IsValid: true}
	database.DB.WithContext(ctx).Create(post)
	// Note: no cache entry (simulating a lost write).

	steemd := steemdPostFor("alice", "p1", "audited")
	provider := &fakeSteemProvider{getContentBatch: func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i := range posts {
			out[i] = steemd
		}
		return out, nil
	}}
	cp := NewCachedPost(database.DB, provider)

	if err := AuditCacheMissing(ctx, database.DB, cp); err != nil {
		t.Fatalf("AuditCacheMissing: %v", err)
	}

	var main models.PostCache
	if err := database.DB.WithContext(ctx).
		Where("post_id = ?", post.ID).First(&main).Error; err != nil {
		t.Fatalf("cache row missing after audit: %v", err)
	}
	if main.Body != "audited" {
		t.Errorf("body = %q", main.Body)
	}
	_ = repo
}

func TestAuditCacheDeleted(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Depth: 0,
		IsValid: true, IsDeleted: true}
	database.DB.WithContext(ctx).Create(post)

	// Stale cache entry the deleted post should not have.
	tx2 := database.DB.WithContext(ctx).Begin()
	tx2.Exec(`INSERT INTO hive_posts_cache (post_id, author, permlink, created_at, updated_at)
	          VALUES (?, 'alice', 'p1', '2026-01-02', '2026-01-02')`, post.ID)
	tx2.Commit()

	cp := NewCachedPost(database.DB, &fakeSteemProvider{})
	if err := AuditCacheDeleted(ctx, database.DB, cp); err != nil {
		t.Fatalf("AuditCacheDeleted: %v", err)
	}

	var n int64
	database.DB.WithContext(ctx).
		Table("hive_posts_cache").Where("post_id = ?", post.ID).Count(&n)
	if n != 0 {
		t.Errorf("stale cache rows = %d, want 0", n)
	}
}

func TestAuditCacheUndelete(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2025, 12, 1, 0, 0, 0, 0, time.UTC), Depth: 0,
		IsValid: true, IsDeleted: true}
	database.DB.WithContext(ctx).Create(post)

	// steemd still serves the post → it was erroneously deleted.
	steemd := steemdPostFor("alice", "p1", "recovered body")
	provider := &fakeSteemProvider{getContentBatch: func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i := range posts {
			out[i] = steemd
		}
		return out, nil
	}}
	cp := NewCachedPost(database.DB, provider)

	if err := AuditCacheUndelete(ctx, database.DB, cp, provider); err != nil {
		t.Fatalf("AuditCacheUndelete: %v", err)
	}

	// hive_posts restored.
	var restored models.Post
	database.DB.WithContext(ctx).Where("id = ?", post.ID).First(&restored)
	if restored.IsDeleted {
		t.Error("post still marked deleted")
	}
	if restored.Category != "life" {
		t.Errorf("category = %q", restored.Category)
	}

	// Cache row restored via the undelete pipeline.
	var main models.PostCache
	if err := database.DB.WithContext(ctx).
		Where("post_id = ?", post.ID).First(&main).Error; err != nil {
		t.Fatalf("cache row not restored: %v", err)
	}
	if main.Body != "recovered body" {
		t.Errorf("body = %q", main.Body)
	}
}
