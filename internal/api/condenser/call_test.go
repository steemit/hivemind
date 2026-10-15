package condenser

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
)

// callRaw invokes a CallAPI route with raw params JSON (same helper shape
// as the call/callErr helpers used by the other condenser tests).
func callRaw(t *testing.T, adapter *CallAPI, raw string) (interface{}, error) {
	t.Helper()
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	return adapter.Call(ctx, json.RawMessage(raw))
}

// TestCallAdapterValidation exercises the adapter's argument validation
// without a database: api guard, arity, strict list/query checks and the
// unknown-method fallback (legacy condenser_api/call.py).
func TestCallAdapterValidation(t *testing.T) {
	adapter := NewCallAPI(nil, nil, nil, nil, nil, nil)

	cases := []struct {
		name     string
		params   string
		wantData string
	}{
		{"non condenser api", `["database_api","get_state",["trending"]]`, "`call` requires condenser_api"},
		{"non string api", `[5,"get_state",[]]`, "`call` requires condenser_api"},
		{"too few top-level", `["condenser_api","get_state"]`, "expected 3 params"},
		{"too many top-level", `["condenser_api","get_state",["trending"],1]`, "expected 3 params"},
		{"list arity short", `["condenser_api","get_followers",["alice","","blog"]]`, "expected 4 params"},
		{"list arity long", `["condenser_api","get_followers",["a","","blog",1,2]]`, "expected 4 params"},
		{"args not a list", `["condenser_api","get_followers","alice"]`, "params not a list"},
		{"blog needs 2-3", `["condenser_api","get_blog",["alice"]]`, "expected 3 params"},
		{"query must be dict", `["condenser_api","get_discussions_by_trending",["steem"]]`, "query must be dict"},
		{"query arity", `["condenser_api","get_discussions_by_trending",[]]`, "expected 1 params"},
		{"query missing limit", `["condenser_api","get_discussions_by_trending",[{"tag":"steem"}]]`, "missing query key {'limit'}"},
		{"query filter_tags", `["condenser_api","get_discussions_by_trending",[{"limit":5,"filter_tags":["x"]}]]`, "filter_tags not supported"},
		{"query select_tags", `["condenser_api","get_discussions_by_trending",[{"limit":5,"select_tags":["x"]}]]`, "select_tags not supported"},
		{"query unknown key", `["condenser_api","get_discussions_by_trending",[{"limit":5,"nope":1}]]`, "unknown query key {'nope'}"},
		{"unknown method", `["condenser_api","get_xyz",[]]`, "unknown method: condenser_api.get_xyz"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := callRaw(t, adapter, tc.params)
			if err == nil {
				t.Fatal("expected an error")
			}
			pe, ok := err.(apierrors.PublicError)
			if !ok {
				t.Fatalf("error type = %T, want PublicError", err)
			}
			if string(pe) != tc.wantData {
				t.Errorf("error = %q, want %q", string(pe), tc.wantData)
			}
		})
	}
}

