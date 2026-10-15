package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
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

// TestNormalizePost drives bridge.normalize_post against a live Postgres and
// the REAL steemd get_content shape (fixture fetched from
// api.steemit.com/condenser_api.get_content for steemit/firstpost).
//
// Expected values were derived by replaying the legacy pipeline verbatim
// (bridge support.py:41-103 + utils/post.py + bridge objects.py
// _condenser_post_object) against the same inputs, covering:
//
//   - A: paid post, NO hive_posts row -> post_id null, input category,
//     payout split author/curator (sum total+curator+pending as Decimal)
//   - B: pending community post, promoted, core gray/hide override,
//     community decoration incl. the support.py ROLES quirk (4 -> admin)
//   - C: paid non-community post with a core row (is_muted/is_valid are dead
//     flags without a community), json_metadata image sanitation
//   - D: comment (depth 1) -> RE: title + parent fields from raw_json
//   - E: deleted core row still supplies core metadata (no is_deleted filter)
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set.
func TestNormalizePost(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping normalize_post test")
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

	fixture := loadSteemdFixture(t)
	seedNormalizePost(t, gdb)

	repo := db.NewRepository(gdb)
	api := NewPostAPI(repo)

	call := func(post map[string]interface{}) map[string]interface{} {
		t.Helper()
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		params, _ := json.Marshal(map[string]interface{}{"post": post})
		result, err := api.NormalizePost(ctx, params)
		if err != nil {
			t.Fatalf("NormalizePost(%s): %v", post["permlink"], err)
		}
		ret, ok := result.(map[string]interface{})
		if !ok {
			t.Fatalf("expected map result, got %T", result)
		}
		return ret
	}

	t.Run("A_paid_post_no_core_row", func(t *testing.T) {
		ret := call(fixture)

		assertKeySet(t, ret, nil) // base keys only
		assertStatsShape(t, ret)

		// Core enrichment fallback: no hive_posts row -> post_id null,
		// category stays the input's, no community decoration. (The map
		// holds a typed (*int64)(nil), so compare via JSON rendering.)
		if gj, _ := json.Marshal(ret["post_id"]); string(gj) != "null" {
			t.Errorf("post_id: expected null, got %s", gj)
		}
		if ret["category"] != "meta" {
			t.Errorf("category: expected meta, got %v", ret["category"])
		}

		// Values from the INPUT steemd object (post_to_internal pipeline).
		assertEq(t, "author", ret["author"], "steemit")
		assertEq(t, "permlink", ret["permlink"], "firstpost")
		assertEq(t, "title", ret["title"], "Welcome to Steem!")
		assertEq(t, "created", ret["created"], "2016-03-30T18:30:18")
		assertEq(t, "updated", ret["updated"], "2016-03-30T18:30:18")
		assertEq(t, "payout_at", ret["payout_at"], "2016-08-24T19:59:42") // last_payout once paid
		assertEq(t, "is_paidout", ret["is_paidout"], true)
		assertEq(t, "depth", ret["depth"], float64(0))
		assertEq(t, "children", ret["children"], float64(445))
		assertEq(t, "net_rshares", ret["net_rshares"], float64(830053779138)) // sum of vote rshares
		assertEq(t, "url", ret["url"], "/meta/@steemit/firstpost")
		assertEq(t, "body", ret["body"], fixture["body"])
		assertEq(t, "json_metadata", ret["json_metadata"], map[string]interface{}{}) // fixture has ""
		assertEq(t, "beneficiaries", ret["beneficiaries"], []interface{}{})
		assertEq(t, "max_accepted_payout", ret["max_accepted_payout"], "1000000.000 SBD")
		assertEq(t, "percent_steem_dollars", ret["percent_steem_dollars"], float64(10000))
		assertEq(t, "replies", ret["replies"], []interface{}{})

		// Paid payout split: pending 0; author = total - curator (Decimals:
		// 0.942 + 0.756 + 0.000 = 1.698; author 1.698 - 0.756 = 0.942).
		assertEq(t, "payout", ret["payout"], 1.698)
		assertEq(t, "pending_payout_value", ret["pending_payout_value"], "0.000 SBD")
		assertEq(t, "author_payout_value", ret["author_payout_value"], "0.942 SBD")
		assertEq(t, "curator_payout_value", ret["curator_payout_value"], "0.756 SBD")
		assertEq(t, "promoted", ret["promoted"], "0.000 SBD") // "0.000 STEEM" sentinel

		// author_reputation comes from hive_accounts, not the input.
		assertEq(t, "author_reputation", ret["author_reputation"], 42.5)

		// stats from post_stats over the input votes: 92 non-zero-rshares
		// votes, no flags (input rep "12944616889" -> ~62).
		stats := ret["stats"].(map[string]interface{})
		assertEq(t, "stats.total_votes", stats["total_votes"], float64(92))
		assertEq(t, "stats.flag_weight", stats["flag_weight"], float64(0))
		assertEq(t, "stats.hide", stats["hide"], false)
		assertEq(t, "stats.gray", stats["gray"], false)

		assertVotes(t, ret)
	})

	t.Run("B_pending_community_post", func(t *testing.T) {
		post := cloneFixture(t)
		post["permlink"] = "community-post"
		post["category"] = "hive-123456"
		post["cashout_time"] = "2027-01-01T00:00:00"
		post["last_payout"] = "1969-12-31T23:59:59"
		post["total_payout_value"] = "0.000 SBD"
		post["curator_payout_value"] = "0.000 SBD"
		post["pending_payout_value"] = "12.345 SBD"
		post["promoted"] = "5.000 SBD"
		post["json_metadata"] = `{"tags":["hive-123456","life"],"image":["not-a-url","https://img.example.org/a.png"]}`

		ret := call(post)

		assertKeySet(t, ret, []string{"community_title", "author_role", "author_title"})

		assertEq(t, "post_id", ret["post_id"], float64(777))
		assertEq(t, "category", ret["category"], "hive-123456") // from hive_posts core
		assertEq(t, "is_paidout", ret["is_paidout"], false)
		assertEq(t, "payout_at", ret["payout_at"], "2027-01-01T00:00:00") // cashout while pending

		// Pending payout split: the whole total is pending.
		assertEq(t, "payout", ret["payout"], 12.345)
		assertEq(t, "pending_payout_value", ret["pending_payout_value"], "12.345 SBD")
		assertEq(t, "author_payout_value", ret["author_payout_value"], "0.000 SBD")
		assertEq(t, "curator_payout_value", ret["curator_payout_value"], "0.000 SBD")
		assertEq(t, "promoted", ret["promoted"], "5.000 SBD")

		// json_metadata sanitation: invalid image dropped, tags kept.
		assertEq(t, "json_metadata", ret["json_metadata"], map[string]interface{}{
			"image": []interface{}{"https://img.example.org/a.png"},
			"tags":  []interface{}{"hive-123456", "life"},
		})

		// Community posts override the display flags with core columns:
		// gray = is_muted, hide = !is_valid (utils/post.py:73-79).
		stats := ret["stats"].(map[string]interface{})
		assertEq(t, "stats.gray", stats["gray"], true)
		assertEq(t, "stats.hide", stats["hide"], true)

		// Community decoration. support.py:16 maps role 4 to "admin" (NOT
		// "mod" as objects.py does) -- replicated bug-for-bug.
		assertEq(t, "community_title", ret["community_title"], "My Community")
		assertEq(t, "author_role", ret["author_role"], "admin")
		assertEq(t, "author_title", ret["author_title"], "moderator")
	})

	t.Run("C_paid_post_core_row_no_community", func(t *testing.T) {
		post := cloneFixture(t)
		post["permlink"] = "paid-post"

		ret := call(post)

		assertKeySet(t, ret, nil)

		assertEq(t, "post_id", ret["post_id"], float64(888))
		assertEq(t, "category", ret["category"], "meta")
		assertEq(t, "payout", ret["payout"], 1.698)
		assertEq(t, "pending_payout_value", ret["pending_payout_value"], "0.000 SBD")
		assertEq(t, "author_payout_value", ret["author_payout_value"], "0.942 SBD")
		assertEq(t, "curator_payout_value", ret["curator_payout_value"], "0.756 SBD")

		// Without a community, the core is_muted/is_valid flags do NOT reach
		// stats; they are computed from the input votes/reputation instead.
		stats := ret["stats"].(map[string]interface{})
		assertEq(t, "stats.gray", stats["gray"], false)
		assertEq(t, "stats.hide", stats["hide"], false)
	})

	t.Run("D_comment_title_rewrite", func(t *testing.T) {
		post := cloneFixture(t)
		post["permlink"] = "re-firstpost"
		post["depth"] = 1
		post["parent_author"] = "steemit"
		post["parent_permlink"] = "firstpost"

		ret := call(post)

		assertKeySet(t, ret, []string{"parent_author", "parent_permlink"})
		assertEq(t, "title", ret["title"], "RE: Welcome to Steem!")
		assertEq(t, "parent_author", ret["parent_author"], "steemit")
		assertEq(t, "parent_permlink", ret["parent_permlink"], "firstpost")
		assertEq(t, "depth", ret["depth"], float64(1)) // input depth, not the core row's
	})

	t.Run("E_deleted_core_row_still_used", func(t *testing.T) {
		post := cloneFixture(t)
		post["permlink"] = "deleted-post"

		ret := call(post)

		// Legacy core query has NO is_deleted filter (support.py:46-49):
		// a deleted post still supplies id/category (unlike get_post).
		assertEq(t, "post_id", ret["post_id"], float64(555))
		assertEq(t, "category", ret["category"], "meta-deleted")
	})

	t.Run("community_row_missing_title_null", func(t *testing.T) {
		post := cloneFixture(t)
		post["permlink"] = "orphan-community-post"

		ret := call(post)

		// community row gone (e.g. fork rollback) -> community_title null,
		// NO category fallback on this endpoint (support.py:91 differs from
		// objects.py:136), guest role.
		if ret["community_title"] != nil {
			t.Errorf("community_title: expected nil, got %v", ret["community_title"])
		}
		assertEq(t, "author_role", ret["author_role"], "guest")
		assertEq(t, "author_title", ret["author_title"], "")
	})

	t.Run("author_missing_is_invalid_params", func(t *testing.T) {
		post := cloneFixture(t)
		post["author"] = "who-does-not-exist"

		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		params, _ := json.Marshal(map[string]interface{}{"post": post})
		_, err := api.NormalizePost(ctx, params)
		if err == nil {
			t.Fatal("expected error for unknown author")
		}
		// Legacy dereferences the None row (TypeError, support.py:82)
		// which jsonrpcserver maps to -32602 invalid parameters.
		var pub apierrors.PublicError
		if ok := errors.As(err, &pub); !ok {
			t.Fatalf("expected public (invalid-params) error, got: %v", err)
		}
	})

	t.Run("positional_params_form", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		params, _ := json.Marshal([]interface{}{fixture})
		result, err := api.NormalizePost(ctx, params)
		if err != nil {
			t.Fatalf("positional form: %v", err)
		}
		if result == nil {
			t.Fatal("expected result")
		}
	})
}

