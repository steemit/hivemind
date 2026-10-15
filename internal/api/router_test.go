// Registry alignment tests (planned PR#418, 03-api.md §一 1.1):
//
//   - TestRegistryGolden locks the registered method set to exactly
//     68 legacy names ∪ 9 Go-only names (= 77). The legacy list was
//     extracted mechanically from hive/server/serve.py build_methods()
//     (AST walk of every methods.add call: dict-comprehension prefixes
//     resolved against the referenced function tuples, plus the explicit
//     string-keyed entries) — do not hand-edit names; re-run the
//     extraction or change the registry explicitly.
//   - TestNewAliasesDispatch covers every alias added for parity: each
//     call must reach the aliased handler (a validation PublicError, not
//     method-not-found), without needing a database.
//   - TestCallAdapterDispatch covers the bare `call` adapter wire shape
//     and its legacy _strict_list/_strict_query rejections.
package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
)

// legacyRegistry is the 68-name method set the legacy server registers
// (serve.py build_methods). Source: AST extraction, 2026-10-14.
var legacyRegistry = []string{
	"bridge.account_notifications",
	"bridge.get_account_posts",
	"bridge.get_community",
	"bridge.get_community_context",
	"bridge.get_discussion",
	"bridge.get_payout_stats",
	"bridge.get_post",
	"bridge.get_post_header",
	"bridge.get_profile",
	"bridge.get_ranked_posts",
	"bridge.get_trending_topics",
	"bridge.list_all_subscriptions",
	"bridge.list_communities",
	"bridge.list_community_roles",
	"bridge.list_pop_communities",
	"bridge.list_subscribers",
	"bridge.normalize_post",
	"bridge.post_notifications",
	"bridge.unread_notifications",
	"call",
	"condenser_api.get_account_reputations",
	"condenser_api.get_account_votes",
	"condenser_api.get_blog",
	"condenser_api.get_blog_entries",
	"condenser_api.get_comment_discussions_by_payout",
	"condenser_api.get_content",
	"condenser_api.get_content_replies",
	"condenser_api.get_discussions_by_author_before_date",
	"condenser_api.get_discussions_by_blog",
	"condenser_api.get_discussions_by_comments",
	"condenser_api.get_discussions_by_created",
	"condenser_api.get_discussions_by_feed",
	"condenser_api.get_discussions_by_hot",
	"condenser_api.get_discussions_by_promoted",
	"condenser_api.get_discussions_by_trending",
	"condenser_api.get_follow_count",
	"condenser_api.get_followers",
	"condenser_api.get_followers_by_page",
	"condenser_api.get_following",
	"condenser_api.get_following_by_page",
	"condenser_api.get_post_discussions_by_payout",
	"condenser_api.get_reblogged_by",
	"condenser_api.get_replies_by_last_update",
	"condenser_api.get_state",
	"condenser_api.get_transaction",
	"condenser_api.get_trending_tags",
	"follow_api.get_account_reputations",
	"follow_api.get_blog",
	"follow_api.get_blog_entries",
	"follow_api.get_follow_count",
	"follow_api.get_followers",
	"follow_api.get_followers_by_page",
	"follow_api.get_following",
	"follow_api.get_following_by_page",
	"follow_api.get_reblogged_by",
	"hive.db_head_state",
	"tags_api.get_account_votes",
	"tags_api.get_comment_discussions_by_payout",
	"tags_api.get_content_replies",
	"tags_api.get_discussion",
	"tags_api.get_discussions_by_author_before_date",
	"tags_api.get_discussions_by_blog",
	"tags_api.get_discussions_by_comments",
	"tags_api.get_discussions_by_created",
	"tags_api.get_discussions_by_hot",
	"tags_api.get_discussions_by_promoted",
	"tags_api.get_discussions_by_trending",
	"tags_api.get_post_discussions_by_payout",
}

// goOnlyRegistry is the registered Go-only surface: the hive_api.* List*
// family (legacy never exposes hive_api methods; D4 keeps them) plus
// bridge.list_top_communities (additive alias of list_pop_communities).
var goOnlyRegistry = []string{
	"bridge.list_top_communities",
	"hive_api.get_account",
	"hive_api.get_accounts",
	"hive_api.list_account_blog",
	"hive_api.list_account_feed",
	"hive_api.list_account_posts",
	"hive_api.list_all_muted",
	"hive_api.list_followers",
	"hive_api.list_following",
}

