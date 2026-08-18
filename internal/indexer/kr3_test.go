package indexer

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/logging"
)

// testLogger returns a component-tagged logger for indexer tests.
func testLogger() *zap.Logger {
	return logging.GetLogger().With(zap.String("component", "indexer-test"))
}

// steemdAccount builds a get_accounts-style account map.
func steemdAccount(name string) map[string]interface{} {
	prof := `{"profile":{"version":2,"name":"Display Name","about":"bio","location":"earth","website":"example.com","profile_image":"https://img.example/p.png","cover_image":"https://img.example/c.png"}}`
	return map[string]interface{}{
		"name":                     name,
		"created":                  "2016-03-24T16:05:00",
		"last_account_update":      "2026-01-01T00:00:00",
		"last_post":                "2026-01-02T00:00:00",
		"last_root_post":           "2026-01-01T12:00:00",
		"last_vote_time":           "2026-01-03T00:00:00",
		"proxy":                    "",
		"post_count":               42,
		"reputation":               "1000000000000", // UI 52
		"vesting_shares":           "1000.000000 VESTS",
		"received_vesting_shares":  "200.000000 VESTS",
		"delegated_vesting_shares": "50.000000 VESTS",
		"proxied_vsf_votes":        []interface{}{"1000000", 0, 0, 0},
		"posting_json_metadata":    prof,
		"json_metadata":            "",
		"transfer_history":         []interface{}{},
		"market_history":           []interface{}{},
	}
}

// TestAccountFlush verifies the real Flush: steemd fetch → ~17-column update.
func TestAccountFlush(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()

	provider := &fakeSteemProvider{getAccounts: func(_ context.Context, names []string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(names))
		for i, n := range names {
			out[i] = steemdAccount(n)
		}
		return out, nil
	}}
	ai := NewAccountIndexer(repo, testLogger(), provider)

	ai.MarkDirty("alice")
	if ai.PendingDirtyLen() != 1 {
		t.Fatalf("dirty len = %d", ai.PendingDirtyLen())
	}

	n, err := ai.FlushBatch(ctx, true)
	if err != nil {
		t.Fatalf("FlushBatch: %v", err)
	}
	if n != 1 {
		t.Fatalf("processed = %d", n)
	}
	if ai.PendingDirtyLen() != 0 {
		t.Errorf("queue not drained")
	}

	var acct models.Account
	if err := database.DB.WithContext(ctx).
		Where("name = ?", "alice").First(&acct).Error; err != nil {
		t.Fatalf("account: %v", err)
	}
	// vote_weight = 1000 + 200 - 50 = 1150
	if acct.VoteWeight != 1150 {
		t.Errorf("vote_weight = %v, want 1150", acct.VoteWeight)
	}
	// proxy empty → proxy_weight = vests + proxied/1e6 = 1000 + 1 = 1001
	if acct.ProxyWeight != 1001 {
		t.Errorf("proxy_weight = %v, want 1001", acct.ProxyWeight)
	}
	if acct.Reputation != 52 {
		t.Errorf("reputation = %v, want 52 (rep_log10)", acct.Reputation)
	}
	if acct.PostCount != 42 {
		t.Errorf("post_count = %d", acct.PostCount)
	}
	// active_at = max of the five timestamps = last_vote_time 2026-01-03.
	want := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	if !acct.ActiveAt.Equal(want) {
		t.Errorf("active_at = %v, want %v", acct.ActiveAt, want)
	}
	// Profile sanitized from posting_json_metadata v2.
	if dn, _ := acct.DisplayName.Value(); dn != "Display Name" {
		t.Errorf("display_name = %v", acct.DisplayName)
	}
	if !acct.ActiveAt.Equal(want) {
		t.Errorf("active_at mismatch")
	}
	// website without proto → http:// prefix.
	if w, _ := acct.Website.Value(); w != "http://example.com" {
		t.Errorf("website = %v", acct.Website)
	}
	// raw_json must not contain the stripped keys.
	if rj, _ := acct.RawJSON.Value(); rj == nil {
		t.Error("raw_json empty")
	}
}