// TestNormalizePostParamBinding covers the parameter binding and input
// validation paths (no DB access needed).
func TestNormalizePostParamBinding(t *testing.T) {
	post := map[string]interface{}{"author": "steemit", "permlink": "firstpost"}

	if _, err := bindNormalizePostParams(mustJSON(t, map[string]interface{}{"post": post})); err != nil {
		t.Errorf("named form: %v", err)
	}
	if _, err := bindNormalizePostParams(mustJSON(t, []interface{}{post})); err != nil {
		t.Errorf("positional form: %v", err)
	}

	cases := []struct {
		name   string
		params string
	}{
		{"top-level object (old Go-only shape)", `{"author": "steemit"}`},
		{"missing post kwarg", `{}`},
		{"post not an object", `{"post": "firstpost"}`},
		{"two positional args", `[{"a": 1}, {"b": 2}]`},
		{"positional non-object", `["firstpost"]`},
		{"empty params", `[]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := bindNormalizePostParams(json.RawMessage(tc.params)); err == nil {
				t.Fatal("expected binding error")
			} else {
				var pub apierrors.PublicError
				if !errors.As(err, &pub) {
					t.Fatalf("expected public (invalid params) error, got %v", err)
				}
			}
		})
	}
}

// TestNormalizePostValidation covers validateSteemdPost's legacy exception
// classes: TypeErrors/Asserts -> invalid params (PublicError), KeyErrors ->
// server error class (plain error).
func TestNormalizePostValidation(t *testing.T) {
	base := func() map[string]interface{} {
		m := map[string]interface{}{}
		for _, k := range steemdPostRequiredFields {
			m[k] = "0.000 SBD"
		}
		m["author"] = "steemit"
		m["permlink"] = "firstpost"
		m["title"] = "t"
		m["body"] = "b"
		m["json_metadata"] = ""
		m["created"] = "2016-03-30T18:30:18"
		m["last_update"] = "2016-03-30T18:30:18"
		m["depth"] = 0
		m["children"] = 0
		m["net_rshares"] = 0
		m["active_votes"] = []interface{}{}
		m["author_reputation"] = "0"
		m["promoted"] = "0.000 STEEM"
		m["cashout_time"] = "2027-01-01T00:00:00"
		m["last_payout"] = "1969-12-31T23:59:59"
		m["max_accepted_payout"] = "1000000.000 SBD"
		m["percent_steem_dollars"] = 10000
		m["beneficiaries"] = []interface{}{}
		m["url"] = "/meta/@steemit/firstpost"
		m["id"] = 33
		m["parent_author"] = ""
		m["parent_permlink"] = "meta"
		m["root_author"] = "steemit"
		m["root_permlink"] = "firstpost"
		m["root_title"] = "Welcome to Steem!"
		m["allow_replies"] = true
		m["allow_votes"] = true
		m["allow_curation_rewards"] = true
		return m
	}

	if err := validateSteemdPost(base()); err != nil {
		t.Fatalf("valid post rejected: %v", err)
	}

	// Missing steemd field -> plain error (legacy KeyError -> -32000).
	p := base()
	delete(p, "cashout_time")
	err := validateSteemdPost(p)
	if err == nil {
		t.Fatal("expected missing-field error")
	}
	var pub apierrors.PublicError
	if errors.As(err, &pub) {
		t.Fatalf("missing field should be a server-class error, got public: %v", err)
	}

	// json_metadata non-string is tolerated (legacy post_basic wraps
	// json.loads in "except Exception: pass" and yields {}); a missing key
	// likewise. The sanitizer just sees an empty metadata map.
	p = base()
	p["json_metadata"] = map[string]interface{}{"tags": []interface{}{}}
	if err := validateSteemdPost(p); err != nil {
		t.Fatalf("json_metadata non-string should be tolerated, got %v", err)
	}
	p = base()
	delete(p, "json_metadata")
	if err := validateSteemdPost(p); err != nil {
		t.Fatalf("missing json_metadata should be tolerated, got %v", err)
	}

	// active_votes non-list -> invalid params (legacy TypeError).
	p = base()
	p["active_votes"] = "none"
	if err := validateSteemdPost(p); err == nil || !errors.As(err, &pub) {
		t.Fatalf("active_votes type: expected public error, got %v", err)
	}

	// Votes/rshares guard (utils/post.py:190 assert).
	p = base()
	p["net_rshares"] = "12345"
	p["active_votes"] = []interface{}{}
	if err := validateSteemdPost(p); err == nil || !errors.As(err, &pub) {
		t.Fatalf("rshares-without-votes: expected public error, got %v", err)
	}

	// Comments need the parent context keys.
	p = base()
	p["depth"] = 1
	delete(p, "root_title")
	if err := validateSteemdPost(p); err == nil {
		t.Fatal("expected comment context error")
	}

	// Wrong asset unit on promoted -> invalid params (legacy sbd_amount
	// assert), via the full handler path shape used by sbdAmountClassed.
	if _, err := sbdAmountClassed("5.000 STEEM"); err == nil || !errors.As(err, &pub) {
		t.Fatalf("unit assert: expected public error, got %v", err)
	}
	if _, err := sbdAmountClassed("garbage"); err == nil {
		t.Fatal("expected malformed amount error")
	}
}

// TestSbdMilli pins the exact-decimal payout helper.
func TestSbdMilli(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"0.000 SBD", 0, true},
		{"0.942 SBD", 942, true},
		{"942.560 SBD", 942560, true},
		{"12.345 SBD", 12345, true},
		{"1000000.000 SBD", 1000000000, true},
		{"-1.500 SBD", -1500, true},
		{"5 SBD", 5000, true},
		{"0.9420 SBD", 0, false}, // >3 decimals -> fallback path
		{"0.942 STEEM", 0, false},
		{"0.942", 0, false},
		{"abc SBD", 0, false},
	}
	for _, tc := range cases {
		got, ok := sbdMilli(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("sbdMilli(%q) = %d,%v want %d,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// --- helpers ---

// loadSteemdFixture reads the real steemd get_content sample.
func loadSteemdFixture(t *testing.T) map[string]interface{} {
	t.Helper()
	raw, err := os.ReadFile("testdata/steemit_firstpost_steemd.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var post map[string]interface{}
	if err := json.Unmarshal(raw, &post); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return post
}

func cloneFixture(t *testing.T) map[string]interface{} {
	t.Helper()
	raw, err := json.Marshal(loadSteemdFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedNormalizePost seeds the DB state for the normalize_post scenarios:
// one author account, core hive_posts rows (B/C/D/E + an orphan-community
// one), one community with the author holding a role_id 4, and a second
// community whose row does not exist.
func seedNormalizePost(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()

	acc := models.Account{Name: "steemit", CreatedAt: now, Reputation: 42.5}
	if err := gdb.Create(&acc).Error; err != nil {
		t.Fatalf("seed account: %v", err)
	}

	mkPost := func(id int64, permlink, category string, communityID int64, muted, valid, deleted bool) models.Post {
		p := models.Post{
			Author:    "steemit",
			Permlink:  permlink,
			Category:  category,
			CreatedAt: now,
			Depth:     0, // scenario D proves the INPUT depth wins
			IsDeleted: deleted,
			IsMuted:   muted,
			IsValid:   valid,
		}
		p.ID = id
		if communityID != 0 {
			p.CommunityID.Valid = true
			p.CommunityID.Int64 = communityID
		}
		return p
	}
	type postSeed struct {
		post      models.Post
		wantValid bool // seeding intent, immune to GORM's RETURNING backfill
	}
	seeds := []postSeed{
		{mkPost(777, "community-post", "hive-123456", 42, true, false, false), false}, // B: gray+hide source
		{mkPost(888, "paid-post", "meta", 0, true, false, false), false},              // C: dead flags (no community)
		{mkPost(999, "re-firstpost", "meta", 0, false, true, false), true},            // D
		{mkPost(555, "deleted-post", "meta-deleted", 0, false, true, true), true},     // E: deleted but core-usable
		{mkPost(776, "orphan-community-post", "hive-777777", 77, false, true, false), true},
	}
	for i := range seeds {
		if err := gdb.Create(&seeds[i].post).Error; err != nil {
			t.Fatalf("seed post %+v: %v", seeds[i].post, err)
		}
		if !seeds[i].wantValid {
			// GORM omits false bools on columns tagged default:true (and
			// backfills the struct via RETURNING), so is_valid=false seeds
			// need an explicit write driven by the seeding intent.
			if err := gdb.Model(&models.Post{}).
				Where("id = ?", seeds[i].post.ID).
				Update("is_valid", false).Error; err != nil {
				t.Fatalf("seed is_valid for %d: %v", seeds[i].post.ID, err)
			}
		}
	}

	comm := models.Community{ID: 42, Name: "hive-123456", Title: "My Community", CreatedAt: now}
	if err := gdb.Create(&comm).Error; err != nil {
		t.Fatalf("seed community: %v", err)
	}

	role := models.Role{CommunityID: 42, AccountID: acc.ID, RoleID: 4, Title: "moderator", CreatedAt: now}
	if err := gdb.Create(&role).Error; err != nil {
		t.Fatalf("seed role: %v", err)
	}
}

// assertKeySet pins the exact legacy response key set (bridge
// _condenser_post_object + normalize_post decoration).
func assertKeySet(t *testing.T, ret map[string]interface{}, extra []string) {
	t.Helper()
	want := map[string]bool{
		"post_id": true, "author": true, "permlink": true, "category": true,
		"title": true, "body": true, "json_metadata": true, "created": true,
		"updated": true, "depth": true, "children": true, "net_rshares": true,
		"is_paidout": true, "payout_at": true, "payout": true,
		"pending_payout_value": true, "author_payout_value": true,
		"curator_payout_value": true, "promoted": true, "replies": true,
		"active_votes": true, "author_reputation": true, "stats": true,
		"beneficiaries": true, "max_accepted_payout": true,
		"percent_steem_dollars": true, "url": true,
	}
	for _, k := range extra {
		want[k] = true
	}
	for k := range ret {
		if !want[k] {
			t.Errorf("unexpected key %q in response", k)
		}
	}
	for k := range want {
		if _, ok := ret[k]; !ok {
			t.Errorf("missing key %q in response", k)
		}
	}
}

func assertStatsShape(t *testing.T, ret map[string]interface{}) {
	t.Helper()
	stats, ok := ret["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("stats not a map: %T", ret["stats"])
	}
	want := map[string]bool{"hide": true, "gray": true, "total_votes": true, "flag_weight": true}
	if len(stats) != len(want) {
		t.Errorf("stats key set %v, want 4 keys", statsKeys(stats))
	}
	for k := range want {
		if _, ok := stats[k]; !ok {
			t.Errorf("stats missing key %q", k)
		}
	}
}

func statsKeys(stats map[string]interface{}) []string {
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	return keys
}

// assertVotes checks the active_votes round-trip against the fixture:
// 2-field vote objects in input order, rshares as strings, full 607 votes
// (no Mutes filtering on this endpoint), summing to the fixture total.
func assertVotes(t *testing.T, ret map[string]interface{}) {
	t.Helper()
	votes, ok := ret["active_votes"].([]interface{})
	if !ok {
		t.Fatalf("active_votes not a list: %T", ret["active_votes"])
	}
	if len(votes) != 607 {
		t.Fatalf("active_votes: expected 607 votes, got %d", len(votes))
	}
	first, ok := votes[0].(map[string]interface{})
	if !ok {
		t.Fatalf("vote[0] not a map: %T", votes[0])
	}
	if len(first) != 2 || first["voter"] != "dantheman" || first["rshares"] != "375241" {
		t.Errorf("vote[0] = %v, want {dantheman 375241} (2 fields)", first)
	}
	var sum float64
	for _, v := range votes {
		vm := v.(map[string]interface{})
		var rs float64
		if _, err := fmt.Sscanf(vm["rshares"].(string), "%g", &rs); err != nil {
			t.Fatalf("vote rshares %v: %v", vm["rshares"], err)
		}
		sum += rs
	}
	if sum != 830053779138 {
		t.Errorf("vote rshares sum = %v, want 830053779138", sum)
	}
}

func assertEq(t *testing.T, field string, got, want interface{}) {
	t.Helper()
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Errorf("%s = %s, want %s", field, gj, wj)
	}
}

func mustJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