// newRegistryRouter builds a Router without a live database: method
// registration only constructs handler objects and never touches the
// connection, so a zero DB is enough to inspect the registry.
func newRegistryRouter(t *testing.T) *Router {
	t.Helper()
	gin.SetMode(gin.TestMode)
	return NewRouter(&db.DB{}, nil, nil, "", 0)
}

func TestRegistryGolden(t *testing.T) {
	router := newRegistryRouter(t)
	got := router.handler.RegisteredMethods()

	wantSet := map[string]bool{}
	for _, name := range legacyRegistry {
		if wantSet[name] {
			t.Fatalf("duplicate name in legacyRegistry golden list: %s", name)
		}
		wantSet[name] = true
	}
	for _, name := range goOnlyRegistry {
		if wantSet[name] {
			t.Fatalf("duplicate name across golden lists: %s", name)
		}
		wantSet[name] = true
	}

	gotSet := map[string]bool{}
	for _, name := range got {
		if gotSet[name] {
			t.Fatalf("duplicate registered method: %s", name)
		}
		gotSet[name] = true
	}

	if len(got) != len(wantSet) {
		t.Errorf("registry has %d methods, golden expects %d", len(got), len(wantSet))
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Errorf("method %q is registered but absent from the golden lists; if intentional, update legacyRegistry/goOnlyRegistry explicitly", name)
		}
	}
	for name := range wantSet {
		if !gotSet[name] {
			t.Errorf("method %q is in the golden lists but not registered", name)
		}
	}
}

// TestRegistryCounts breaks the totals out for quick inspection; the
// set-level comparison above is the authoritative check.
func TestRegistryCounts(t *testing.T) {
	router := newRegistryRouter(t)
	names := router.handler.RegisteredMethods()
	counts := map[string]int{}
	for _, name := range names {
		ns := name
		if i := strings.IndexByte(name, '.'); i >= 0 {
			ns = name[:i]
		}
		counts[ns]++
	}
	for ns, want := range map[string]int{
		"hive": 1, "condenser_api": 26, "follow_api": 9, "tags_api": 12,
		"bridge": 20, "hive_api": 8, "call": 1,
	} {
		if counts[ns] != want {
			t.Errorf("namespace %s registers %d methods, want %d", ns, counts[ns], want)
		}
	}
	if total := len(names); total != 77 {
		t.Errorf("registry total = %d, want 77", total)
	}
}

// TestNewAliasesDispatch exercises every alias added by PR#418 through the
// full JSON-RPC dispatch path: each must reach its target handler — proven
// by the handler's own validation PublicError — and never -32601.
func TestNewAliasesDispatch(t *testing.T) {
	router := newRegistryRouter(t)
	engine := gin.New()
	engine.POST("/", router.handler.Handle)

	cases := []struct {
		method   string
		params   string
		wantData string
	}{
		// bare `call`: non-condenser api is refused first (legacy call.py
		// asserts api == 'condenser_api').
		{"call", `["database_api","get_state",["trending"]]`, "`call` requires condenser_api"},

		// follow_api aliases -> condenser handlers
		{"follow_api.get_followers_by_page", `["ab",0,5]`, "invalid account name length: `ab`"},
		{"follow_api.get_following_by_page", `["ab",0,5]`, "invalid account name length: `ab`"},
		{"follow_api.get_account_reputations", `["ab",-1]`, "limit must be positive"},
		{"follow_api.get_reblogged_by", `["ab","some-permlink"]`, "invalid account name length: `ab`"},

		// tags_api aliases -> condenser handlers
		{"tags_api.get_discussion", `["ab","some-permlink"]`, "invalid account name length: `ab`"},
		{"tags_api.get_content_replies", `["ab","some-permlink"]`, "invalid account name length: `ab`"},
		{"tags_api.get_discussions_by_author_before_date", `["ab","","",10]`, "invalid account name length: `ab`"},
		{"tags_api.get_post_discussions_by_payout", `{"start_author":"ab","limit":5}`, "invalid account name length: `ab`"},
		{"tags_api.get_comment_discussions_by_payout", `{"start_author":"ab","limit":5}`, "invalid account name length: `ab`"},
	}

	// tags_api.get_account_votes is asserted separately below: its target
	// handler is the not-supported dummy, whose classification is -32000
	// ("Server error"), not the -32602 validation class this loop expects.

	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			w := doPost(t, engine, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, tc.method, tc.params))
			obj := decodeObject(t, w)
			errObj, ok := obj["error"].(map[string]interface{})
			if !ok {
				t.Fatalf("expected an error response, got %v", obj)
			}
			if code := errObj["code"].(float64); code != -32602 {
				t.Fatalf("error code = %v, want -32602 (reached the handler; -32601 would mean a missing alias)", code)
			}
			if msg := errObj["message"].(string); msg != "Invalid parameters" {
				t.Errorf("error message = %q, want Invalid parameters", msg)
			}
			if data := errObj["data"].(string); data != tc.wantData {
				t.Errorf("error data = %q, want %q", data, tc.wantData)
			}
		})
	}
}

