package condenser

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// openCursorTestDB provisions a fresh dedicated database and returns a gorm
// handle plus a cursor over it.
func openCursorTestDB(t *testing.T) (*gorm.DB, *Cursor) {
	t.Helper()
	dbURL := newNamedTestDB(t, "hive_condenser_cursor_test")

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
	t.Cleanup(func() {
		if sqlDB, err := gdb.DB(); err == nil {
			sqlDB.Close()
		}
	})
	return gdb, NewCursor(gdb)
}

// TestCursorRepliesPaginationRebind pins the 2.8a fix: when paging
// pids_by_replies_to_account (condenser get_replies_by_last_update and
// bridge.get_account_posts sort=replies), the second page must rebind the
// parent-account subquery parameter to the seek post's parent author
// (legacy cursor.py:525 `parent_account = row[0]`). Keeping startAuthor (the
// reply's own author) queries the wrong parent set from page 2 on.
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set.
func TestCursorRepliesPaginationRebind(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping replies rebind test")
	}
	gdb, cursor := openCursorTestDB(t)
	now := time.Now().UTC()

	// Replies fixture:
	//
	//	alice/parent-post (10) <- bob/reply-2 (12), bob/reply-1 (11)
	//	bob/own-post (13)      <- carol/other-reply (14)
	//
	// Page 1 (alice, limit 1) -> [reply-2]. Page 2 seeks at bob/reply-2:
	// rebinding the subquery to alice yields [reply-1]; the pre-fix behavior
	// (subquery stays bob) yields [other-reply].
	mkPost := func(id, parent int64, author, permlink string) models.Post {
		p := models.Post{Author: author, Permlink: permlink, Category: "test", CreatedAt: now}
		p.ID = id
		if parent != 0 {
			p.ParentID.Valid = true
			p.ParentID.Int64 = parent
		}
		return p
	}
	posts := []models.Post{
		mkPost(10, 0, "alice", "parent-post"),
		mkPost(11, 10, "bob", "reply-1"),
		mkPost(12, 10, "bob", "reply-2"),
		mkPost(13, 0, "bob", "own-post"),
		mkPost(14, 13, "carol", "other-reply"),
	}
	for _, name := range []string{"alice", "bob", "carol"} {
		if err := gdb.Create(&models.Account{Name: name, CreatedAt: now}).Error; err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}
	for i := range posts {
		if err := gdb.Create(&posts[i]).Error; err != nil {
			t.Fatalf("seed post: %v", err)
		}
		cache := models.PostCache{
			PostID: posts[i].ID, Author: posts[i].Author, Permlink: posts[i].Permlink,
			Category: "test", Title: "t", CreatedAt: now, PayoutAt: now, UpdatedAt: now,
			Depth: posts[i].Depth,
		}
		if posts[i].ParentID.Valid {
			cache.Depth = 1
		}
		if err := gdb.Create(&cache).Error; err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	}

	// Cursor level.
	page1, err := cursor.GetPostIDsByRepliesToAccount(context.Background(), "alice", "", 1)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 1 || page1[0] != 12 {
		t.Fatalf("page1: expected [12], got %v", page1)
	}
	// Page 2 seeks at bob/reply-2: rebinding the subquery to alice yields
	// alice's remaining replies (the seek post itself included — legacy seek
	// is the inclusive `id <= start_id`). The pre-fix behavior (subquery
	// stays bob, so the seek resolves to bob/reply-2 and prunes the
	// result by `id <= 14` on the wrong parent set) yields [].
	page2, err := cursor.GetPostIDsByRepliesToAccount(context.Background(), "bob", "reply-2", 10)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 2 || page2[0] != 12 || page2[1] != 11 {
		t.Fatalf("page2: expected [12 11] (parent rebind to alice), got %v", page2)
	}

	// Handler level (condenser_api.get_replies_by_last_update).
	gin.SetMode(gin.TestMode)
	wrapped := &db.DB{DB: gdb}
	misc := NewMiscAPI(db.NewRepository(gdb), wrapped, nil)
	invoke := func(raw string) []string {
		t.Helper()
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		result, err := misc.GetRepliesByLastUpdate(ctx, json.RawMessage(raw))
		if err != nil {
			t.Fatalf("handler: %v", err)
		}
		out := []string{}
		for _, p := range result.([]map[string]interface{}) {
			out = append(out, p["permlink"].(string))
		}
		return out
	}
	if got := invoke(`["alice", "", 1]`); len(got) != 1 || got[0] != "reply-2" {
		t.Fatalf("handler page1: expected [reply-2], got %v", got)
	}
	if got := invoke(`["bob", "reply-2", 10]`); len(got) != 2 || got[0] != "reply-2" || got[1] != "reply-1" {
		t.Fatalf("handler page2: expected [reply-2 reply-1], got %v", got)
	}
}

