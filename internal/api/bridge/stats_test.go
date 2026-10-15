package bridge

import (
	"encoding/json"
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
)

// TestGetPayoutStats_AggregationAndCache verifies the query-time payout
// aggregation against a real Postgres schema:
//
//   - the UNION of per-(community, author) rows and per-community totals
//     matches legacy payout_stats materialization (common/payout_stats.py):
//     community rows surface as (name, title, payout, posts, authors) and
//     non-community author rows as ('@'+author, '@'+author, payout, posts,
//     null); author-level rows INSIDE a community never surface;
//   - is_paidout='1' rows are excluded from every aggregate;
//   - total = unpaid payout across all rows, blogs = unpaid non-community
//     payout (hive_api/stats.py:42-47);
//   - limit slices the payout-DESC ordered items (legacy valid_limit
//     default 250, max 250);
//   - the 60-minute in-process cache serves identical results after the
//     table changes, until it expires.
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set:
//
//	HIVE_TEST_DATABASE_URL=postgresql://test:test@localhost:5433/hive go test -run TestGetPayoutStats -p 1 ./internal/api/bridge/...
func TestGetPayoutStats_AggregationAndCache(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping payout stats test")
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

	seedPayoutStats(t, gdb)

	gin.SetMode(gin.TestMode)
	api := NewStatsAPI(db.NewRepository(gdb), &db.DB{DB: gdb})

	call := func(limit interface{}) map[string]interface{} {
		t.Helper()
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		req := map[string]interface{}{}
		if limit != nil {
			req["limit"] = limit
		}
		params, _ := json.Marshal(req)
		result, err := api.GetPayoutStats(ctx, params)
		if err != nil {
			t.Fatalf("get_payout_stats(limit=%v): %v", limit, err)
		}
		res, ok := result.(map[string]interface{})
		if !ok {
			t.Fatalf("expected {items,total,blogs} map, got %T", result)
		}
		return res
	}

	// --- aggregation correctness (seed layout in seedPayoutStats) ---
	//
	// Unpaid payouts by bucket:
	//   community 111 (hive-111, "Comm One"): alice 30 + bob 20 = 50, 2 posts
	//   community 222 (hive-222, "Comm Two"): carol 40, 1 post
	//   non-community ("blogs"): alice 10 + dave 5 over 2 posts
	//   paid-out post (alice 100, is_paidout): excluded everywhere
	//
	// Items ordered by payout DESC: the two community aggregates (50, 40)
	// then the blogs author rows (10, 5). Author-level rows inside
	// communities (e.g. alice's 30 in hive-111) must NOT surface.
	res := call(nil)
	if res["total"].(float64) != 105 { // 50 + 40 + 15
		t.Errorf("total: expected 105, got %v", res["total"])
	}
	if res["blogs"].(float64) != 15 {
		t.Errorf("blogs: expected 15, got %v", res["blogs"])
	}

	items := res["items"].([]interface{})
	expect := []struct {
		url, label    string
		payout        float64
		posts         int64
		authors       int64
		authorsIsNull bool
	}{
		{"hive-111", "Comm One", 50, 2, 2, false},
		{"hive-222", "Comm Two", 40, 1, 1, false},
		{"@alice", "@alice", 10, 1, 0, true},
		{"@dave", "@dave", 5, 1, 0, true},
	}
	if len(items) != len(expect) {
		t.Fatalf("items: expected %d rows, got %d: %v", len(expect), len(items), items)
	}
	for i, want := range expect {
		arr := items[i].([]interface{})
		if arr[0] != want.url || arr[1] != want.label {
			t.Errorf("item %d: expected [%v %v], got [%v %v]", i, want.url, want.label, arr[0], arr[1])
		}
		if arr[2].(float64) != want.payout {
			t.Errorf("item %d (%v): expected payout %v, got %v", i, want.url, want.payout, arr[2])
		}
		if arr[3].(int64) != want.posts {
			t.Errorf("item %d (%v): expected posts %v, got %v", i, want.url, want.posts, arr[3])
		}
		if want.authorsIsNull {
			if arr[4] != nil {
				t.Errorf("item %d (%v): expected null authors on author-level row, got %v", i, want.url, arr[4])
			}
		} else if arr[4] == nil || arr[4].(int64) != want.authors {
			t.Errorf("item %d (%v): expected authors %v, got %v", i, want.url, want.authors, arr[4])
		}
	}

	// limit slices the payout-ordered items (legacy LIMIT applies to the
	// same ORDER BY).
	res = call(2)
	if got := len(res["items"].([]interface{})); got != 2 {
		t.Errorf("limit=2: expected 2 items, got %d", got)
	}
	first := res["items"].([]interface{})[0].([]interface{})
	if first[0] != "hive-111" || first[2].(float64) != 50 {
		t.Errorf("limit=2: first item should be hive-111/50, got %v", first)
	}

	// --- 60-minute in-process cache ---
	//
	// A new unpaid post (payout 999) lands after the first aggregation; the
	// cached answer must not observe it, proving no second query ran. After
	// expiring the cache the new post surfaces.
	seedPayoutCacheRow(t, gdb, 90, "erin", "late-post", 999, false, 0)

	cached := call(nil)
	if cached["total"].(float64) != 105 {
		t.Errorf("cache hit: total should still be 105, got %v", cached["total"])
	}
	if got := len(cached["items"].([]interface{})); got != 4 {
		t.Errorf("cache hit: expected cached 4 items, got %d", got)
	}

	// Expire the cache (same-package access to the unexported state) and
	// verify the aggregation re-runs against the new row.
	api.cacheMu.Lock()
	api.cachedAt = time.Time{}
	api.cacheMu.Unlock()

	fresh := call(nil)
	if fresh["total"].(float64) != 1104 { // 105 + 999
		t.Errorf("after expiry: total should be 1104, got %v", fresh["total"])
	}
	first = fresh["items"].([]interface{})[0].([]interface{})
	if first[0] != "@erin" || first[2].(float64) != 999 {
		t.Errorf("after expiry: first item should be @erin/999, got %v", first)
	}
}

