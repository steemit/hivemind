package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/internal/steem"
)

// TestGetPostResiduals verifies the get_post fixes against legacy
// load_posts_keyed (bridge_api/objects.py):
//
//   - community field is the category string (objects.py:135), NOT a
//     `hive-{id}` construction; community_title falls back to the category
//     when the community row has no title (objects.py:136);
//   - stats.hide is the Mutes/irredeemables flag for EVERY post
//     (objects.py:143), replacing the cache row's is_hidden initial value;
//   - stats.gray keeps the cache is_grayed for community posts but is
//     recomputed from blacklists for non-community posts
//     (objects.py:140-142: irredeemables OR >=2 blacklist tags);
//   - the post carries a blacklists key (objects.py:81);
//   - deleted posts are invisible (legacy bridge _get_post_id filters
//     is_deleted = '0', methods.py:30-40).
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set.
func TestGetPostResiduals(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping get_post residuals test")
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

	seedPostResiduals(t, gdb)

	// mutedguy is on the irredeemables list; with reputation 0 its blacklists
	// are [irredeemables, reputation-0] (>=2 tags -> gray).
	muteSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("mutedguy"))
	}))
	defer muteSrv.Close()
	steem.SetSharedMutes(steem.NewMutes(muteSrv.URL))
	t.Cleanup(func() { steem.SetSharedMutes(steem.NewMutes("")) })

	gin.SetMode(gin.TestMode)
	repo := db.NewRepository(gdb)
	api := NewPostAPI(repo)

	getPost := func(author, permlink string) map[string]interface{} {
		t.Helper()
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		raw, _ := json.Marshal(map[string]string{"author": author, "permlink": permlink})
		res, err := api.GetPost(ctx, raw)
		if err != nil {
			t.Fatalf("GetPost %s/%s: %v", author, permlink, err)
		}
		if res == nil {
			return nil
		}
		return res.(map[string]interface{})
	}

	t.Run("community field is the category, not hive-{id}", func(t *testing.T) {
		post := getPost("alice", "in-community")
		// Community id is 123456 but its name (and the post category) is
		// hive-999zzz: legacy reports the category, never hive-123456.
		if post["community"] != "hive-999zzz" {
			t.Errorf("community = %v, want hive-999zzz (category)", post["community"])
		}
		if post["community_title"] != "Titled Community" {
			t.Errorf("community_title = %v, want Titled Community", post["community_title"])
		}
		if post["category"] != "hive-999zzz" {
			t.Errorf("category = %v, want hive-999zzz", post["category"])
		}
		// Community post: stats keep the cache row values (no blacklist
		// override of gray; author is clean so hide stays false).
		stats := post["stats"].(map[string]interface{})
		if stats["gray"] != true { // seeded is_grayed = true
			t.Errorf("community post stats.gray = %v, want true (cache is_grayed)", stats["gray"])
		}
		if stats["hide"] != false {
			t.Errorf("community post stats.hide = %v, want false", stats["hide"])
		}
	})

	t.Run("community_title falls back to category", func(t *testing.T) {
		post := getPost("alice", "untitled-community")
		if post["community_title"] != "hive-777qqq" {
			t.Errorf("community_title = %v, want category fallback hive-777qqq", post["community_title"])
		}
	})

	t.Run("mutes drive hide and gray for non-community posts", func(t *testing.T) {
		// mutedguy: blacklists [irredeemables, reputation-0] -> hide AND gray.
		post := getPost("mutedguy", "muted-post")
		if bl, ok := post["blacklists"].([]string); !ok || len(bl) != 2 {
			t.Fatalf("blacklists = %v, want [irredeemables reputation-0]", post["blacklists"])
		}
		stats := post["stats"].(map[string]interface{})
		if stats["hide"] != true {
			t.Errorf("muted post stats.hide = %v, want true", stats["hide"])
		}
		if stats["gray"] != true {
			t.Errorf("muted post stats.gray = %v, want true (>=2 blacklist tags)", stats["gray"])
		}

		// muted community post: hide applies everywhere; gray keeps is_grayed.
		cpost := getPost("mutedguy", "muted-community-post")
		cstats := cpost["stats"].(map[string]interface{})
		if cstats["hide"] != true {
			t.Errorf("muted community post stats.hide = %v, want true (applies to all posts)", cstats["hide"])
		}
		if cstats["gray"] != false { // seeded is_grayed = false
			t.Errorf("muted community post stats.gray = %v, want false (cache is_grayed)", cstats["gray"])
		}
	})

	t.Run("hide overwrites the cache is_hidden flag", func(t *testing.T) {
		// alice/hidden-flag has is_hidden=true in the cache row, but legacy
		// objects.py:143 overwrites hide with the mutes value (false here).
		post := getPost("alice", "hidden-flag")
		stats := post["stats"].(map[string]interface{})
		if stats["hide"] != false {
			t.Errorf("stats.hide = %v, want false (mutes value overwrites is_hidden)", stats["hide"])
		}
	})

	t.Run("reputation-0 tag alone neither hides nor grays", func(t *testing.T) {
		post := getPost("lowrep", "lowrep-post")
		if bl, ok := post["blacklists"].([]string); !ok || len(bl) != 1 || bl[0] != "reputation-0" {
			t.Fatalf("blacklists = %v, want [reputation-0]", post["blacklists"])
		}
		stats := post["stats"].(map[string]interface{})
		if stats["hide"] != false || stats["gray"] != false {
			t.Errorf("lowrep stats = %v, want hide=false gray=false (single tag)", stats)
		}
	})

	t.Run("deleted post is invisible", func(t *testing.T) {
		if post := getPost("alice", "deleted"); post != nil {
			t.Errorf("deleted post = %v, want nil", post)
		}
	})
}

