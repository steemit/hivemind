package hive

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

// TestNotifyMethods verifies the bridge.* notification methods against legacy
// hive_api/notify.py:
//
//  1. notifications referencing a deleted post are filtered from the list
//     methods (COALESCE(hp.is_deleted, False) = False) but still counted by
//     unread_notifications (whose legacy SQL has no such filter);
//  2. community accounts (hive-*) force min_score=0 and switch the query to
//     community_id;
//  3. vote payloads format as "$%.2f" in the message when >= $0.01 and stay
//     bare below it;
//  4. unknown accounts/posts are PublicErrors (legacy get_account_id /
//     get_post_id asserts);
//  5. min_score values beyond int16 no longer wrap (int32 conversion).
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set.
func TestNotifyMethods(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping notify integration test")
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

	seedNotifyFixture(t, gdb)

	gin.SetMode(gin.TestMode)
	repo := db.NewRepository(gdb)
	api := NewNotifyAPI(repo)

	// invoke calls a handler and returns (result, error) without failing the
	// test, so error-path subtests can inspect the error.
	invoke := func(handler func(*gin.Context, json.RawMessage) (interface{}, error), params map[string]interface{}) (interface{}, error) {
		w := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(w)
		ctx.Request = httptest.NewRequest("POST", "/", nil)
		raw, _ := json.Marshal(params)
		return handler(ctx, raw)
	}

	t.Run("deleted-post notifications filtered, postless pass through", func(t *testing.T) {
		// alice's notifications: vote on live post (n1), vote on deleted post
		// (n2, filtered), follow without post (n3, passes).
		res, err := invoke(api.AccountNotifications, map[string]interface{}{"account": "alice"})
		if err != nil {
			t.Fatalf("AccountNotifications: %v", err)
		}
		list, ok := res.([]interface{})
		if !ok {
			t.Fatalf("expected list, got %T", res)
		}
		if len(list) != 2 {
			t.Fatalf("expected 2 notifications (deleted-post one filtered), got %d: %v", len(list), list)
		}
		msgs := map[string]bool{}
		for _, item := range list {
			msgs[item.(map[string]interface{})["msg"].(string)] = true
		}
		if !msgs["@bob voted on your post ($0.50)"] || !msgs["@bob followed you"] {
			t.Errorf("msgs = %v", msgs)
		}

		// post_notifications for the deleted post: only its own (filtered)
		// rows — nothing references p2 except n2, so empty.
		res, err = invoke(api.PostNotifications, map[string]interface{}{"author": "alice", "permlink": "deleted-post"})
		if err != nil {
			t.Fatalf("PostNotifications deleted: %v", err)
		}
		if l := res.([]interface{}); len(l) != 0 {
			t.Errorf("deleted post notifications = %v, want empty", l)
		}

		// post_notifications for the live post returns n1 and n5 (both
		// reference post 1; the query filters by post_id, not dst).
		res, err = invoke(api.PostNotifications, map[string]interface{}{"author": "alice", "permlink": "live-post"})
		if err != nil {
			t.Fatalf("PostNotifications live: %v", err)
		}
		if l := res.([]interface{}); len(l) != 2 {
			t.Errorf("live post notifications = %v, want 2", l)
		}
	})

	t.Run("unread counts deleted-post notifications (no filter)", func(t *testing.T) {
		// alice lastread is before all seeds: n1+n2+n3 all score 25+, so 3.
		res, err := invoke(api.UnreadNotifications, map[string]interface{}{"account": "alice"})
		if err != nil {
			t.Fatalf("UnreadNotifications: %v", err)
		}
		out := res.(map[string]interface{})
		if out["unread"] != int64(3) {
			t.Errorf("unread = %v, want 3 (legacy unread SQL has no is_deleted filter)", out["unread"])
		}
		// lastread renders as str(datetime): space-separated, no timezone.
		if out["lastread"] != "2026-01-01 00:00:00" {
			t.Errorf("lastread = %v, want 2026-01-01 00:00:00", out["lastread"])
		}
	})

	t.Run("hive- community account forces min_score 0", func(t *testing.T) {
		// n4 targets community 999 with score 10 (below default 25): only
		// visible with the forced min_score=0.
		res, err := invoke(api.AccountNotifications, map[string]interface{}{"account": "hive-999888"})
		if err != nil {
			t.Fatalf("AccountNotifications community: %v", err)
		}
		list := res.([]interface{})
		if len(list) != 1 {
			t.Fatalf("expected 1 community notification (min_score forced to 0), got %d: %v", len(list), list)
		}
		item := list[0].(map[string]interface{})
		if item["msg"] != "@bob subscribed to Test Community" {
			t.Errorf("msg = %v", item["msg"])
		}
		if item["url"] != "trending/hive-999888" {
			t.Errorf("url = %v, want trending/hive-999888", item["url"])
		}

		// A regular account with the same low score sees nothing.
		res, err = invoke(api.AccountNotifications, map[string]interface{}{"account": "carol"})
		if err != nil {
			t.Fatalf("AccountNotifications carol: %v", err)
		}
		if l := res.([]interface{}); len(l) != 0 {
			t.Errorf("carol notifications = %v, want empty (score 10 < min_score 25)", l)
		}
	})

	t.Run("vote payload formatting", func(t *testing.T) {
		// n5: $0.005 vote — below the $0.01 threshold, message stays bare.
		res, err := invoke(api.AccountNotifications, map[string]interface{}{"account": "dave"})
		if err != nil {
			t.Fatalf("AccountNotifications dave: %v", err)
		}
		list := res.([]interface{})
		if len(list) != 1 {
			t.Fatalf("expected 1 notification, got %d: %v", len(list), list)
		}
		if msg := list[0].(map[string]interface{})["msg"]; msg != "@bob voted on your post" {
			t.Errorf("small vote msg = %q, want bare @bob voted on your post", msg)
		}
	})

	t.Run("unknown account and post are public errors", func(t *testing.T) {
		if _, err := invoke(api.AccountNotifications, map[string]interface{}{"account": "nobody"}); err == nil {
			t.Errorf("account_notifications unknown account: expected error")
		} else if err.Error() != "account not found: `nobody`" {
			t.Errorf("error = %q", err.Error())
		}
		if _, err := invoke(api.UnreadNotifications, map[string]interface{}{"account": "nobody"}); err == nil {
			t.Errorf("unread_notifications unknown account: expected error")
		}
		if _, err := invoke(api.PostNotifications, map[string]interface{}{"author": "alice", "permlink": "no-such-post"}); err == nil {
			t.Errorf("post_notifications unknown post: expected error")
		} else if err.Error() != "post id not found" {
			t.Errorf("error = %q", err.Error())
		}
	})

	t.Run("min_score beyond int16 does not wrap", func(t *testing.T) {
		// With the int16 conversion, 40000 wrapped negative and every score
		// passed the filter; int32 keeps the comparison honest -> no rows.
		res, err := invoke(api.AccountNotifications, map[string]interface{}{"account": "alice", "min_score": 40000})
		if err != nil {
			t.Fatalf("AccountNotifications big min_score: %v", err)
		}
		if l := res.([]interface{}); len(l) != 0 {
			t.Errorf("min_score 40000 returned %d rows, want 0 (no int16 wrap)", len(l))
		}
	})
}