// TestFollowDeltaFlush verifies count deltas batch into followers/following.
func TestFollowDeltaFlush(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "a", CreatedAt: time.Unix(1458845200, 0)})
	tx.Create(&models.Account{Name: "b", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()

	accountRepo := db.NewAccountRepository(repo)
	a, _ := accountRepo.GetByName(ctx, "a")
	b, _ := accountRepo.GetByName(ctx, "b")

	fi := NewFollowIndexer(repo, testLogger())

	// a follows b twice, unfollows once → net +1 for a.following/b.followers.
	fi.follow(a.ID, b.ID)
	fi.follow(a.ID, b.ID)
	fi.unfollow(a.ID, b.ID)
	// b follows a → +1 both directions.
	fi.follow(b.ID, a.ID)

	if err := fi.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	var aa, bb models.Account
	database.DB.WithContext(ctx).Where("id = ?", a.ID).First(&aa)
	database.DB.WithContext(ctx).Where("id = ?", b.ID).First(&bb)
	if aa.Following != 1 || aa.Followers != 1 {
		t.Errorf("a: following=%d followers=%d, want 1/1", aa.Following, aa.Followers)
	}
	if bb.Following != 1 || bb.Followers != 1 {
		t.Errorf("b: following=%d followers=%d, want 1/1", bb.Following, bb.Followers)
	}

	// Delta maps drained.
	if fi.PendingDeltasLen() != 0 {
		t.Errorf("deltas not drained: %d", fi.PendingDeltasLen())
	}
}

// TestFollowForceRecount recomputes counts from hive_follows rows.
func TestFollowForceRecount(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "a", CreatedAt: time.Unix(1458845200, 0)})
	tx.Create(&models.Account{Name: "b", CreatedAt: time.Unix(1458845200, 0)})
	tx.Create(&models.Account{Name: "c", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()

	accountRepo := db.NewAccountRepository(repo)
	a, _ := accountRepo.GetByName(ctx, "a")
	b, _ := accountRepo.GetByName(ctx, "b")
	c, _ := accountRepo.GetByName(ctx, "c")

	// Seed follows: a→b, a→c (state 1), b→a (state 3 = blog+ignore).
	now := time.Now().UTC()
	tx2 := database.DB.WithContext(ctx).Begin()
	tx2.Create(&models.Follow{FollowerID: a.ID, FollowingID: b.ID, State: 1, CreatedAt: now})
	tx2.Create(&models.Follow{FollowerID: a.ID, FollowingID: c.ID, State: 1, CreatedAt: now})
	tx2.Create(&models.Follow{FollowerID: b.ID, FollowingID: a.ID, State: 3, CreatedAt: now})
	tx2.Commit()

	fi := NewFollowIndexer(repo, testLogger())
	if err := fi.ForceRecount(ctx); err != nil {
		t.Fatalf("ForceRecount: %v", err)
	}

	var aa, bb models.Account
	database.DB.WithContext(ctx).Where("id = ?", a.ID).First(&aa)
	database.DB.WithContext(ctx).Where("id = ?", b.ID).First(&bb)
	// a: follows b+c (following=2); followed by b(state 3 counts) → followers=1.
	if aa.Following != 2 || aa.Followers != 1 {
		t.Errorf("a: following=%d followers=%d, want 2/1", aa.Following, aa.Followers)
	}
	if bb.Following != 1 || bb.Followers != 1 {
		t.Errorf("b: following=%d followers=%d, want 1/1", bb.Following, bb.Followers)
	}
}

// TestProcessDelete_FeedEviction: deleting a root post removes ALL feed
// entries (author + reblogs), not just the author's.
func TestProcessDelete_FeedEviction(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "author", CreatedAt: time.Unix(1458845200, 0)})
	tx.Create(&models.Account{Name: "reblogger", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()
	accountRepo := db.NewAccountRepository(repo)
	author, _ := accountRepo.GetByName(ctx, "author")
	reblogger, _ := accountRepo.GetByName(ctx, "reblogger")

	post := &models.Post{Author: "author", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Depth: 0, IsValid: true}
	database.DB.WithContext(ctx).Create(post)

	// Author's own feed entry + a reblogger's entry.
	tx2 := database.DB.WithContext(ctx).Begin()
	tx2.Create(&models.FeedCache{PostID: post.ID, AccountID: author.ID, CreatedAt: time.Now().UTC()})
	tx2.Create(&models.FeedCache{PostID: post.ID, AccountID: reblogger.ID, CreatedAt: time.Now().UTC()})
	tx2.Commit()

	pi := NewPostIndexer(repo, NewCachedPost(database.DB, &fakeSteemProvider{}), testLogger())
	txDel := database.DB.WithContext(ctx).Begin()
	if err := pi.ProcessDelete(ctx, txDel, map[string]interface{}{
		"author": "author", "permlink": "p1",
	}); err != nil {
		t.Fatalf("ProcessDelete: %v", err)
	}
	txDel.Commit()

	var n int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").
		Where("post_id = ?", post.ID).Count(&n)
	if n != 0 {
		t.Errorf("feed entries after delete = %d, want 0 (author + reblogs)", n)
	}
}