// TestNotSupportedClassification pins the -32000 mapping of the
// not-supported class (planned PR#421, #408 audit): the dummy
// get_account_votes (direct, tags_api alias and via the `call` adapter) and
// the not-implemented get_state refusal must answer exactly like the legacy
// ApiError route through jsonrpcserver 4.0.1's ExceptionResponse — code
// -32000, message "Server error", and NO error.data (Go's registered
// divergence: legacy debug dispatch adds "ApiError: <msg>" as data, which
// the comparison tooling whitelists because it judges error.message only).
// A validation PublicError must keep its -32602 mapping, proving the two
// classes stay distinct.
func TestNotSupportedClassification(t *testing.T) {
	router := newRegistryRouter(t)
	engine := gin.New()
	engine.POST("/", router.handler.Handle)

	cases := []struct {
		method string
		params string
	}{
		{"condenser_api.get_account_votes", `["alice"]`},
		{"tags_api.get_account_votes", `["alice"]`},
		{"condenser_api.get_state", `["trending"]`},
		{"call", `["condenser_api","get_account_votes",["alice"]]`},
		{"call", `["condenser_api","get_state",["trending"]]`},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			w := doPost(t, engine, fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, tc.method, tc.params))
			obj := decodeObject(t, w)
			errObj, ok := obj["error"].(map[string]interface{})
			if !ok {
				t.Fatalf("expected an error response, got %v", obj)
			}
			if code := errObj["code"].(float64); code != -32000 {
				t.Fatalf("error code = %v, want -32000 (legacy ApiError -> ExceptionResponse)", code)
			}
			if msg := errObj["message"].(string); msg != "Server error" {
				t.Errorf("error message = %q, want \"Server error\" (legacy ExceptionResponse message; the refusal text lives in legacy debug data)", msg)
			}
			if data, has := errObj["data"]; has {
				t.Errorf("error data = %v, want absent (Go omits data on -32000; registered whitelist)", data)
			}
		})
	}

	// Control: a validation PublicError through the same dispatch path must
	// still map to -32602 — the not-supported class must not swallow it.
	w := doPost(t, engine, `{"jsonrpc":"2.0","id":1,"method":"condenser_api.get_follow_count","params":["ab"]}`)
	obj := decodeObject(t, w)
	errObj, _ := obj["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatalf("expected an error response, got %v", obj)
	}
	if code := errObj["code"].(float64); code != -32602 {
		t.Errorf("validation error code = %v, want -32602", code)
	}
	if data := errObj["data"].(string); data != "invalid account name length: `ab`" {
		t.Errorf("validation error data = %q, want the PublicError text", data)
	}
}