// seedNotifyFixture seeds two accounts (alice/dave as notification targets,
// bob as source, carol as a low-score target), one community account + row,
// a live and a deleted post, and notifications covering each fix.
func seedNotifyFixture(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// Account ids start at 101: the migrations seed accounts at ids 1-4.
	accounts := []models.Account{
		{ID: 101, Name: "alice", CreatedAt: now, ActiveAt: now, Reputation: 25,
			LastreadAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{ID: 102, Name: "bob", CreatedAt: now, ActiveAt: now, Reputation: 25},
		{ID: 103, Name: "carol", CreatedAt: now, ActiveAt: now, Reputation: 25},
		{ID: 104, Name: "dave", CreatedAt: now, ActiveAt: now, Reputation: 25},
		{ID: 999, Name: "hive-999888", CreatedAt: now, ActiveAt: now, Reputation: 25},
	}
	for i := range accounts {
		if err := gdb.Create(&accounts[i]).Error; err != nil {
			t.Fatalf("seed account: %v", err)
		}
	}

	if err := gdb.Create(&models.Community{
		ID: 999, Name: "hive-999888", Title: "Test Community", CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed community: %v", err)
	}

	posts := []models.Post{
		{ID: 1, Author: "alice", Permlink: "live-post", Category: "life", CreatedAt: now, Depth: 0},
		{ID: 2, Author: "alice", Permlink: "deleted-post", Category: "life", CreatedAt: now, Depth: 0, IsDeleted: true},
	}
	for i := range posts {
		if err := gdb.Create(&posts[i]).Error; err != nil {
			t.Fatalf("seed post: %v", err)
		}
	}

	mkNotif := func(id int64, typ int16, score int16, srcID, dstID, communityID, postID int64, payload string, createdAt time.Time) models.Notification {
		n := models.Notification{
			ID: id, Type: typ, Score: score, CreatedAt: createdAt,
		}
		if srcID != 0 {
			n.SrcID.Valid, n.SrcID.Int64 = true, srcID
		}
		if dstID != 0 {
			n.DstID.Valid, n.DstID.Int64 = true, dstID
		}
		if communityID != 0 {
			n.CommunityID.Valid, n.CommunityID.Int64 = true, communityID
		}
		if postID != 0 {
			n.PostID.Valid, n.PostID.Int64 = true, postID
		}
		if payload != "" {
			n.Payload.Valid, n.Payload.String = true, payload
		}
		return n
	}
	notifs := []models.Notification{
		// n1: vote $0.50 on the live post (>= $0.01 -> formatted).
		mkNotif(1, models.NotifyTypeVote, 25, 102, 101, 0, 1, "$0.50", now.Add(1*time.Minute)),
		// n2: vote on the deleted post -> filtered from lists, counted unread.
		mkNotif(2, models.NotifyTypeVote, 25, 102, 101, 0, 2, "$0.50", now.Add(2*time.Minute)),
		// n3: follow, no post reference.
		mkNotif(3, models.NotifyTypeFollow, 25, 102, 101, 0, 0, "", now.Add(3*time.Minute)),
		// n4: community notification with score 10 (< default min_score 25).
		mkNotif(4, models.NotifyTypeSubscribe, 10, 102, 0, 999, 0, "", now.Add(4*time.Minute)),
		// n5: vote $0.005 (< $0.01 -> bare message).
		mkNotif(5, models.NotifyTypeVote, 25, 102, 104, 0, 1, "$0.005", now.Add(5*time.Minute)),
		// n6: low-score personal notification for carol.
		mkNotif(6, models.NotifyTypeFollow, 10, 102, 103, 0, 0, "", now.Add(6*time.Minute)),
	}
	for i := range notifs {
		if err := gdb.Create(&notifs[i]).Error; err != nil {
			t.Fatalf("seed notif: %v", err)
		}
	}
}
