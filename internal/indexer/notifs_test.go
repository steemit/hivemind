package indexer

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// notifsPost builds a steemd comment post (depth 1) mentioning @bob.
func notifsComment() map[string]interface{} {
	p := steemdPostFor("alice", "c1", "hello @bob check this")
	p["depth"] = 1
	p["parent_author"] = "rooter"
	p["parent_permlink"] = "root-post"
	return p
}

func TestPostNotifs_ReplyMention(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	// Accounts: alice (author), rooter (parent), bob (mentioned).
	tx := database.DB.WithContext(ctx).Begin()
	for _, name := range []string{"alice", "rooter", "bob"} {
		tx.Create(&models.Account{Name: name, CreatedAt: time.Unix(1458845200, 0), VoteWeight: 1000})
	}
	tx.Commit()

	// Parent post in hive_posts (rooter/root-post).
	root := &models.Post{Author: "rooter", Permlink: "root-post", Category: "life",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true}
	database.DB.WithContext(ctx).Create(root)
	comment := &models.Post{Author: "alice", Permlink: "c1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Depth: 1,
		ParentID: sql.NullInt64{Int64: root.ID, Valid: true}, IsValid: true}
	database.DB.WithContext(ctx).Create(comment)

	accountRepo := db.NewAccountRepository(repo)
	alice, _ := accountRepo.GetByName(ctx, "alice")
	rooter, _ := accountRepo.GetByName(ctx, "rooter")
	bob, _ := accountRepo.GetByName(ctx, "bob")

	provider := &fakeSteemProvider{}
	cp := NewCachedPost(database.DB, provider)
	pn := NewPostNotifs(database.DB, cp, NewNotifyIndexer(repo))
	pn.Install()

	post := notifsComment()
	pn.Hook(ctx, post, comment.ID, "insert", 3.0)

	// Two notifs expected: reply → rooter, mention → bob.
	type notifRow struct {
		TypeID int16
		SrcID  int64
		DstID  int64
		PostID int64
		Score  int16
	}
	var notifs []notifRow
	database.DB.WithContext(ctx).
		Table("hive_notifs").
		Select("type_id", "src_id", "dst_id", "post_id", "score").
		Order("type_id").
		Scan(&notifs)

	if len(notifs) != 2 {
		t.Fatalf("expected 2 notifs, got %+v", notifs)
	}
	// type 12 = reply (depth 1)
	if notifs[0].TypeID != models.NotifyTypeReply || notifs[0].DstID != rooter.ID || notifs[0].SrcID != alice.ID {
		t.Errorf("reply notif = %+v", notifs[0])
	}
	// type 16 = mention
	if notifs[1].TypeID != models.NotifyTypeMention || notifs[1].DstID != bob.ID {
		t.Errorf("mention notif = %+v", notifs[1])
	}

	// Dedup: running the hook again must not duplicate the mention.
	pn.Hook(ctx, post, comment.ID, "update", 3.0)
	var n int64
	database.DB.WithContext(ctx).Table("hive_notifs").
		Where("type_id = ?", models.NotifyTypeMention).Count(&n)
	if n != 1 {
		t.Errorf("mention notif duplicated: %d", n)
	}
}

func TestPostNotifs_Vote(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	tx.Create(&models.Account{Name: "whale", CreatedAt: time.Unix(1458845200, 0)})
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Depth: 0, IsValid: true}
	database.DB.WithContext(ctx).Create(post)

	accountRepo := db.NewAccountRepository(repo)
	alice, _ := accountRepo.GetByName(ctx, "alice")
	whale, _ := accountRepo.GetByName(ctx, "whale")

	provider := &fakeSteemProvider{}
	cp := NewCachedPost(database.DB, provider)
	pn := NewPostNotifs(database.DB, cp, NewNotifyIndexer(repo))
	pn.Install()

	// Queue a vote from whale, then flush a post with a big vote.
	cp.Vote("alice", "p1", post.ID, "whale")

	steemd := steemdPostFor("alice", "p1", "body")
	steemd["net_rshares"] = "100000000000" // 1e11
	steemd["active_votes"] = []interface{}{
		map[string]interface{}{
			"voter": "whale", "rshares": "100000000000", // 1e11 ≥ 10e9
			"percent": "10000", "reputation": "0",
			"time": "2026-01-02T04:00:00",
		},
	}
	// payout 3.0, net 1e11 → ratio 3e-11 (inexact in binary); legacy Python
	// computes int(1000 * 3e-11 * 1e11) = 2999 (verified against Python),
	// so contrib truncates to 2999 → $2.999, score (4-1)*25 = 75.
	pn.Hook(ctx, steemd, post.ID, "upvote", 3.0)

	type notifRow struct {
		TypeID  int16
		SrcID   int64
		DstID   int64
		Payload string
		Score   int16
	}
	var notifs []notifRow
	database.DB.WithContext(ctx).
		Table("hive_notifs").
		Select("type_id", "src_id", "dst_id", "payload", "score").
		Where("type_id = ?", models.NotifyTypeVote).
		Scan(&notifs)

	if len(notifs) != 1 {
		t.Fatalf("expected 1 vote notif, got %+v", notifs)
	}
	n := notifs[0]
	if n.SrcID != whale.ID || n.DstID != alice.ID {
		t.Errorf("vote notif ids = %+v", n)
	}
	if n.Payload != "$2.999" {
		t.Errorf("payload = %q, want $2.999 (matches legacy float math)", n.Payload)
	}
	// contrib=2999 → 4 digits → (4-1)*25 = 75.
	if n.Score != 75 {
		t.Errorf("score = %d, want 75", n.Score)
	}

	// Small votes (< 10e9 rshares) produce no notifs even when queued.
	cp.Vote("alice", "p1", post.ID, "minnow")
	steemd["active_votes"] = []interface{}{
		map[string]interface{}{
			"voter": "minnow", "rshares": "1000000", // tiny
			"percent": "100", "reputation": "0", "time": "2026-01-02T04:00:00",
		},
	}
	pn.Hook(ctx, steemd, post.ID, "upvote", 3.0)

	var count int64
	database.DB.WithContext(ctx).Table("hive_notifs").
		Where("type_id = ?", models.NotifyTypeVote).Count(&count)
	if count != 1 {
		t.Errorf("small vote should not notify, count = %d", count)
	}
}

func TestPostNotifs_MentionSpamCap(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	// Six mentioned accounts; author default score 20 (no vote_weight) →
	// cap 5 → ALL mentions skipped.
	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0)})
	for _, n := range []string{"u1", "u2", "u3", "u4", "u5", "u6"} {
		tx.Create(&models.Account{Name: n, CreatedAt: time.Unix(1458845200, 0)})
	}
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Depth: 0, IsValid: true}
	database.DB.WithContext(ctx).Create(post)

	provider := &fakeSteemProvider{}
	cp := NewCachedPost(database.DB, provider)
	pn := NewPostNotifs(database.DB, cp, NewNotifyIndexer(repo))
	pn.Install()

	steemd := steemdPostFor("alice", "p1", "hi @u1 @u2 @u3 @u4 @u5 @u6")
	pn.Hook(ctx, steemd, post.ID, "insert", 0)

	var n int64
	database.DB.WithContext(ctx).Table("hive_notifs").
		Where("type_id = ?", models.NotifyTypeMention).Count(&n)
	if n != 0 {
		t.Errorf("6 mentions over cap 5 should all be skipped, got %d", n)
	}
}