// TestCursorQueryPayoutSeekAndTag pins the condenser ranked-query fixes
// (legacy condenser cursor.py pids_by_query):
//
//   - payout-family seek compares the payout value (`payout <= (SELECT
//     payout ... WHERE post_id = :start_id)`, cursor.py:319), not post_id;
//   - payout-family tag filters go by `category = :tag` (cursor.py:304-307)
//     instead of the tag index;
//   - promoted has a seek branch (the pre-fix code applied no seek at all,
//     so every page returned the first page).
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set.
func TestCursorQueryPayoutSeekAndTag(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping payout seek/tag test")
	}
	gdb, cursor := openCursorTestDB(t)
	now := time.Now().UTC()

	// No hive_post_tags rows at all: only the category filter can find the
	// payout posts.
	//
	//	21 cat=life payout 9 | 22 cat=life payout 5 | 23 cat=life payout 5
	//	24 cat=news payout 7 | 31 promoted 9 | 32 promoted 8 | 33 promoted 7
	if err := gdb.Create(&models.Account{Name: "alice", CreatedAt: now}).Error; err != nil {
		t.Fatalf("seed account: %v", err)
	}
	type spec struct {
		id       int64
		category string
		payout   float64
		promoted float64
	}
	for _, s := range []spec{
		{id: 21, category: "life", payout: 9},
		{id: 22, category: "life", payout: 5},
		{id: 23, category: "life", payout: 5},
		{id: 24, category: "news", payout: 7},
		{id: 31, category: "promoted", promoted: 9},
		{id: 32, category: "promoted", promoted: 8},
		{id: 33, category: "promoted", promoted: 7},
	} {
		permlink := "post-" + strconv.FormatInt(s.id, 10)
		post := models.Post{Author: "alice", Permlink: permlink, Category: s.category, CreatedAt: now}
		post.ID = s.id
		if err := gdb.Create(&post).Error; err != nil {
			t.Fatalf("seed post %d: %v", s.id, err)
		}
		cache := models.PostCache{
			PostID: s.id, Author: "alice", Permlink: permlink,
			Category: s.category, Title: "t", CreatedAt: now,
			PayoutAt: now.Add(24 * time.Hour), UpdatedAt: now,
			Payout: s.payout, Promoted: s.promoted,
		}
		if err := gdb.Create(&cache).Error; err != nil {
			t.Fatalf("seed cache %d: %v", s.id, err)
		}
	}

	sortIDs := func(ids []int64) []int64 {
		out := append([]int64(nil), ids...)
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	expectIDs := func(name string, got, want []int64) {
		t.Helper()
		got, want = sortIDs(got), sortIDs(want)
		if len(got) != len(want) {
			t.Fatalf("%s: expected %v, got %v", name, want, got)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: expected %v, got %v", name, want, got)
			}
		}
	}

	// Payout tag filter by category: 'life' posts only, with no tag-index
	// rows involved.
	ids, err := cursor.GetPostIDsByQuery(context.Background(), "payout", "", "", 10, "life")
	if err != nil {
		t.Fatalf("payout tag: %v", err)
	}
	expectIDs("payout tag=life", ids, []int64{21, 22, 23})

	// Payout seek by payout value: seeking at post 22 (payout 5) yields the
	// posts with payout <= 5 (the start post included, legacy inclusive
	// semantics; the payout-0 promoted posts qualify too). The pre-fix
	// post_id seek returned {21, 22} — re-serving the payout-9 post and
	// losing 23.
	ids, err = cursor.GetPostIDsByQuery(context.Background(), "payout", "alice", "post-22", 10, "")
	if err != nil {
		t.Fatalf("payout seek: %v", err)
	}
	expectIDs("payout seek at payout 5", ids, []int64{22, 23, 31, 32, 33})

	// Promoted seek: the second page moves past the first (pre-fix: every
	// page returned the first page because no seek was applied).
	ids, err = cursor.GetPostIDsByQuery(context.Background(), "promoted", "", "", 2, "")
	if err != nil {
		t.Fatalf("promoted p1: %v", err)
	}
	expectIDs("promoted p1", ids, []int64{31, 32})
	ids, err = cursor.GetPostIDsByQuery(context.Background(), "promoted", "alice", "post-32", 2, "")
	if err != nil {
		t.Fatalf("promoted p2: %v", err)
	}
	expectIDs("promoted p2 (seek by promoted value)", ids, []int64{32, 33})
}
