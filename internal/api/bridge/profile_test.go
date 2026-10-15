package bridge

import (
	"database/sql"
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

// TestGetProfileGolden verifies bridge.get_profile against the legacy shape
// (bridge_api/objects.py:203-227 _condenser_profile_object): every key, every
// value source (json_date dates, Mutes blacklists, stats.sp from
// vote_weight*steem_per_vest, metadata.profile from the six profile columns),
// plus the observer context contract (methods.py:53-55 ->
// hive_api/objects.py:48-67 _follow_contexts with include_mute=True).
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set.
func TestGetProfileGolden(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping get_profile test")
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

	seedProfileFixture(t, gdb)

	gin.SetMode(gin.TestMode)
	repo := db.NewRepository(gdb)
	api := NewProfileAPI(repo)

	callProfile := func(params map[string]interface{}) (interface{}, error) {
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		raw, _ := json.Marshal(params)
		return api.GetProfile(ctx, raw)
	}

	t.Run("golden shape without observer", func(t *testing.T) {
		res, err := callProfile(map[string]interface{}{"account": "alice"})
		if err != nil {
			t.Fatalf("GetProfile: %v", err)
		}
		prof, ok := res.(map[string]interface{})
		if !ok {
			t.Fatalf("expected map, got %T", res)
		}

		// Exact key set: 9 top-level keys, NO context without observer.
		wantKeys := []string{"id", "name", "created", "active", "post_count",
			"reputation", "blacklists", "stats", "metadata"}
		if len(prof) != len(wantKeys) {
			t.Fatalf("expected %d keys, got %d: %v", len(wantKeys), len(prof), keysOf(prof))
		}
		for _, k := range wantKeys {
			if _, ok := prof[k]; !ok {
				t.Errorf("missing key %q in %v", k, keysOf(prof))
			}
		}

		if prof["id"] != int64(101) || prof["name"] != "alice" {
			t.Errorf("id/name = %v/%v", prof["id"], prof["name"])
		}
		// json_date: 'T'-joined, no timezone, no microseconds.
		if prof["created"] != "2026-01-02T03:04:05" {
			t.Errorf("created = %v, want 2026-01-02T03:04:05", prof["created"])
		}
		if prof["active"] != "2026-02-03T04:05:06" {
			t.Errorf("active = %v, want 2026-02-03T04:05:06", prof["active"])
		}
		if prof["post_count"] != int64(7) {
			t.Errorf("post_count = %v, want 7", prof["post_count"])
		}
		if prof["reputation"] != 44.75 {
			t.Errorf("reputation = %v, want 44.75", prof["reputation"])
		}
		// rep >= 1 and not muted -> empty list (legacy Mutes.lists).
		if bl, ok := prof["blacklists"].([]string); !ok || len(bl) != 0 {
			t.Errorf("blacklists = %v (%T), want empty []string", prof["blacklists"], prof["blacklists"])
		}

		stats := prof["stats"].(map[string]interface{})
		// sp = int(vote_weight * steem_per_mvest/1e6) = int(2000000 * 480/1e6).
		if stats["sp"] != 960 {
			t.Errorf("stats.sp = %v, want 960", stats["sp"])
		}
		if stats["rank"] != int64(42) || stats["following"] != int64(3) || stats["followers"] != int64(5) {
			t.Errorf("stats = %v, want rank=42 following=3 followers=5", stats)
		}

		meta := prof["metadata"].(map[string]interface{})
		profile := meta["profile"].(map[string]interface{})
		if profile["name"] != "Alice A" || profile["about"] != "about me" ||
			profile["website"] != "https://example.com" || profile["location"] != "moon" ||
			profile["cover_image"] != "cover.png" || profile["profile_image"] != "avatar.png" {
			t.Errorf("metadata.profile = %v", profile)
		}

		// Nullable columns render as JSON null, not "".
		res2, err := callProfile(map[string]interface{}{"account": "nulls"})
		if err != nil {
			t.Fatalf("GetProfile nulls: %v", err)
		}
		prof2 := res2.(map[string]interface{})
		p2 := prof2["metadata"].(map[string]interface{})["profile"].(map[string]interface{})
		for _, k := range []string{"name", "about", "website", "location"} {
			if v, ok := p2[k]; !ok || v != nil {
				t.Errorf("nulls %s = %v, want JSON null", k, v)
			}
		}
	})

	t.Run("blacklists reputation tags and mutes", func(t *testing.T) {
		// rep 0 -> reputation-0 (legacy Mutes.lists int(rep) < 1).
		res, err := callProfile(map[string]interface{}{"account": "lowrep"})
		if err != nil {
			t.Fatalf("GetProfile lowrep: %v", err)
		}
		prof := res.(map[string]interface{})
		if bl, ok := prof["blacklists"].([]string); !ok || len(bl) != 1 || bl[0] != "reputation-0" {
			t.Errorf("lowrep blacklists = %v, want [reputation-0]", prof["blacklists"])
		}

		// muted author -> irredeemables tag prepended.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("badguy"))
		}))
		defer srv.Close()
		steem.SetSharedMutes(steem.NewMutes(srv.URL))
		t.Cleanup(func() { steem.SetSharedMutes(steem.NewMutes("")) })

		res, err = callProfile(map[string]interface{}{"account": "badguy"})
		if err != nil {
			t.Fatalf("GetProfile badguy: %v", err)
		}
		prof = res.(map[string]interface{})
		if bl, ok := prof["blacklists"].([]string); !ok || len(bl) != 1 || bl[0] != "irredeemables" {
			t.Errorf("badguy blacklists = %v, want [irredeemables]", prof["blacklists"])
		}
	})

	t.Run("observer context", func(t *testing.T) {
		cases := []struct {
			name     string
			account  string
			observer string
			want     map[string]interface{}
		}{
			// bob follows alice (state 1).
			{"follow state 1", "alice", "bob", map[string]interface{}{"followed": true}},
			// bob ignores carol (state 2).
			{"follow state 2 (ignore)", "carol", "bob", map[string]interface{}{"followed": false, "muted": true}},
			// bob follows+ignores dave (state 3).
			{"follow state 3 (both)", "dave", "bob", map[string]interface{}{"followed": true, "muted": true}},
			// bob has no relationship with erin -> {followed: false}.
			{"no follow row", "erin", "bob", map[string]interface{}{"followed": false}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				res, err := callProfile(map[string]interface{}{"account": tc.account, "observer": tc.observer})
				if err != nil {
					t.Fatalf("GetProfile: %v", err)
				}
				prof := res.(map[string]interface{})
				ctxObj, ok := prof["context"].(map[string]interface{})
				if !ok {
					t.Fatalf("missing context: %v", prof)
				}
				if len(ctxObj) != len(tc.want) {
					t.Fatalf("context = %v, want %v", ctxObj, tc.want)
				}
				for k, v := range tc.want {
					if ctxObj[k] != v {
						t.Errorf("context[%q] = %v, want %v", k, ctxObj[k], v)
					}
				}
			})
		}

		// Unknown observer is an error (legacy get_account_id asserts).
		if _, err := callProfile(map[string]interface{}{"account": "alice", "observer": "nobody"}); err == nil {
			t.Errorf("unknown observer: expected error, got nil")
		} else if err.Error() != "account not found: `nobody`" {
			t.Errorf("unknown observer error = %q, want account not found: `nobody`", err.Error())
		}
	})

	t.Run("account not found returns null", func(t *testing.T) {
		res, err := callProfile(map[string]interface{}{"account": "missing"})
		if err != nil {
			t.Fatalf("GetProfile missing: %v", err)
		}
		if res != nil {
			t.Errorf("expected nil result for unknown account, got %v", res)
		}
	})
}

