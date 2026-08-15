package bridge

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

// newTestDB creates a dedicated database (hive_bridge_test) for this package.
// Other packages' integration tests (internal/db, internal/indexer) reset the
// shared schema from HIVE_TEST_DATABASE_URL too; running `go test ./...`
// would otherwise have concurrent packages dropping each other's schema.
func newTestDB(t *testing.T) string {
	t.Helper()
	baseURL := os.Getenv("HIVE_TEST_DATABASE_URL")

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse db url: %v", err)
	}
	u.Path = "/postgres"
	maintDSN := u.String()

	raw, err := sqlOpen(maintDSN)
	if err != nil {
		t.Fatalf("open maintenance conn: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP DATABASE IF EXISTS hive_bridge_test;`); err != nil {
		t.Fatalf("drop test db: %v", err)
	}
	if _, err := raw.Exec(`CREATE DATABASE hive_bridge_test;`); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	u.Path = "/hive_bridge_test"
	return u.String()
}

// TestGetDiscussionHideFilter verifies the hive_posts_status moderation
// filtering against a real Postgres schema:
//
//   - discussions whose root author is user-blocked (list_type=3) return empty,
//   - discussions whose root post is article-blocked (list_type=1) return empty,
//   - deleted roots return empty,
//   - and within a visible tree, blocked replies (post-level or by blocked
//     authors) are pruned together with their whole subtrees.
//
// Mirrors legacy hive/server/bridge_api/thread.py behavior. Requires a live
// Postgres; skipped when HIVE_TEST_DATABASE_URL is not set:
//
//	HIVE_TEST_DATABASE_URL=postgresql://test:test@localhost:5433/hive go test -run TestGetDiscussionHideFilter ./internal/api/bridge/...
func TestGetDiscussionHideFilter(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping get_discussion hide-filter test")
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

	seedDiscussionTree(t, gdb)

	repo := db.NewRepository(gdb)
	api := NewPostAPI(repo)

	cases := []struct {
		name    string
		author  string
		wantNll bool
		wantIDs map[string]bool // "author/permlink" keys expected in the result
	}{
		{
			name:    "visible tree with blocked reply and blocked-author reply pruned",
			author:  "alice",
			wantNll: false,
			wantIDs: map[string]bool{
				"alice/root": true,
				"bob/r1":     true,
				"carol/r1a":  true,
			},
		},
		{
			name:    "root post article-blocked returns empty",
			author:  "grace",
			wantNll: true,
		},
		{
			name:    "root author user-blocked returns empty",
			author:  "hank",
			wantNll: true,
		},
		{
			name:    "deleted root returns empty",
			author:  "iris",
			wantNll: true,
		},
		{
			name:    "unknown root returns empty",
			author:  "nobody",
			wantNll: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(w)
			ctx.Request = httptest.NewRequest("POST", "/", nil)

			params, _ := json.Marshal(map[string]string{
				"author":   tc.author,
				"permlink": "root",
			})
			result, err := api.GetDiscussion(ctx, params)
			if err != nil {
				t.Fatalf("GetDiscussion: %v", err)
			}
			if tc.wantNll {
				if result != nil {
					t.Fatalf("expected empty (nil) result, got %v", result)
				}
				return
			}
			discussion, ok := result.(map[string]interface{})
			if !ok {
				t.Fatalf("expected map result, got %T", result)
			}
			if len(discussion) != len(tc.wantIDs) {
				t.Fatalf("expected %d posts, got %d: %v", len(tc.wantIDs), len(discussion), discussion)
			}
			for key := range tc.wantIDs {
				if _, ok := discussion[key]; !ok {
					t.Errorf("expected post %q in discussion, missing", key)
				}
			}
		})
	}
}

// seedDiscussionTree inserts one visible 3-level tree under alice/root plus
// moderation cases: a blocked reply (with its own child, proving subtree
// pruning), a reply by a blocked author, and three hidden/deleted roots.
func seedDiscussionTree(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()

	// hive_posts.author has an FK to hive_accounts(name); seed authors first.
	for _, name := range []string{"alice", "bob", "carol", "dan", "frank", "eve", "grace", "hank", "iris"} {
		if err := gdb.Create(&models.Account{Name: name, CreatedAt: now}).Error; err != nil {
			t.Fatalf("seed account %s: %v", name, err)
		}
	}

	mk := func(id int64, parent int64, author, permlink string, depth int16, deleted bool) models.Post {
		p := models.Post{
			Author:    author,
			Permlink:  permlink,
			Category:  "test",
			CreatedAt: now,
			Depth:     depth,
			IsDeleted: deleted,
		}
		p.ID = id
		if parent != 0 {
			p.ParentID.Valid = true
			p.ParentID.Int64 = parent
		}
		return p
	}
	posts := []models.Post{
		// Visible tree: alice/root -> bob/r1 -> carol/r1a.
		mk(1, 0, "alice", "root", 0, false),
		mk(2, 1, "bob", "r1", 1, false),
		mk(3, 2, "carol", "r1a", 2, false),
		// Blocked reply with a child: both must be pruned.
		mk(4, 1, "dan", "r2", 1, false),
		mk(5, 4, "frank", "r2a", 2, false),
		// Reply by a user-blocked author: pruned.
		mk(6, 1, "eve", "r3", 1, false),
		// Roots for the hidden/deleted cases.
		mk(7, 0, "grace", "root", 0, false),
		mk(8, 0, "hank", "root", 0, false),
		mk(9, 0, "iris", "root", 0, true),
	}
	for i := range posts {
		if err := gdb.Create(&posts[i]).Error; err != nil {
			t.Fatalf("seed post %+v: %v", posts[i], err)
		}
	}

	// LoadPostsKeyed requires a hive_posts_cache row per post (written by the
	// indexer in production). Seed every visible-tree post, including the
	// blocked ones, to prove pruning happens regardless of cache presence.
	for _, p := range posts {
		if p.IsDeleted {
			continue
		}
		cacheRow := models.PostCache{
			PostID:    p.ID,
			Author:    p.Author,
			Permlink:  p.Permlink,
			Category:  p.Category,
			CreatedAt: now,
			PayoutAt:  now,
			UpdatedAt: now,
		}
		if err := gdb.Create(&cacheRow).Error; err != nil {
			t.Fatalf("seed post_cache %+v: %v", cacheRow, err)
		}
	}

	statuses := []models.PostStatus{
		{ID: 1, PostID: 4, Author: "dan", ListType: models.PostStatusArticleBlock, CreatedAt: now}, // block dan/r2
		{ID: 2, PostID: 0, Author: "eve", ListType: models.PostStatusUserBlock, CreatedAt: now},    // block all eve posts
		{ID: 3, PostID: 7, Author: "grace", ListType: models.PostStatusArticleBlock, CreatedAt: now},
		{ID: 4, PostID: 0, Author: "hank", ListType: models.PostStatusUserBlock, CreatedAt: now},
	}
	for i := range statuses {
		if err := gdb.Create(&statuses[i]).Error; err != nil {
			t.Fatalf("seed status %+v: %v", statuses[i], err)
		}
	}
}

// sqlOpen opens a raw *sql.DB via the pgx stdlib driver (registered by
// gorm.io/driver/postgres).
func sqlOpen(dbURL string) (*sql.DB, error) {
	return sql.Open("pgx", dbURL)
}

// resetSchema drops and recreates the public schema so migrations run clean.
func resetSchema(t *testing.T, dbURL string) {
	t.Helper()
	raw, err := sqlOpen(dbURL)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
}
