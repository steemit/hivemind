package condenser

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// TestRepToRawIntGolden checks the rep_to_raw port against exact vectors
// produced by the legacy Python implementation (hive/utils/normalize.py:136).
// Values are chosen so the truncation boundary is not within 1 ULP of an
// integer (Go math.Pow vs C pow may differ in the last bit).
func TestRepToRawIntGolden(t *testing.T) {
	vectors := []struct {
		rep  float64
		want int64
	}{
		{25, 0}, // legacy special case: neutral rep -> 0
		{0, -599484250318},
		{30, 3593813663},
		{34, 10000000000},
		{52, 1000000000000},
		{62.5, 14677992676220},
		{15.5, -11364636663},
		{43.21, 105519647889},
		{62.35, 14125375446227},
		{70.12, 103117727459305},
		{61.04, 10102862550356},
		{78.63, 909680507660533},
		{79.16, 1041784227026451},
	}
	for _, v := range vectors {
		if got := repToRawInt(v.rep); got != v.want {
			t.Errorf("repToRawInt(%v) = %d, want %d", v.rep, got, v.want)
		}
	}
}

// TestValidFollowType checks the valid_follow_type port (legacy
// helpers.py): empty defaults to blog, blog/ignore pass, anything else is a
// PublicError with the legacy message.
func TestValidFollowType(t *testing.T) {
	if got, err := validFollowType(""); err != nil || got != "blog" {
		t.Errorf("empty follow_type = %q, err=%v; want blog", got, err)
	}
	if got, err := validFollowType("blog"); err != nil || got != "blog" {
		t.Errorf("blog follow_type = %q, err=%v", got, err)
	}
	if got, err := validFollowType("ignore"); err != nil || got != "ignore" {
		t.Errorf("ignore follow_type = %q, err=%v", got, err)
	}
	_, err := validFollowType("spam")
	if err == nil {
		t.Fatal("invalid follow_type accepted")
	}
	pe, ok := err.(apierrors.PublicError)
	if !ok {
		t.Fatalf("follow_type error type = %T, want PublicError", err)
	}
	if string(pe) != "invalid follow_type `spam`" {
		t.Errorf("follow_type error message = %q, want legacy message", string(pe))
	}
}