// seedProfileFixture seeds one fully-populated account plus observers and
// follow rows covering the four hive_follows state outcomes.
func seedProfileFixture(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	active := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)

	// Account ids start at 101: the migrations seed accounts (miners, null,
	// temp, initminer) at ids 1-4.
	accounts := []models.Account{
		{ID: 101, Name: "alice", CreatedAt: created, ActiveAt: active,
			Reputation: 44.75, DisplayName: str("Alice A"), About: str("about me"),
			Location: str("moon"), Website: str("https://example.com"),
			ProfileImage: "avatar.png", CoverImage: "cover.png",
			Followers: 5, Following: 3, PostCount: 7, VoteWeight: 2000000, Rank: 42},
		{ID: 102, Name: "bob", CreatedAt: created, ActiveAt: active, Reputation: 25},
		{ID: 103, Name: "carol", CreatedAt: created, ActiveAt: active, Reputation: 25},
		{ID: 104, Name: "dave", CreatedAt: created, ActiveAt: active, Reputation: 25},
		{ID: 105, Name: "erin", CreatedAt: created, ActiveAt: active, Reputation: 25},
		{ID: 106, Name: "lowrep", CreatedAt: created, ActiveAt: active, Reputation: 0},
		{ID: 107, Name: "badguy", CreatedAt: created, ActiveAt: active, Reputation: 25},
		{ID: 108, Name: "nulls", CreatedAt: created, ActiveAt: active, Reputation: 25},
	}
	for i := range accounts {
		if err := gdb.Create(&accounts[i]).Error; err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}

	// GORM omits zero-valued fields on Create (column default 25 would win);
	// write the deliberate reputation-0 rows explicitly.
	if err := gdb.Model(&models.Account{}).Where("name = ?", "lowrep").
		Update("reputation", 0).Error; err != nil {
		t.Fatalf("set lowrep reputation: %v", err)
	}

	follows := []models.Follow{
		{FollowerID: 102, FollowingID: 101, State: models.FollowStateBlog, CreatedAt: created},
		{FollowerID: 102, FollowingID: 103, State: models.FollowStateIgnore, CreatedAt: created},
		{FollowerID: 102, FollowingID: 104, State: models.FollowStateBoth, CreatedAt: created},
	}
	for i := range follows {
		if err := gdb.Create(&follows[i]).Error; err != nil {
			t.Fatalf("seed follow: %v", err)
		}
	}

	// hive_state drives stats.sp (steem_per_mvest/1e6). The migrations seed a
	// row at block_num 0 — update it rather than adding a second row.
	if err := gdb.Exec(`UPDATE hive_state SET steem_per_mvest = 480.000`).Error; err != nil {
		t.Fatalf("seed hive_state: %v", err)
	}
}

func keysOf(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// str builds a valid sql.NullString for fixture seeding.
func str(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