// TestCallAdapterDispatch covers the bare `call` adapter's own validation:
// arity, api guard, strict list/query rejections and the unknown-method
// fallback — all legacy call.py behavior, none needing a database.
func TestCallAdapterDispatch(t *testing.T) {
	router := newRegistryRouter(t)
	engine := gin.New()
	engine.POST("/", router.handler.Handle)

	call := func(t *testing.T, params string) map[string]interface{} {
		t.Helper()
		w := doPost(t, engine, fmt.Sprintf(`{"jsonrpc":"2.0","id":7,"method":"call","params":%s}`, params))
		return decodeObject(t, w)
	}

	t.Run("wrong arity", func(t *testing.T) {
		obj := call(t, `["condenser_api","get_state"]`)
		assertRPCErrorData(t, obj, "expected 3 params")
		obj = call(t, `["condenser_api","get_state",["trending"],"extra"]`)
		assertRPCErrorData(t, obj, "expected 3 params")
	})

	t.Run("non condenser api", func(t *testing.T) {
		obj := call(t, `["database_api","get_dynamic_global_properties",[]]`)
		assertRPCErrorData(t, obj, "`call` requires condenser_api")
	})

	t.Run("strict list", func(t *testing.T) {
		obj := call(t, `["condenser_api","get_followers",["alice","","blog"]]`)
		assertRPCErrorData(t, obj, "expected 4 params")
		obj = call(t, `["condenser_api","get_followers",["alice","","blog",10,1]]`)
		assertRPCErrorData(t, obj, "expected 4 params")
		obj = call(t, `["condenser_api","get_followers","alice"]`)
		assertRPCErrorData(t, obj, "params not a list")
		obj = call(t, `["condenser_api","get_blog",["alice"]]`)
		assertRPCErrorData(t, obj, "expected 3 params")
	})

	t.Run("strict query", func(t *testing.T) {
		obj := call(t, `["condenser_api","get_discussions_by_trending",[{"tag":"steem"}]]`)
		assertRPCErrorData(t, obj, "missing query key {'limit'}")
		obj = call(t, `["condenser_api","get_discussions_by_trending",[{"limit":5,"filter_tags":["nsfw"]}]]`)
		assertRPCErrorData(t, obj, "filter_tags not supported")
		obj = call(t, `["condenser_api","get_discussions_by_trending",[{"limit":5,"select_tags":["life"]}]]`)
		assertRPCErrorData(t, obj, "select_tags not supported")
		obj = call(t, `["condenser_api","get_discussions_by_trending",[{"limit":5,"mystery":1}]]`)
		assertRPCErrorData(t, obj, "unknown query key {'mystery'}")
		obj = call(t, `["condenser_api","get_discussions_by_trending",["steem"]]`)
		assertRPCErrorData(t, obj, "query must be dict")
		obj = call(t, `["condenser_api","get_discussions_by_trending",[]]`)
		assertRPCErrorData(t, obj, "expected 1 params")
	})

	t.Run("routed refusals", func(t *testing.T) {
		// get_state is the registered D1 deviation; the adapter must
		// forward to it rather than answer unknown-method. Both refusals
		// are the not-supported class (-32000 "Server error", no data);
		// the full classification matrix is pinned by
		// TestNotSupportedClassification above.
		obj := call(t, `["condenser_api","get_state",["trending"]]`)
		assertRPCServerError(t, obj)
		obj = call(t, `["condenser_api","get_account_votes",["alice"]]`)
		assertRPCServerError(t, obj)
		obj = call(t, `["condenser_api","no_such_method",[]]`)
		assertRPCErrorData(t, obj, "unknown method: condenser_api.no_such_method")
	})
}

func assertRPCServerError(t *testing.T, obj map[string]interface{}) {
	t.Helper()
	errObj, ok := obj["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected an error response, got %v", obj)
	}
	if code := errObj["code"].(float64); code != -32000 {
		t.Fatalf("error code = %v, want -32000", code)
	}
	if msg := errObj["message"].(string); msg != "Server error" {
		t.Fatalf("error message = %q, want \"Server error\"", msg)
	}
	if data, has := errObj["data"]; has {
		t.Fatalf("error data = %v, want absent", data)
	}
}

func assertRPCErrorData(t *testing.T, obj map[string]interface{}, want string) {
	t.Helper()
	errObj, ok := obj["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected an error response, got %v", obj)
	}
	if code := errObj["code"].(float64); code != -32602 {
		t.Fatalf("error code = %v, want -32602", code)
	}
	if data := errObj["data"].(string); data != want {
		t.Fatalf("error data = %q, want %q", data, want)
	}
}