// TestStrictQueryNormalization checks the blank-key stripping and
// select_authors drop of _strict_query (call.py): a query carrying blank
// optional keys and a select_authors list is cleaned to exactly the keys
// legacy would forward as kwargs.
func TestStrictQueryNormalization(t *testing.T) {
	cleaned, err := strictQuery(json.RawMessage(`[{"tag":"","start_author":"","start_permlink":"","select_authors":["nobody"],"limit":5,"truncate_body":3}]`))
	if err != nil {
		t.Fatalf("strictQuery: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(cleaned, &m); err != nil {
		t.Fatalf("cleaned query is not JSON: %v", err)
	}
	for _, key := range []string{"tag", "start_author", "start_permlink", "select_authors"} {
		if _, ok := m[key]; ok {
			t.Errorf("cleaned query still carries %q: %v", key, m)
		}
	}
	if m["limit"] != float64(5) || m["truncate_body"] != float64(3) {
		t.Errorf("cleaned query = %v, want limit 5 and truncate_body 3 only", m)
	}

	// Non-blank filter_tags still rejected after the blank pass.
	if _, err := strictQuery(json.RawMessage(`[{"filter_tags":["nsfw"],"limit":5}]`)); err == nil {
		t.Error("non-blank filter_tags must be rejected")
	}
	// Null is falsy in Python, so a null optional key is stripped; a
	// zero truncate_body is NOT in the blank-keys list and stays.
	cleaned, err = strictQuery(json.RawMessage(`[{"start_author":null,"truncate_body":0,"limit":1}]`))
	if err != nil {
		t.Fatalf("strictQuery falsy strip: %v", err)
	}
	if err := json.Unmarshal(cleaned, &m); err != nil {
		t.Fatalf("cleaned query is not JSON: %v", err)
	}
	if _, ok := m["start_author"]; ok {
		t.Errorf("cleaned query still carries null start_author: %v", m)
	}
	if m["limit"] != float64(1) || m["truncate_body"] != float64(0) {
		t.Errorf("cleaned query = %v, want limit 1 and truncate_body 0", m)
	}
}

// TestCallAdapterDispatch runs positive `call` routes against a real
// Postgres schema and compares them with the direct condenser handler
// results. Requires HIVE_TEST_DATABASE_URL.
func TestCallAdapterDispatch(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping call adapter integration test")
	}
	dbURL := newNamedTestDB(t, "hive_condenser_call_test")
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

	seedCondenserFixture(t, gdb)

	gin.SetMode(gin.TestMode)
	repo := db.NewRepository(gdb)
	wrapped := &db.DB{DB: gdb}
	follow := NewFollowAPI(repo)
	follow.SetDatabase(gdb)
	content := NewContentAPI(repo)
	content.SetDatabase(gdb)
	discussions := NewDiscussionsAPI(repo, wrapped)
	blog := NewBlogAPI(repo, wrapped)
	tags := NewTagsAPI(repo, wrapped)
	misc := NewMiscAPI(repo, wrapped, nil)
	adapter := NewCallAPI(follow, content, discussions, blog, tags, misc)

	// get_content via `call` == direct get_content.
	viaCall, err := callRaw(t, adapter, `["condenser_api","get_content",["alice","post-a"]]`)
	if err != nil {
		t.Fatalf("call get_content: %v", err)
	}
	direct := call(t, content.GetContent, `["alice","post-a"]`)
	callPost := viaCall.(map[string]interface{})
	directPost := direct.(map[string]interface{})
	if callPost["author"] != "alice" || callPost["permlink"] != "post-a" {
		t.Errorf("call get_content = %v/%v, want alice/post-a", callPost["author"], callPost["permlink"])
	}
	if callPost["id"] != directPost["id"] || callPost["title"] != directPost["title"] {
		t.Errorf("call get_content diverges from direct: %v vs %v", callPost, directPost)
	}

	// get_content_replies via `call`.
	replies, err := callRaw(t, adapter, `["condenser_api","get_content_replies",["alice","post-a"]]`)
	if err != nil {
		t.Fatalf("call get_content_replies: %v", err)
	}
	if list := replies.([]map[string]interface{}); len(list) != 1 || list[0]["permlink"] != "reply-y" {
		t.Errorf("call get_content_replies = %v, want [reply-y]", replies)
	}

	// Query-style route with blank keys and select_authors (cleaned away):
	// the adapter result must equal the direct handler result.
	viaCallTrending, err := callRaw(t, adapter,
		`["condenser_api","get_discussions_by_trending",[{"tag":"","select_authors":["nobody"],"limit":10}]]`)
	if err != nil {
		t.Fatalf("call get_discussions_by_trending: %v", err)
	}
	directTrending := call(t, discussions.GetDiscussionsByTrending, `{"tag":"","select_authors":["nobody"],"limit":10}`)
	callList := viaCallTrending.([]map[string]interface{})
	directList := directTrending.([]map[string]interface{})
	if len(callList) != len(directList) {
		t.Fatalf("call get_discussions_by_trending = %d posts, direct = %d", len(callList), len(directList))
	}
	for i := range callList {
		if callList[i]["id"] != directList[i]["id"] {
			t.Errorf("trending[%d]: call = %v, direct = %v", i, callList[i]["id"], directList[i]["id"])
		}
	}

	// get_blog via `call` with the 2-arg window.
	entries, err := callRaw(t, adapter, `["condenser_api","get_blog",["alice",1]]`)
	if err != nil {
		t.Fatalf("call get_blog: %v", err)
	}
	if list := entries.([]map[string]interface{}); len(list) != 2 {
		t.Errorf("call get_blog(alice,1) = %d entries, want 2", len(list))
	}

	// get_replies_by_last_update via `call` (exactly 3 args).
	repliesByLU, err := callRaw(t, adapter, `["condenser_api","get_replies_by_last_update",["alice","",10]]`)
	if err != nil {
		t.Fatalf("call get_replies_by_last_update: %v", err)
	}
	if list := repliesByLU.([]map[string]interface{}); len(list) != 1 || list[0]["permlink"] != "reply-y" {
		t.Errorf("call get_replies_by_last_update = %v, want [reply-y]", repliesByLU)
	}

	// get_account_votes dummy and get_state refusal route through too.
	if _, err := callRaw(t, adapter, `["condenser_api","get_account_votes",["alice"]]`); err == nil {
		t.Error("call get_account_votes must return the informational error")
	} else if pe, ok := err.(apierrors.PublicError); !ok || string(pe) != "get_account_votes is no longer supported" {
		t.Errorf("call get_account_votes error = %v, want the legacy dummy message", err)
	}
	if _, err := callRaw(t, adapter, `["condenser_api","get_state",["trending"]]`); err == nil {
		t.Error("call get_state must return the registered not-implemented refusal")
	}
}

// TestDiscussionBlankParamMessages pins the legacy blank-value asserts that
// run before valid_account (methods.py:260/274/296/310/330): the five
// discussion methods must report "`<key>` cannot be blank", never
// "invalid account (not specified)".
func TestDiscussionBlankParamMessages(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// An empty db.DB is sufficient: the blank-param assertions fire before
	// any database access, so the handlers never touch the nil connection.
	emptyDB := &db.DB{}
	repo := db.NewRepository(nil)
	misc := NewMiscAPI(repo, emptyDB, nil)
	disc := NewDiscussionsAPI(repo, emptyDB)
	cases := []struct {
		name   string
		call   func(*gin.Context, json.RawMessage) (interface{}, error)
		params string
		want   string
	}{
		{"blog_blank_tag", disc.GetDiscussionsByBlog, `[{"tag":"","limit":1}]`, "`tag` cannot be blank"},
		{"feed_blank_tag", disc.GetDiscussionsByFeed, `[{"tag":"","limit":1}]`, "`tag` cannot be blank"},
		{"comments_blank_author", misc.GetDiscussionsByComments, `[{"start_author":"","limit":1}]`, "`start_author` cannot be blank"},
		{"replies_blank_author", misc.GetRepliesByLastUpdate, `[{"start_author":""}]`, "`start_author` cannot be blank"},
		{"author_before_blank", misc.GetDiscussionsByAuthorBeforeDate, `[{"author":""}]`, "`author` cannot be blank"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "/", nil)
			_, err := tc.call(c, json.RawMessage(tc.params))
			if err == nil {
				t.Fatal("expected error for blank param")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.want)
			}
			if strings.Contains(err.Error(), "invalid account") {
				t.Fatalf("err = %q must not fall through to valid_account", err.Error())
			}
		})
	}
}
