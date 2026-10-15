package condenser

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"github.com/steemit/hivemind/internal/steem"
)

// newNamedTestDB creates a dedicated database with the given name (shared
// helper; each integration test owns its own database).
func newNamedTestDB(t *testing.T, name string) string {
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
	if _, err := raw.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %s;`, name)); err != nil {
		t.Fatalf("drop test db: %v", err)
	}
	if _, err := raw.Exec(fmt.Sprintf(`CREATE DATABASE %s;`, name)); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	u.Path = "/" + name
	return u.String()
}

// steemd-shaped raw_json payloads (legacy stores the full comment object;
// only the fields _condenser_post_object imports are exercised here).
const unpaidRootRawJSON = `{"parent_author":"","parent_permlink":"life","author":"alice","permlink":"unpaid-root","category":"life","title":"Root Title","url":"/life/@alice/unpaid-root","root_title":"Root Title","beneficiaries":[{"account":"steem.dao","weight":1000}],"max_accepted_payout":"1000000.000 SBD","percent_steem_dollars":10000,"curator_payout_value":"0.000 SBD","total_payout_value":"0.000 SBD"}`

const paidCommentRawJSON = `{"parent_author":"alice","parent_permlink":"unpaid-root","author":"bob","permlink":"paid-comment","category":"life","title":"RE: Root Title","url":"/life/@alice/unpaid-root#/@bob/paid-comment","root_title":"Root Title","beneficiaries":[],"max_accepted_payout":"0.000 SBD","percent_steem_dollars":0,"curator_payout_value":"1.234 SBD","total_payout_value":"1.766 SBD"}`

const nocatRawJSON = `{"parent_author":"","parent_permlink":"","author":"alice","permlink":"nocat-root","category":"","title":"NoCat","url":"/@alice/nocat-root","root_title":"NoCat","beneficiaries":[],"max_accepted_payout":"1000000.000 SBD","percent_steem_dollars":10000,"curator_payout_value":"0.000 SBD","total_payout_value":"0.000 SBD"}`

// TestGetContentPostObject covers the condenser post object fixes against a
// real Postgres schema (planned PR#411, 03-api.md 5.2/5.4): the six
// raw_json import fields, paid/unpaid curator split, cashout sentinel
// strings, #3424 empty-category mitigation, _mute_votes filtering, the
// get_content 3-key sentinel, is_deleted filtering, and get_content_replies
// error propagation. Requires HIVE_TEST_DATABASE_URL.
func TestGetContentPostObject(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping condenser content integration test")
	}
	dbURL := newNamedTestDB(t, "hive_condenser_content_test")
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

	seedContentFixture(t, gdb)

	gin.SetMode(gin.TestMode)
	repo := db.NewRepository(gdb)
	content := NewContentAPI(repo)
	content.SetDatabase(gdb)

	// --- 1. Unpaid root post: six import fields + pending payout state. ---
	unpaid := getContentObj(t, content, "alice", "unpaid-root")

	if got := unpaid["parent_author"]; got != "" {
		t.Errorf("unpaid root parent_author = %v, want \"\"", got)
	}
	if got := unpaid["parent_permlink"]; got != "life" {
		t.Errorf("unpaid root parent_permlink = %v, want life (category)", got)
	}
	if got := unpaid["root_title"]; got != "Root Title" {
		t.Errorf("root_title = %v, want Root Title", got)
	}
	bens, ok := unpaid["beneficiaries"].([]interface{})
	if !ok || len(bens) != 1 {
		t.Fatalf("beneficiaries = %#v, want 1 entry", unpaid["beneficiaries"])
	}
	ben := bens[0].(map[string]interface{})
	if ben["account"] != "steem.dao" || ben["weight"] != float64(1000) {
		t.Errorf("beneficiaries[0] = %#v, want steem.dao/1000", ben)
	}
	if got := unpaid["max_accepted_payout"]; got != "1000000.000 SBD" {
		t.Errorf("max_accepted_payout = %v, want 1000000.000 SBD", got)
	}
	if got := unpaid["percent_steem_dollars"]; got != float64(10000) {
		t.Errorf("percent_steem_dollars = %#v, want numeric 10000", got)
	}

	// Serialized shape: integer JSON number, no ".0" suffix (legacy int).
	blob, err := json.Marshal(unpaid)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(blob), `"percent_steem_dollars":10000`) {
		t.Errorf("percent_steem_dollars serialization: %s", string(blob))
	}
	if !strings.Contains(string(blob), `"beneficiaries":[{"account":"steem.dao","weight":1000}]`) {
		t.Errorf("beneficiaries serialization: %s", string(blob))
	}

	// Payout trio: everything pending while unpaid, no curator split.
	if got := unpaid["total_payout_value"]; got != "0.000 SBD" {
		t.Errorf("unpaid total_payout_value = %v, want 0.000 SBD", got)
	}
	if got := unpaid["curator_payout_value"]; got != "0.000 SBD" {
		t.Errorf("unpaid curator_payout_value = %v, want 0.000 SBD", got)
	}
	if got := unpaid["pending_payout_value"]; got != "5.000 SBD" {
		t.Errorf("unpaid pending_payout_value = %v, want 5.000 SBD", got)
	}

	// cashout_time/last_payout: real timestamp (timezone-less, no Z) and
	// the legacy sentinel string respectively.
	if got := unpaid["cashout_time"]; got != "2026-10-20T12:00:00" {
		t.Errorf("unpaid cashout_time = %v, want 2026-10-20T12:00:00", got)
	}
	if got := unpaid["last_payout"]; got != "1969-12-31T23:59:59" {
		t.Errorf("unpaid last_payout = %v, want sentinel 1969-12-31T23:59:59", got)
	}

	// Every key the legacy condenser post object carries must be present.
	for _, key := range legacyCondenserPostKeys() {
		if _, ok := unpaid[key]; !ok {
			t.Errorf("post object missing legacy key %q", key)
		}
	}

	// active_votes before muting: voter1 + voter2 (votes CSV fixture).
	assertVoters(t, unpaid, []string{"voter1", "voter2"})

	// --- 2. Paid comment: curator split + inverted cashout sentinel. ---
	paid := getContentObj(t, content, "bob", "paid-comment")

	if got := paid["parent_author"]; got != "alice" {
		t.Errorf("paid comment parent_author = %v, want alice (from raw_json)", got)
	}
	if got := paid["parent_permlink"]; got != "unpaid-root" {
		t.Errorf("paid comment parent_permlink = %v, want unpaid-root (from raw_json)", got)
	}
	if got := paid["curator_payout_value"]; got != "1.234 SBD" {
		t.Errorf("paid curator_payout_value = %v, want 1.234 SBD", got)
	}
	if got := paid["total_payout_value"]; got != "1.766 SBD" {
		t.Errorf("paid total_payout_value = %v, want 1.766 SBD (payout 3 - curator 1.234)", got)
	}
	if got := paid["pending_payout_value"]; got != "0.000 SBD" {
		t.Errorf("paid pending_payout_value = %v, want 0.000 SBD", got)
	}
	if got := paid["last_payout"]; got != "2026-09-30T10:00:00" {
		t.Errorf("paid last_payout = %v, want 2026-09-30T10:00:00", got)
	}
	if got := paid["cashout_time"]; got != "1969-12-31T23:59:59" {
		t.Errorf("paid cashout_time = %v, want sentinel 1969-12-31T23:59:59", got)
	}

	// --- 3. condenser#3424: empty category surfaces as 'undefined'. ---
	nocat := getContentObj(t, content, "alice", "nocat-root")
	if got := nocat["category"]; got != "undefined" {
		t.Errorf("#3424 category = %v, want undefined", got)
	}
	if got := nocat["parent_permlink"]; got != "undefined" {
		t.Errorf("#3424 parent_permlink = %v, want undefined (mitigated category)", got)
	}

	// --- 4. get_content sentinel for a missing post: exactly 3 keys. ---
	missing := call(t, content.GetContent, `["alice", "no-such-post"]`).(map[string]interface{})
	if len(missing) != 3 || missing["id"] != 0 || missing["author"] != "" || missing["permlink"] != "" {
		t.Fatalf("missing post sentinel = %#v, want {id:0, author:'', permlink:''}", missing)
	}

	// --- 5. Deleted posts resolve to the same sentinel (is_deleted filter). ---
	deleted := call(t, content.GetContent, `["alice", "deleted-root"]`).(map[string]interface{})
	if len(deleted) != 3 || deleted["id"] != 0 {
		t.Fatalf("deleted post should hit sentinel, got %#v", deleted)
	}

	// --- 6. _mute_votes: muted voters stripped from active_votes. ---
	muteSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("voter1"))
	}))
	defer muteSrv.Close()
	steem.SetSharedMutes(steem.NewMutes(muteSrv.URL))
	t.Cleanup(func() { steem.SetSharedMutes(steem.NewMutes("")) })
	filtered := getContentObj(t, content, "alice", "unpaid-root")
	assertVoters(t, filtered, []string{"voter2"})

	// --- 7. get_content_replies: children in creation order, deleted
	// children and deleted parents filtered out. ---
	replies := call(t, content.GetContentReplies, `["alice", "unpaid-root"]`).([]map[string]interface{})
	if len(replies) != 2 {
		t.Fatalf("get_content_replies = %d replies, want 2 (deleted child excluded)", len(replies))
	}
	if replies[0]["permlink"] != "paid-comment" || replies[1]["permlink"] != "reply-ok" {
		t.Errorf("replies order = %v then %v, want paid-comment then reply-ok",
			replies[0]["permlink"], replies[1]["permlink"])
	}
	if got := replies[0]["parent_author"]; got != "alice" {
		t.Errorf("reply[0] parent_author = %v, want alice", got)
	}
	if empty := call(t, content.GetContentReplies, `["alice", "no-such-post"]`).([]map[string]interface{}); len(empty) != 0 {
		t.Errorf("replies of missing parent = %v, want empty", empty)
	}
	if empty := call(t, content.GetContentReplies, `["alice", "deleted-root"]`).([]map[string]interface{}); len(empty) != 0 {
		t.Errorf("replies of deleted parent = %v, want empty", empty)
	}

	// --- 8. DB failures propagate instead of being swallowed. ---
	sqlDB.Close()
	if _, err := content.GetContentReplies(testGinContext(t), json.RawMessage(`["alice", "unpaid-root"]`)); err == nil {
		t.Error("get_content_replies must propagate DB errors, got nil")
	}
}

// getContentObj fetches a post object via get_content and asserts the
// handler returned without error.
func getContentObj(t *testing.T, c *ContentAPI, author, permlink string) map[string]interface{} {
	t.Helper()
	return call(t, c.GetContent, fmt.Sprintf(`["%s", "%s"]`, author, permlink)).(map[string]interface{})
}

// assertVoters checks the voter sequence of a post object's active_votes.
func assertVoters(t *testing.T, post map[string]interface{}, want []string) {
	t.Helper()
	votes, ok := post["active_votes"].([]interface{})
	if !ok {
		t.Fatalf("active_votes type = %T, want slice", post["active_votes"])
	}
	if len(votes) != len(want) {
		t.Fatalf("active_votes = %#v, want voters %v", post["active_votes"], want)
	}
	for i, w := range want {
		vote := votes[i].(map[string]interface{})
		if vote["voter"] != w {
			t.Errorf("active_votes[%d].voter = %v, want %v", i, vote["voter"], w)
		}
	}
}

// testGinContext builds a gin context carrying a POST request.
func testGinContext(t *testing.T) *gin.Context {
	t.Helper()
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	return ctx
}

// legacyCondenserPostKeys lists every key of legacy _condenser_post_object
// (condenser_api/objects.py:135-200), with the legacy `post_id` key
// expressed as the Go-side `id` (registered deviation: the Go rewrite kept
// the steemd-style name and internal/api/condenser consumers read it).
func legacyCondenserPostKeys() []string {
	return []string{
		"id", "author", "permlink", "category", "title", "body",
		"json_metadata", "created", "last_update", "depth", "children",
		"net_rshares", "last_payout", "cashout_time", "total_payout_value",
		"curator_payout_value", "pending_payout_value", "promoted",
		"replies", "body_length", "active_votes", "author_reputation",
		"parent_author", "parent_permlink", "url", "root_title",
		"beneficiaries", "max_accepted_payout", "percent_steem_dollars",
	}
}

// seedContentFixture builds the accounts/posts/cache rows used by
// TestGetContentPostObject.
func seedContentFixture(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	created := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	unpaidCashout := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	paidAt := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)

	for _, name := range []string{"alice", "bob", "carol"} {
		if err := gdb.Create(&models.Account{Name: name, CreatedAt: created}).Error; err != nil {
			t.Fatalf("seed account %s: %v", name, err)
		}
	}

	mk := func(id int64, parent int64, author, permlink, category string, depth int16, deleted bool) models.Post {
		p := models.Post{
			Author:    author,
			Permlink:  permlink,
			Category:  category,
			CreatedAt: created,
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
		mk(1, 0, "alice", "unpaid-root", "life", 0, false),
		mk(2, 1, "bob", "paid-comment", "life", 1, false),
		mk(3, 0, "alice", "deleted-root", "life", 0, true),
		mk(4, 0, "alice", "nocat-root", "", 0, false),
		mk(5, 1, "carol", "reply-ok", "life", 1, false),
		mk(6, 1, "carol", "reply-deleted", "life", 1, true),
	}
	for i := range posts {
		if err := gdb.Create(&posts[i]).Error; err != nil {
			t.Fatalf("seed post %d: %v", posts[i].ID, err)
		}
	}

	// Votes CSV: voter1 (muted later in the test), voter2 stay.
	votesCSV := "voter1,1000,10000,25\nvoter2,2000,10000,40"

	caches := []models.PostCache{
		{
			PostID: 1, Author: "alice", Permlink: "unpaid-root", Category: "life",
			Title: "Root Title", Depth: 0, Payout: 5, CreatedAt: created,
			PayoutAt: unpaidCashout, UpdatedAt: created, IsPaidout: false,
			Votes: votesCSV, RawJSON: unpaidRootRawJSON,
		},
		{
			PostID: 2, Author: "bob", Permlink: "paid-comment", Category: "life",
			Title: "RE: Root Title", Depth: 1, Payout: 3, CreatedAt: created,
			PayoutAt: paidAt, UpdatedAt: created, IsPaidout: true,
			RawJSON: paidCommentRawJSON,
		},
		{
			PostID: 3, Author: "alice", Permlink: "deleted-root", Category: "life",
			Title: "Gone", Depth: 0, CreatedAt: created, PayoutAt: created,
			UpdatedAt: created, RawJSON: unpaidRootRawJSON,
		},
		{
			PostID: 4, Author: "alice", Permlink: "nocat-root", Category: "",
			Title: "NoCat", Depth: 0, CreatedAt: created, PayoutAt: created,
			UpdatedAt: created, RawJSON: nocatRawJSON,
		},
		{
			PostID: 5, Author: "carol", Permlink: "reply-ok", Category: "life",
			Title: "hi", Depth: 1, CreatedAt: created, PayoutAt: created,
			UpdatedAt: created, RawJSON: paidCommentRawJSON,
		},
		{
			PostID: 6, Author: "carol", Permlink: "reply-deleted", Category: "life",
			Title: "bye", Depth: 1, CreatedAt: created, PayoutAt: created,
			UpdatedAt: created, RawJSON: paidCommentRawJSON,
		},
	}
	for i := range caches {
		if err := gdb.Create(&caches[i]).Error; err != nil {
			t.Fatalf("seed cache %d: %v", caches[i].PostID, err)
		}
	}
}