// TestGetPayoutStats_EmptyTable pins the degenerate empty-table answer:
// legacy would raise float(None) on SUM over an empty payout_stats; Go
// serves {items:[], total:0, blogs:0} instead (COALESCE, documented
// divergence for an empty index).
func TestGetPayoutStats_EmptyTable(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping payout stats test")
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

	gin.SetMode(gin.TestMode)
	api := NewStatsAPI(db.NewRepository(gdb), &db.DB{DB: gdb})

	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	result, err := api.GetPayoutStats(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("empty table: %v", err)
	}
	res := result.(map[string]interface{})
	if len(res["items"].([]interface{})) != 0 || res["total"].(float64) != 0 || res["blogs"].(float64) != 0 {
		t.Errorf("empty table: expected empty/0/0, got %v", res)
	}
}

// TestGetPayoutStats_LimitValidation mirrors legacy valid_limit(limit, 250):
// absent keeps the 250 default, 1..250 passes, 0 or >250 errors.
func TestGetPayoutStats_LimitValidation(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping payout stats test")
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

	gin.SetMode(gin.TestMode)
	api := NewStatsAPI(db.NewRepository(gdb), &db.DB{DB: gdb})

	callErr := func(body string) error {
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		_, err := api.GetPayoutStats(ctx, json.RawMessage(body))
		return err
	}
	if err := callErr(`{"limit":0}`); err == nil {
		t.Error("limit=0 should error (must be positive)")
	}
	if err := callErr(`{"limit":251}`); err == nil {
		t.Error("limit=251 should error (exceeds max 250)")
	}
	if err := callErr(`{"limit":250}`); err != nil {
		t.Errorf("limit=250 should pass, got %v", err)
	}
	if err := callErr(`{"limit":null}`); err == nil {
		t.Error("limit=null should error (legacy: limit must be provided)")
	}
}

// seedPayoutStats builds the fixture for TestGetPayoutStats:
//
//	communities: hive-111 "Comm One" (id 111), hive-222 "Comm Two" (id 222)
//	unpaid: alice 30 (cid 111), bob 20 (cid 111), carol 40 (cid 222),
//	        alice 10 (blog), dave 5 (blog)
//	paid out: alice 100 (cid 111, is_paidout) -- excluded everywhere
func seedPayoutStats(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()

	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		if err := gdb.Create(&models.Account{Name: name, CreatedAt: now}).Error; err != nil {
			t.Fatalf("seed account %s: %v", name, err)
		}
	}
	comms := []models.Community{
		{ID: 111, Name: "hive-111", Title: "Comm One", CreatedAt: now},
		{ID: 222, Name: "hive-222", Title: "Comm Two", CreatedAt: now},
	}
	for i := range comms {
		if err := gdb.Create(&comms[i]).Error; err != nil {
			t.Fatalf("seed community %d: %v", comms[i].ID, err)
		}
	}

	seedPayoutPost(t, gdb, 10, "alice", "a-in-111", 30, false, 111)
	seedPayoutPost(t, gdb, 11, "bob", "b-in-111", 20, false, 111)
	seedPayoutPost(t, gdb, 12, "carol", "c-in-222", 40, false, 222)
	seedPayoutPost(t, gdb, 13, "alice", "a-blog", 10, false, 0)
	seedPayoutPost(t, gdb, 14, "dave", "d-blog", 5, false, 0)
	seedPayoutPost(t, gdb, 15, "alice", "a-paid", 100, true, 111)
}

// seedPayoutPost inserts one hive_posts + hive_posts_cache pair for the
// payout-stats fixture.
func seedPayoutPost(t *testing.T, gdb *gorm.DB, id int64, author, permlink string, payout float64, paidout bool, communityID int64) {
	t.Helper()
	now := time.Now().UTC()

	p := models.Post{Author: author, Permlink: permlink, Category: "test", CreatedAt: now}
	p.ID = id
	if communityID != 0 {
		p.CommunityID.Valid = true
		p.CommunityID.Int64 = communityID
	}
	if err := gdb.Create(&p).Error; err != nil {
		t.Fatalf("seed post %d: %v", id, err)
	}
	seedPayoutCacheRow(t, gdb, id, author, permlink, payout, paidout, communityID)
}

// seedPayoutCacheRow inserts one hive_posts_cache row (the payout-stats
// aggregation reads the cache table only).
func seedPayoutCacheRow(t *testing.T, gdb *gorm.DB, id int64, author, permlink string, payout float64, paidout bool, communityID int64) {
	t.Helper()
	now := time.Now().UTC()
	c := models.PostCache{
		PostID: id, Author: author, Permlink: permlink, Category: "test",
		Title: "t-" + permlink, CreatedAt: now, PayoutAt: now, UpdatedAt: now,
		Payout: payout, IsPaidout: paidout,
	}
	if communityID != 0 {
		c.CommunityID.Valid = true
		c.CommunityID.Int64 = communityID
	}
	if err := gdb.Create(&c).Error; err != nil {
		t.Fatalf("seed cache %d: %v", id, err)
	}
}