// TestLegacyFollowerWithReputationShape checks the follow entry shape
// (legacy methods.py _legacy_follower_with_reputation): fixed-length
// 2-element `what`, reputation pass-through, exact key set.
func TestLegacyFollowerWithReputationShape(t *testing.T) {
	cases := []struct {
		state int16
		what  []string
	}{
		{1, []string{"blog", ""}},
		{2, []string{"", "ignore"}},
		{3, []string{"blog", "ignore"}},
		{0, []string{"", ""}},
	}
	for _, c := range cases {
		entry := legacyFollowerWithReputation("fan", 62.5, "alice", c.state)
		what, ok := entry["what"].([]string)
		if !ok || len(what) != 2 || what[0] != c.what[0] || what[1] != c.what[1] {
			t.Errorf("state %d: what = %#v, want %v (fixed length 2)", c.state, entry["what"], c.what)
		}
		if len(entry) != 4 {
			t.Errorf("state %d: entry keys = %d, want exactly 4", c.state, len(entry))
		}
		if entry["follower"] != "fan" || entry["following"] != "alice" || entry["reputation"] != 62.5 {
			t.Errorf("state %d: entry = %#v", c.state, entry)
		}
	}

	blob, err := json.Marshal(legacyFollowerWithReputation("fan", 62.5, "alice", 1))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"what":["blog",""]`) {
		t.Errorf("serialized what not fixed-length: %s", string(blob))
	}
}

// callErr invokes a handler and returns its error (counterpart of call).
func callErr(t *testing.T, handler func(*gin.Context, json.RawMessage) (interface{}, error), raw string) error {
	t.Helper()
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	_, err := handler(ctx, json.RawMessage(raw))
	return err
}

// TestFollowAPIsCursor covers the follow-family fixes against a real
// Postgres schema (planned PR#412; audit #19/#26/#27):
//   - get_followers/get_following on the created_at DESC cursor (inclusive
//     start seek, tie determinism at page boundaries, state filter branch)
//   - reputation field pass-through + fixed-length what
//   - invalid follow_type and missing accounts as PublicError
//   - N+1 elimination (query count independent of page size)
//   - get_account_reputations `name` key + rep_to_raw values
//   - get_blog / get_blog_entries allowing start_entry_id = -1
//
// Requires HIVE_TEST_DATABASE_URL.
func TestFollowAPIsCursor(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping follow API integration test")
	}
	dbURL := newNamedTestDB(t, "hive_condenser_follow_test")
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
		t.Fatalf("get sql db: %v", err)
	}
	defer sqlDB.Close()

	gin.SetMode(gin.TestMode)
	repo := db.NewRepository(gdb)
	follow := NewFollowAPI(repo)
	follow.SetDatabase(gdb)
	tags := NewTagsAPI(repo, &db.DB{DB: gdb})
	blog := NewBlogAPI(repo, &db.DB{DB: gdb})

	seedFollowFixture(t, gdb)

	// --- 1. get_followers: created_at DESC cursor, reputation, what. ---
	followers := call(t, follow.GetFollowers, `["alice", "", "blog", 100]`).([]interface{})
	// Blog filter (state IN (1,3)): fan1(1h), fan3(3h), fan4(4h), fan5(4h).
	// fan4/fan5 share created_at so their relative order is unspecified;
	// both precede fan3, which precedes fan1.
	if len(followers) != 4 {
		t.Fatalf("get_followers = %d entries, want 4", len(followers))
	}
	setOf := func(entries []interface{}) map[string]bool {
		out := map[string]bool{}
		for _, e := range entries {
			out[e.(map[string]interface{})["follower"].(string)] = true
		}
		return out
	}
	firstTwo := setOf(followers[:2])
	if len(firstTwo) != 2 || !firstTwo["fan4"] || !firstTwo["fan5"] {
		t.Errorf("page head = %v, want tie pair {fan4, fan5} (created_at DESC)", firstTwo)
	}
	if followers[2].(map[string]interface{})["follower"] != "fan3" ||
		followers[3].(map[string]interface{})["follower"] != "fan1" {
		t.Errorf("page tail order = %#v, want fan3 then fan1", followers[2:])
	}

	entry4 := followers[0].(map[string]interface{})
	if entry4["following"] != "alice" {
		t.Errorf("following = %v, want alice", entry4["following"])
	}
	what, ok := entry4["what"].([]string)
	if !ok || len(what) != 2 || what[0] != "blog" || what[1] != "" {
		t.Errorf("what = %#v, want [blog ''] fixed length", entry4["what"])
	}
	if entry4["reputation"] != float64(62.5) {
		t.Errorf("reputation = %v, want stored UI value 62.5", entry4["reputation"])
	}
	// fan3 has state 3 (blog+ignore) under the blog filter.
	for _, e := range followers {
		m := e.(map[string]interface{})
		if m["follower"] == "fan3" {
			w := m["what"].([]string)
			if w[0] != "blog" || w[1] != "ignore" {
				t.Errorf("fan3 what = %v, want [blog ignore]", w)
			}
		}
	}

	// --- 2. Paging: tie rows land together; inclusive start seek. ---
	page1 := call(t, follow.GetFollowers, `["alice", "", "blog", 2]`).([]interface{})
	if got := setOf(page1); len(got) != 2 || !got["fan4"] || !got["fan5"] {
		t.Fatalf("page1 = %v, want {fan4, fan5}", got)
	}
	// Seek from the tie boundary: everything at or before the tie, so the
	// tie pair repeats in full (deterministic despite the shared created_at).
	page2 := call(t, follow.GetFollowers, `["alice", "fan5", "blog", 2]`).([]interface{})
	if got := setOf(page2); len(got) != 2 || !got["fan4"] || !got["fan5"] {
		t.Fatalf("page2 = %v, want {fan4, fan5} (inclusive tie boundary)", got)
	}
	// Inclusive start on a unique timestamp: the start row is returned.
	page3 := call(t, follow.GetFollowers, `["alice", "fan3", "blog", 10]`).([]interface{})
	if len(page3) != 2 ||
		page3[0].(map[string]interface{})["follower"] != "fan3" ||
		page3[1].(map[string]interface{})["follower"] != "fan1" {
		t.Errorf("seek from fan3 = %#v, want [fan3, fan1] (start inclusive)", page3)
	}

	// --- 3. ignore branch: state IN (2,3) only. ---
	ignored := call(t, follow.GetFollowers, `["alice", "", "ignore", 100]`).([]interface{})
	// fan6 (state 2, 6h), fan3 (state 3 = blog+ignore, 3h), fan2 (state 2, 2h).
	if len(ignored) != 3 {
		t.Fatalf("get_followers ignore = %d entries, want 3 (fan6, fan3, fan2)", len(ignored))
	}
	if ignored[0].(map[string]interface{})["follower"] != "fan6" ||
		ignored[1].(map[string]interface{})["follower"] != "fan3" ||
		ignored[2].(map[string]interface{})["follower"] != "fan2" {
		t.Errorf("ignore order = %#v, want fan6 (6h), fan3 (3h), fan2 (2h)", ignored)
	}
	if w := ignored[0].(map[string]interface{})["what"].([]string); w[0] != "" || w[1] != "ignore" {
		t.Errorf("ignore what = %v, want ['', 'ignore']", w)
	}
	// state 3 appears in both branches; its `what` still shows both bits.
	if w := ignored[1].(map[string]interface{})["what"].([]string); w[0] != "blog" || w[1] != "ignore" {
		t.Errorf("state-3 what = %v, want ['blog', 'ignore']", w)
	}

	// --- 4. get_following: same cursor, mirrored roles. ---
	following := call(t, follow.GetFollowing, `["alice", "", "blog", 100]`).([]interface{})
	if len(following) != 3 {
		t.Fatalf("get_following = %d entries, want 3", len(following))
	}
	if following[0].(map[string]interface{})["following"] != "aa3" ||
		following[1].(map[string]interface{})["following"] != "aa2" ||
		following[2].(map[string]interface{})["following"] != "aa1" {
		t.Errorf("following order = %#v, want aa3, aa2, aa1 (created_at DESC)", following)
	}
	if following[0].(map[string]interface{})["follower"] != "alice" {
		t.Errorf("following entry follower = %v, want alice", following[0])
	}
	if following[0].(map[string]interface{})["reputation"] != float64(34) {
		t.Errorf("following reputation = %v, want 34 (aa3 stored score)", following[0].(map[string]interface{})["reputation"])
	}
	seeked := call(t, follow.GetFollowing, `["alice", "aa2", "blog", 10]`).([]interface{})
	if len(seeked) != 2 ||
		seeked[0].(map[string]interface{})["following"] != "aa2" ||
		seeked[1].(map[string]interface{})["following"] != "aa1" {
		t.Errorf("following seek from g2 = %#v, want [g2, g1] (start inclusive)", seeked)
	}

	// --- 5. by_page: offset pagination on the same ordering. ---
	pageA := call(t, follow.GetFollowersByPage, `["alice", 0, 3, "blog"]`).([]interface{})
	if len(pageA) != 3 || pageA[2].(map[string]interface{})["follower"] != "fan3" {
		t.Fatalf("by_page(0,3) = %#v, want tail fan3", pageA)
	}
	if got := setOf(pageA[:2]); len(got) != 2 || !got["fan4"] || !got["fan5"] {
		t.Errorf("by_page(0,3) head = %v, want {fan4, fan5}", got)
	}
	pageB := call(t, follow.GetFollowersByPage, `["alice", 1, 3, "blog"]`).([]interface{})
	if len(pageB) != 1 || pageB[0].(map[string]interface{})["follower"] != "fan1" {
		t.Errorf("by_page(1,3) = %#v, want [fan1]", pageB)
	}

	// --- 6. Error shapes: invalid follow_type, missing accounts. ---
	err = callErr(t, follow.GetFollowers, `["alice", "", "spam", 10]`)
	if err == nil {
		t.Fatal("invalid follow_type accepted")
	}
	if pe, ok := err.(apierrors.PublicError); !ok || string(pe) != "invalid follow_type `spam`" {
		t.Errorf("follow_type error = %v (%T), want PublicError invalid follow_type `spam`", err, err)
	}
	err = callErr(t, follow.GetFollowingByPage, `["alice", 0, 3, "spam"]`)
	if pe, ok := err.(apierrors.PublicError); !ok || string(pe) != "invalid follow_type `spam`" {
		t.Errorf("by_page follow_type error = %v (%T), want PublicError", err, err)
	}
	err = callErr(t, follow.GetFollowers, `["nosuchuser", "", "blog", 10]`)
	if pe, ok := err.(apierrors.PublicError); !ok || string(pe) != "account not found: `nosuchuser`" {
		t.Errorf("missing account error = %v (%T), want PublicError account not found: `nosuchuser`", err, err)
	}
	err = callErr(t, follow.GetFollowers, `["alice", "nosuchuser", "blog", 10]`)
	if pe, ok := err.(apierrors.PublicError); !ok || string(pe) != "account not found: `nosuchuser`" {
		t.Errorf("missing start account error = %v (%T), want PublicError", err, err)
	}

	// --- 7. N+1 elimination: query count independent of result size. ---
	var queries int64
	gdb.Callback().Query().Before("gorm:query").Register("test:follow_query_count", func(tx *gorm.DB) {
		atomic.AddInt64(&queries, 1)
	})
	for _, raw := range []string{
		`["alice", "", "blog", 100]`, // 4 matched rows
		`["alice", "", "blog", 1]`,   // 1 matched row
		`["alice", "fan5", "blog", 100]`,
	} {
		atomic.StoreInt64(&queries, 0)
		call(t, follow.GetFollowers, raw)
		if n := atomic.LoadInt64(&queries); n > 3 {
			t.Errorf("get_followers(%s) issued %d queries; want <= 3 (account+start lookups+join, no per-row N+1)", raw, n)
		}
	}

	// --- 8. get_account_reputations: `name` key + rep_to_raw values. ---
	// (Migrations seed initminer/miners/null/temp accounts; the lower bound
	// keeps the asserted window deterministic.)
	reps := call(t, tags.GetAccountReputations, `["fan1", 3]`).(map[string]interface{})
	list, ok := reps["reputations"].([]map[string]interface{})
	if !ok {
		t.Fatalf("reputations wrapper = %#v, want list under `reputations`", reps["reputations"])
	}
	want := []struct {
		name string
		rep  int64
	}{
		{"fan1", 3593813663},    // 30
		{"fan2", -599484250318}, // 0
		{"fan3", 0},             // 25 (neutral special case)
	}
	if len(list) != len(want) {
		t.Fatalf("reputations = %d entries, want %d", len(list), len(want))
	}
	for i, w := range want {
		e := list[i]
		if e["name"] != w.name {
			t.Fatalf("reputations[%d].name = %v, want %v", i, e["name"], w.name)
		}
		if _, has := e["account"]; has {
			t.Errorf("reputations[%s] still has legacy-wrong key `account`", w.name)
		}
		got, ok := e["reputation"].(int64)
		if !ok {
			t.Errorf("reputations[%s] value type = %T, want int64 (legacy int)", w.name, e["reputation"])
			continue
		}
		if got != w.rep {
			t.Errorf("reputations[%s] = %d, want %d (rep_to_raw)", w.name, got, w.rep)
		}
	}
	// Remaining vectors + name ASC ordering + lower bound seek + limit.
	reps2 := call(t, tags.GetAccountReputations, `["fan4", 3]`).(map[string]interface{})["reputations"].([]map[string]interface{})
	want2 := []struct {
		name string
		rep  int64
	}{
		{"fan4", 14677992676220}, // 62.5
		{"fan5", 10000000000},    // 34
		{"fan6", 3593813663},     // 30
	}
	if len(reps2) != len(want2) {
		t.Fatalf("reputations seek = %d entries, want %d", len(reps2), len(want2))
	}
	for i, w := range want2 {
		if reps2[i]["name"] != w.name || reps2[i]["reputation"] != w.rep {
			t.Errorf("reputations seek[%d] = %#v, want %s/%d", i, reps2[i], w.name, w.rep)
		}
	}

	// --- 9. get_blog: start_entry_id -1 resolves to newest entries. ---
	entries := call(t, blog.GetBlog, `["alice", -1, 10]`).([]map[string]interface{})
	if len(entries) != 10 {
		t.Fatalf("get_blog(-1,10) = %d entries, want 10 (12 in feed)", len(entries))
	}
	if entries[0]["entry_id"] != 11 || entries[0]["blog"] != "alice" {
		t.Errorf("first entry = %#v, want entry_id 11 (newest index)", entries[0])
	}
	if entries[9]["entry_id"] != 2 {
		t.Errorf("last entry_id = %v, want 2", entries[9]["entry_id"])
	}
	firstComment := entries[0]["comment"].(map[string]interface{})
	if firstComment["permlink"] != "post-12" {
		t.Errorf("newest blog post = %v, want post-12", firstComment["permlink"])
	}
	// entry_id strictly descending.
	for i := 1; i < len(entries); i++ {
		if entries[i]["entry_id"].(int) != entries[i-1]["entry_id"].(int)-1 {
			t.Errorf("entry_id not descending: %v then %v", entries[i-1]["entry_id"], entries[i]["entry_id"])
			break
		}
	}
	// legacy docstring example: (acct, 2, 3) = posts at idx 2,1,0, newest first.
	oldest := call(t, blog.GetBlog, `["alice", 2, 3]`).([]map[string]interface{})
	if len(oldest) != 3 ||
		oldest[0]["comment"].(map[string]interface{})["permlink"] != "post-03" ||
		oldest[1]["comment"].(map[string]interface{})["permlink"] != "post-02" ||
		oldest[2]["comment"].(map[string]interface{})["permlink"] != "post-01" {
		t.Errorf("get_blog(2,3) = %#v, want posts 3,2,1", oldest)
	}
	if oldest[0]["entry_id"] != 2 || oldest[2]["entry_id"] != 0 {
		t.Errorf("get_blog(2,3) entry_ids = %v,%v, want 2,0", oldest[0]["entry_id"], oldest[2]["entry_id"])
	}

	// get_blog_entries also passes -1 through (same legacy branch).
	entryList := call(t, blog.GetBlogEntries, `["alice", -1, 3]`).([]map[string]interface{})
	if len(entryList) != 3 {
		t.Fatalf("get_blog_entries(-1,3) = %d entries, want 3", len(entryList))
	}
	if _, has := entryList[0]["comment"]; has {
		t.Errorf("get_blog_entries must not carry the comment object")
	}
	if entryList[0]["author"] != "alice" || entryList[0]["permlink"] != "post-12" {
		t.Errorf("entry ref = %#v, want alice/post-12", entryList[0])
	}

	// Only values below -1 are rejected (legacy valid_offset).
	err = callErr(t, blog.GetBlog, `["alice", -2, 5]`)
	if pe, ok := err.(apierrors.PublicError); !ok || string(pe) != "offset cannot be negative" {
		t.Errorf("start_entry_id -2 error = %v (%T), want PublicError offset cannot be negative", err, err)
	}
}

// seedFollowFixture builds the accounts/follows/feed rows used by
// TestFollowAPIsCursor. Follow timestamps are staggered hourly; fan4 and
// fan5 deliberately share one timestamp (created_at tie).
func seedFollowFixture(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	// float4-exact reputation scores so DB round-trips keep the golden
	// rep_to_raw vectors stable.
	accounts := []struct {
		name string
		rep  float64
	}{
		{"alice", 52},
		{"fan1", 30},
		{"fan2", 0},
		{"fan3", 25},
		{"fan4", 62.5},
		{"fan5", 34},
		{"fan6", 30},
		{"aa1", 52},
		{"aa2", 25},
		{"aa3", 34},
	}
	ids := map[string]int64{}
	for _, a := range accounts {
		acc := models.Account{Name: a.name, Reputation: a.rep, CreatedAt: base}
		if err := gdb.Create(&acc).Error; err != nil {
			t.Fatalf("seed account %s: %v", a.name, err)
		}
		ids[a.name] = acc.ID
	}
	// GORM skips zero-value fields that carry a `default:` tag, so reputation
	// 0 (fan2) would silently become the schema default 25. Write it back
	// explicitly with UpdateColumn (always emits the value).
	if err := gdb.Model(&models.Account{}).
		Where("name = ?", "fan2").
		UpdateColumn("reputation", 0).Error; err != nil {
		t.Fatalf("seed fan2 reputation: %v", err)
	}

	// Followers of alice. created_at offsets (hours ago): fan1=1, fan2=2,
	// fan3=3, fan4=fan5=4 (tie), fan6=6. States cover 1/2/3.
	follows := []struct {
		follower string
		state    int16
		at       time.Time
	}{
		{"fan1", 1, base.Add(1 * time.Hour)},
		{"fan2", 2, base.Add(2 * time.Hour)},
		{"fan3", 3, base.Add(3 * time.Hour)},
		{"fan4", 1, base.Add(4 * time.Hour)},
		{"fan5", 1, base.Add(4 * time.Hour)},
		{"fan6", 2, base.Add(6 * time.Hour)},
	}
	for _, f := range follows {
		follow := models.Follow{
			FollowerID:  ids[f.follower],
			FollowingID: ids["alice"],
			State:       f.state,
			CreatedAt:   f.at,
		}
		if err := gdb.Create(&follow).Error; err != nil {
			t.Fatalf("seed follow %s: %v", f.follower, err)
		}
	}

	// alice follows g1..g3 (blog state), staggered.
	for i, name := range []string{"aa1", "aa2", "aa3"} {
		follow := models.Follow{
			FollowerID:  ids["alice"],
			FollowingID: ids[name],
			State:       1,
			CreatedAt:   base.Add(time.Duration(i+1) * time.Hour),
		}
		if err := gdb.Create(&follow).Error; err != nil {
			t.Fatalf("seed following %s: %v", name, err)
		}
	}

	// Blog feed for alice: 12 posts, one per hour, post-12 newest.
	for i := 1; i <= 12; i++ {
		permlink := fmt.Sprintf("post-%02d", i)
		post := models.Post{
			Author:    "alice",
			Permlink:  permlink,
			Category:  "test",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
			Depth:     0,
		}
		post.ID = int64(i)
		if err := gdb.Create(&post).Error; err != nil {
			t.Fatalf("seed post %d: %v", i, err)
		}
		at := base.Add(time.Duration(i) * time.Hour)
		cache := models.PostCache{
			PostID: int64(i), Author: "alice", Permlink: permlink, Category: "test",
			Title: permlink, Depth: 0, Payout: 1, CreatedAt: at, PayoutAt: at, UpdatedAt: at,
		}
		if err := gdb.Create(&cache).Error; err != nil {
			t.Fatalf("seed cache %d: %v", i, err)
		}
		feed := models.FeedCache{PostID: int64(i), AccountID: ids["alice"], CreatedAt: at}
		if err := gdb.Create(&feed).Error; err != nil {
			t.Fatalf("seed feed %d: %v", i, err)
		}
	}
}