func seedPostResiduals(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	// Account ids start at 101: the migrations seed accounts at ids 1-4.
	accounts := []models.Account{
		{ID: 101, Name: "alice", CreatedAt: now, ActiveAt: now, Reputation: 50},
		{ID: 102, Name: "mutedguy", CreatedAt: now, ActiveAt: now, Reputation: 0},
		{ID: 103, Name: "lowrep", CreatedAt: now, ActiveAt: now, Reputation: 0},
	}
	for i := range accounts {
		if err := gdb.Create(&accounts[i]).Error; err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	// GORM omits zero-valued fields on Create (column default 25 would win);
	// both mutedguy and lowrep need a deliberate reputation of 0.
	if err := gdb.Model(&models.Account{}).Where("name IN ?", []string{"mutedguy", "lowrep"}).
		Update("reputation", 0).Error; err != nil {
		t.Fatalf("set reputation 0: %v", err)
	}

	// Communities: 123456 named hive-999zzz (id != name suffix — proves the
	// community field is not hive-{id}), and an untitled one.
	comms := []models.Community{
		{ID: 123456, Name: "hive-999zzz", Title: "Titled Community", CreatedAt: now},
		{ID: 777, Name: "hive-777qqq", Title: "", CreatedAt: now},
	}
	for i := range comms {
		if err := gdb.Create(&comms[i]).Error; err != nil {
			t.Fatalf("seed community: %v", err)
		}
	}

	mkPost := func(id int64, author, permlink, category string, communityID int64, deleted bool) models.Post {
		p := models.Post{
			Author: author, Permlink: permlink, Category: category,
			CreatedAt: now, Depth: 0, IsDeleted: deleted,
		}
		p.ID = id
		if communityID != 0 {
			p.CommunityID.Valid = true
			p.CommunityID.Int64 = communityID
		}
		return p
	}
	posts := []models.Post{
		mkPost(1, "alice", "in-community", "hive-999zzz", 123456, false),
		mkPost(2, "alice", "untitled-community", "hive-777qqq", 777, false),
		mkPost(3, "mutedguy", "muted-post", "life", 0, false),
		mkPost(4, "mutedguy", "muted-community-post", "hive-999zzz", 123456, false),
		mkPost(5, "alice", "hidden-flag", "life", 0, false),
		mkPost(6, "lowrep", "lowrep-post", "life", 0, false),
		mkPost(7, "alice", "deleted", "life", 0, true),
	}
	for i := range posts {
		if err := gdb.Create(&posts[i]).Error; err != nil {
			t.Fatalf("seed post: %v", err)
		}
	}

	mkCache := func(postID int64, author, permlink, category string, communityID int64, isHidden, isGrayed bool) models.PostCache {
		c := models.PostCache{
			PostID: postID, Author: author, Permlink: permlink, Category: category,
			CreatedAt: now, PayoutAt: now, UpdatedAt: now,
			IsHidden: isHidden, IsGrayed: isGrayed,
		}
		if communityID != 0 {
			c.CommunityID.Valid = true
			c.CommunityID.Int64 = communityID
		}
		return c
	}
	caches := []models.PostCache{
		mkCache(1, "alice", "in-community", "hive-999zzz", 123456, false, true),
		mkCache(2, "alice", "untitled-community", "hive-777qqq", 777, false, false),
		mkCache(3, "mutedguy", "muted-post", "life", 0, false, false),
		mkCache(4, "mutedguy", "muted-community-post", "hive-999zzz", 123456, false, false),
		mkCache(5, "alice", "hidden-flag", "life", 0, true, false),
		mkCache(6, "lowrep", "lowrep-post", "life", 0, false, false),
	}
	for i := range caches {
		if err := gdb.Create(&caches[i]).Error; err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	}
}
