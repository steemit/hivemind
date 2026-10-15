package indexer

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/config"
)

// This file pins the PR#407 (planned) scope: payment TXIndex + promoted
// cache wiring (§3.7③④), mention mute direction (§3.8), the payout sweep
// wiring (§3.9), reblog impersonation + notification (§3.10①), audit-path
// undelete community/feed parity (§3.10②), and the block-rollback orphan
// fix for CachedPost.Delete/Undelete (§3.10③).

// --- §3.7③ + §3.7④: golden end-to-end on a REAL chain block ---

// goldenPromoBlock is mainnet block 110420250 exactly as served by
// api.steemit.com (2026-10-14T02:03:54), carrying three transactions:
//
//	tx 0: damurq votes enrisanti/gato-relajado-al-frente-de-una-tienda
//	tx 1: steemburnup publishes a burn-pool root post (+ comment_options)
//	tx 2: remlaps2 burns 0.001 SBD to null with the real promotion memo
//	      "@eglis/the-diary-game-or-11-10-2026-or-domingo-tranquilo-en-casa"
//
// The account_history entry for the transfer reports trx_in_block=2, which
// is the value hive_payments.tx_idx must carry. Deviations from the raw
// chain block: `previous` points at the genesis row so the hive_blocks
// foreign key resolves in an empty test database, and the operation type
// strings appear in the "_operation"-suffixed form the steem client's
// opTypeToString normalization produces before blocks reach the processor
// (chain: "vote"/"comment"/"comment_options"/"transfer").
func goldenPromoBlock() map[string]interface{} {
	return map[string]interface{}{
		"block_num": float64(110420250),
		"block_id":  "0694e11ae8a3a37e347e0e3d1106730ea63e0edd",
		// Real previous (0694e119...) replaced for the hive_blocks.prev FK.
		"previous":  genesisHash,
		"timestamp": "2026-10-14T02:03:54",
		"transaction_ids": []interface{}{
			"16c56675d49a2f6980d3c899f2c2ce548568ddeb",
			"4a9c5e73f9e4e6eca9697f731d249db6fdc17bbe",
			"f5234ddfb8652b46b59c5a2ffd8e66bc0942b80a",
		},
		"transactions": []interface{}{
			map[string]interface{}{"operations": []interface{}{
				[]interface{}{"vote_operation", map[string]interface{}{
					"author": "enrisanti", "permlink": "gato-relajado-al-frente-de-una-tienda",
					"voter": "damurq", "weight": 101,
				}},
			}},
			map[string]interface{}{"operations": []interface{}{
				[]interface{}{"comment_operation", map[string]interface{}{
					"author":          "steemburnup",
					"permlink":        "burn-pool-1791943429623",
					"parent_author":   "",
					"parent_permlink": "burnpost",
					"title":           "Burn Pool Contribution - October 14, 2026",
					"body":            "This post is part of an automated burn pool initiative to reduce STEEM supply.",
					"json_metadata":   `{"tags":["burnpost","steem","burn","null","steemit"],"app":"steem-burn-bot/1.0"}`,
				}},
				[]interface{}{"comment_options_operation", map[string]interface{}{
					"author": "steemburnup", "permlink": "burn-pool-1791943429623",
					"max_accepted_payout":   "1000000.000 SBD",
					"percent_steem_dollars": 10000,
					"allow_votes":           true, "allow_curation_rewards": true,
					"extensions": []interface{}{},
				}},
			}},
			map[string]interface{}{"operations": []interface{}{
				[]interface{}{"transfer_operation", map[string]interface{}{
					"from": "remlaps2", "to": "null",
					"amount": "0.001 SBD",
					"memo":   "@eglis/the-diary-game-or-11-10-2026-or-domingo-tranquilo-en-casa",
				}},
			}},
		},
	}
}

// goldenPromoAccounts seeds the accounts the real block touches.
func goldenPromoAccounts(t *testing.T, database *db.DB) {
	t.Helper()
	seedAccounts(t, database, "remlaps2", "eglis", "enrisanti", "damurq", "steemburnup")
}

// goldenPromoPosts seeds the posts the block references (the promotion
// goldenPromoPosts seeds the posts the block references (the promotion
// target and the voted post; steemburnup's post is created by the block's
// own comment op) plus their cache rows -- in production a promotion targets
// a post that was cached at publication (comment op insert level); without
// the row the upvote-level UPDATE would be a no-op.
func goldenPromoPosts(t *testing.T, database *db.DB) {
	t.Helper()
	for _, p := range []struct{ author, permlink, category string }{
		{"eglis", "the-diary-game-or-11-10-2026-or-domingo-tranquilo-en-casa", "hive-166863"},
		{"enrisanti", "gato-relajado-al-frente-de-una-tienda", "arte"},
	} {
		post := &models.Post{
			Author: p.author, Permlink: p.permlink, Category: p.category,
			CreatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			Depth:     0, IsValid: true,
		}
		if err := database.DB.Create(post).Error; err != nil {
			t.Fatalf("seed post %s/%s: %v", p.author, p.permlink, err)
		}
		for _, table := range []string{"hive_posts_cache", "hive_posts_cache_temp"} {
			if err := database.DB.Exec(
				fmt.Sprintf(`INSERT INTO %s (post_id, author, permlink, category) VALUES (?, ?, ?, ?)`, table),
				post.ID, p.author, p.permlink, p.category).Error; err != nil {
				t.Fatalf("seed %s row: %v", table, err)
			}
		}
	}
}

// goldenContent serves get_content for every post the golden block dirties.
func goldenContent() map[string]map[string]interface{} {
	m := map[string]map[string]interface{}{}
	for _, ap := range [][2]string{
		{"eglis", "the-diary-game-or-11-10-2026-or-domingo-tranquilo-en-casa"},
		{"enrisanti", "gato-relajado-al-frente-de-una-tienda"},
		{"steemburnup", "burn-pool-1791943429623"},
	} {
		m[ap[0]+"/"+ap[1]] = steemdPostFor(ap[0], ap[1], "real block 110420250 content")
	}
	return m
}

// TestPaymentGoldenRealBlockPromotedWiring runs the real mainnet block
// through the block processor in live mode and verifies:
//
//   - hive_payments.tx_idx carries the transfer's transaction index (2 --
//     the account_history trx_in_block for this op), not a placeholder 0;
//   - hive_posts.promoted is bumped inside the block transaction;
//   - the promotion is wired into the post cache (§3.7④): after the flush,
//     hive_posts_cache AND hive_posts_cache_temp both carry promoted=0.001
//     for the memo target (before this PR the payment never reached the
//     cache and promoted sorting stayed dead);
//   - in initial-sync mode the same block records the payment but queues
//     nothing on the cache (legacy gates on !is_initial_sync);
//   - a real URL-shaped memo (multi-slash, mainnet block 20234409) records
//     no payment at all.
func TestPaymentGoldenRealBlockPromotedWiring(t *testing.T) {
	t.Run("live mode records tx_idx and wires promoted into the cache", func(t *testing.T) {
		database, repo, cleanup := withMigratedDB(t)
		defer cleanup()
		ctx := context.Background()

		goldenPromoAccounts(t, database)
		goldenPromoPosts(t, database)

		content := goldenContent()
		provider := &fakeSteemProvider{}
		provider.getContentBatch = func(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
			out := make([]map[string]interface{}, 0, len(posts))
			for _, ap := range posts {
				c, ok := content[ap[0]+"/"+ap[1]]
				if !ok {
					return nil, fmt.Errorf("golden test: unexpected get_content %s/%s", ap[0], ap[1])
				}
				out = append(out, c)
			}
			return out, nil
		}
		bp := NewBlockProcessor(database, repo, provider)

		if err := bp.ProcessBlock(ctx, goldenPromoBlock(), false); err != nil {
			t.Fatalf("ProcessBlock: %v", err)
		}

		// tx_idx: the transfer sits at transaction index 2 of 3.
		var payment models.Payment
		if err := database.DB.WithContext(ctx).
			Where("block_num = ?", 110420250).First(&payment).Error; err != nil {
			t.Fatalf("payment row missing: %v", err)
		}
		if payment.TXIndex != 2 {
			t.Errorf("payment.tx_idx = %d, want 2 (transaction position within the block)", payment.TXIndex)
		}
		if payment.Amount != 0.001 || payment.Token != "SBD" {
			t.Errorf("payment amount/token = %v/%s, want 0.001/SBD", payment.Amount, payment.Token)
		}
		if payment.Memo != "@eglis/the-diary-game-or-11-10-2026-or-domingo-tranquilo-en-casa" {
			t.Errorf("payment.memo = %q", payment.Memo)
		}

		// hive_posts.promoted bumped by the block transaction.
		postRepo := db.NewPostRepository(repo)
		target, err := postRepo.GetByAuthorPermlink(ctx, "eglis", "the-diary-game-or-11-10-2026-or-domingo-tranquilo-en-casa")
		if err != nil || target == nil {
			t.Fatalf("promotion target missing: %v %v", target, err)
		}
		if target.Promoted != 0.001 {
			t.Errorf("hive_posts.promoted = %v, want 0.001", target.Promoted)
		}

		// §3.7④: the promotion is queued on the shared cache (upvote level
		// via CachedPost.Vote with no voter => no vote notif).
		if bp.CachedPost().PendingQueueLen() == 0 {
			t.Fatal("promotion did not queue the post on the cache dirty queue")
		}
		if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeVote); n != 0 {
			t.Errorf("vote notifs = %d, want 0 (payment-driven Vote carries no voter)", n)
		}

		// Flush (as Sync does after each batch): the staged promoted amount
		// must land in BOTH cache tables.
		if _, err := bp.CachedPost().Flush(ctx, false); err != nil {
			t.Fatalf("flush: %v", err)
		}
		for _, table := range []string{"hive_posts_cache", "hive_posts_cache_temp"} {
			var promoted float64
			if err := database.DB.WithContext(ctx).
				Table(table).Select("promoted").
				Where("post_id = ?", target.ID).Scan(&promoted).Error; err != nil {
				t.Fatalf("read %s.promoted: %v", table, err)
			}
			if promoted != 0.001 {
				t.Errorf("%s.promoted = %v, want 0.001 (promoted cache wiring)", table, promoted)
			}
		}
	})

	t.Run("initial sync records the payment but queues nothing", func(t *testing.T) {
		database, repo, cleanup := withMigratedDB(t)
		defer cleanup()
		ctx := context.Background()

		goldenPromoAccounts(t, database)
		goldenPromoPosts(t, database)
		bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

		if err := bp.ProcessBlock(ctx, goldenPromoBlock(), true); err != nil {
			t.Fatalf("ProcessBlock (initial): %v", err)
		}
		if n := countWhere(t, database, "hive_payments", "block_num = ?", 110420250); n != 1 {
			t.Fatalf("payment rows = %d, want 1 (payment itself is recorded in initial sync)", n)
		}
		if q := bp.CachedPost().PendingQueueLen(); q != 0 {
			t.Errorf("cache queue after initial-sync block = %d, want 0 (legacy !is_initial_sync gate)", q)
		}
	})

	t.Run("real URL-shaped memo records no payment", func(t *testing.T) {
		database, repo, cleanup := withMigratedDB(t)
		defer cleanup()
		ctx := context.Background()

		seedAccounts(t, database, "saifulsabri")
		post := &models.Post{Author: "saifulsabri", Permlink: "promo-steem-activity-wherever-and-whenever-fa0355ff11bda",
			Category: "promo-steem", CreatedAt: time.Unix(1458845200, 0).UTC(), Depth: 0, IsValid: true}
		if err := database.DB.Create(post).Error; err != nil {
			t.Fatalf("seed post: %v", err)
		}
		bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

		// Real transfer from mainnet block 20234409: a full URL as memo
		// (multiple slashes) -- legacy _validate_url rejects it.
		block := robustnessBlock(20234409, "2018-02-09T00:00:00", []string{"trx-1"},
			[]interface{}{
				[]interface{}{"transfer_operation", map[string]interface{}{
					"from": "saifulsabri", "to": "null",
					"amount": "0.001 SBD",
					"memo":   "https://steemit.com/promo-steem/@saifulsabri/promo-steem-activity-wherever-and-whenever-fa0355ff11bda",
				}},
			},
		)
		if err := bp.ProcessBlock(ctx, block, false); err != nil {
			t.Fatalf("ProcessBlock: %v", err)
		}
		if n := countWhere(t, database, "hive_payments", "1=1"); n != 0 {
			t.Errorf("payment rows = %d, want 0 (URL memo skipped)", n)
		}
		if q := bp.CachedPost().PendingQueueLen(); q != 0 {
			t.Errorf("cache queue = %d, want 0", q)
		}
	})
}

// --- §3.8: mention mute direction ---

// TestPostNotifs_MentionMuteDirection pins the P1-8 fix: a mention is
// suppressed when the MENTIONED account ignores the AUTHOR (legacy
// _muted(mention_id, author_id) -- the recipient's mute governs), NOT when
// the author ignores the mentioned account (the pre-fix inverted query).
func TestPostNotifs_MentionMuteDirection(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	for _, name := range []string{"alice", "bob", "carol"} {
		tx.Create(&models.Account{Name: name, CreatedAt: time.Unix(1458845200, 0), VoteWeight: 1000})
	}
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Depth: 0, IsValid: true}
	database.DB.WithContext(ctx).Create(post)

	accountRepo := db.NewAccountRepository(repo)
	alice, _ := accountRepo.GetByName(ctx, "alice")
	bob, _ := accountRepo.GetByName(ctx, "bob")
	carol, _ := accountRepo.GetByName(ctx, "carol")

	// bob ignores alice (recipient mutes author): mention of bob suppressed.
	database.DB.WithContext(ctx).Create(&models.Follow{
		FollowerID: bob.ID, FollowingID: alice.ID, State: models.FollowStateIgnore,
		CreatedAt: time.Unix(1458845200, 0).UTC(),
	})
	// alice ignores carol (author mutes recipient): mention of carol SURVIVES.
	database.DB.WithContext(ctx).Create(&models.Follow{
		FollowerID: alice.ID, FollowingID: carol.ID, State: models.FollowStateBoth,
		CreatedAt: time.Unix(1458845200, 0).UTC(),
	})

	provider := &fakeSteemProvider{}
	cp := NewCachedPost(database.DB, provider)
	pn := NewPostNotifs(database.DB, cp, NewNotifyIndexer(repo))
	pn.Install()

	steemd := steemdPostFor("alice", "p1", "hello @bob and @carol")
	pn.Hook(ctx, steemd, post.ID, "insert", 0)

	var dsts []int64
	database.DB.WithContext(ctx).
		Table("hive_notifs").
		Select("dst_id").
		Where("type_id = ?", models.NotifyTypeMention).
		Scan(&dsts)
	if len(dsts) != 1 || dsts[0] != carol.ID {
		t.Errorf("mention notif dsts = %v, want exactly [carol(%d)]: bob (who ignores the author) must be suppressed, carol (whom the author ignores) must NOT be", dsts, carol.ID)
	}
}

// --- §3.9: payout sweep wiring ---

// TestPayoutSweepMarksFlushesAndDirtiesAuthors drives Sync.payoutSweep --
// the legacy run-loop step (sync.py:98/208) this PR wires into
// syncIrreversible -- on a database with one stale payout: the post is
// marked, flushed through the cache (is_paidout flips), and the author is
// marked dirty (legacy Accounts.dirty_set inside dirty_paidouts).
func TestPayoutSweepMarksFlushesAndDirtiesAuthors(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice")
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true}
	if err := database.DB.Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}
	// Cache row whose payout passed without the is_paidout flag.
	if err := database.DB.WithContext(ctx).Exec(
		`INSERT INTO hive_posts_cache (post_id, author, permlink, category, payout_at, is_paidout)
		 VALUES (?, 'alice', 'p1', 'life', '2026-01-09T00:00:00', '0')`, post.ID).Error; err != nil {
		t.Fatalf("seed cache row: %v", err)
	}
	// Head block supplies the sweep date (legacy Blocks.head_date()).
	head := &models.Block{Num: 1000, Hash: "ab" + fmt.Sprintf("%038d", 0),
		CreatedAt: time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC)}
	if err := database.DB.Create(head).Error; err != nil {
		t.Fatalf("seed head block: %v", err)
	}

	// Paid-out content: cashout_time 1969-… marks the post paid out.
	content := steemdPostFor("alice", "p1", "paid out")
	content["cashout_time"] = "1969-12-31T23:59:59"
	content["last_payout"] = "2026-01-09T00:00:00"
	provider := &fakeSteemProvider{}
	provider.getContentBatch = func(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
		return []map[string]interface{}{content}, nil
	}

	cfg := &config.Config{Indexer: config.IndexerConfig{MaxBatch: 50, SyncInterval: 1}}
	s, err := NewSync(cfg, database, provider)
	if err != nil {
		t.Fatalf("NewSync: %v", err)
	}

	if err := s.payoutSweep(ctx); err != nil {
		t.Fatalf("payoutSweep: %v", err)
	}

	if q := s.blockProcessor.CachedPost().PendingQueueLen(); q != 0 {
		t.Errorf("cache queue after sweep = %d, want 0 (sweep flushes)", q)
	}
	var paidout bool
	database.DB.WithContext(ctx).
		Table("hive_posts_cache").Select("is_paidout").
		Where("post_id = ?", post.ID).Scan(&paidout)
	if !paidout {
		t.Error("is_paidout not flipped by the sweep flush")
	}
	if n := s.blockProcessor.Accounts().PendingDirtyLen(); n != 1 {
		t.Errorf("dirty accounts after sweep = %d, want 1 (legacy dirty_set on payout authors)", n)
	}
}

// --- §3.10①: reblog impersonation + notification ---

// reblogJSON builds a follow-plugin reblog payload.
func reblogJSON(blogger, author, permlink string) string {
	return fmt.Sprintf(`["reblog",{"account":%q,"author":%q,"permlink":%q}]`, blogger, author, permlink)
}

// TestReblogImpersonationAndNotify pins the two legacy behaviors this PR
// restores: the impersonation check (payload.account must equal the signing
// account, custom_op.py:117) and the reblog notification to the post author
// (custom_op.py:146-148, !is_initial_sync gate).
func TestReblogImpersonationAndNotify(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob")
	post := &models.Post{Author: "alice", Permlink: "root", Category: "life",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true}
	if err := database.DB.Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}
	accountRepo := db.NewAccountRepository(repo)
	alice, _ := accountRepo.GetByName(ctx, "alice")
	bob, _ := accountRepo.GetByName(ctx, "bob")

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	const blockNum = int64(30000000)
	const ts = "2019-01-01T00:00:00"

	// 1. Impersonation: payload names mallory but bob signed.
	impersonation := robustnessBlock(blockNum, ts, []string{"t1"},
		[]interface{}{
			[]interface{}{"custom_json_operation", customJSONOpWithID("follow", "bob", reblogJSON("mallory", "alice", "root"))},
		},
	)
	if err := bp.ProcessBlock(ctx, impersonation, false); err != nil {
		t.Fatalf("ProcessBlock (impersonation): %v (must skip the op, not fail the block)", err)
	}
	if n := countWhere(t, database, "hive_reblogs", "1=1"); n != 0 {
		t.Fatalf("reblog rows after impersonation = %d, want 0", n)
	}
	if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeReblog); n != 0 {
		t.Errorf("reblog notifs after impersonation = %d, want 0", n)
	}

	// 2. Legitimate reblog: row + feed entry + notification to the author.
	legit := robustnessBlock(blockNum+1, ts, []string{"t2"},
		[]interface{}{
			[]interface{}{"custom_json_operation", customJSONOpWithID("follow", "bob", reblogJSON("bob", "alice", "root"))},
		},
	)
	if err := bp.ProcessBlock(ctx, legit, false); err != nil {
		t.Fatalf("ProcessBlock (legit): %v", err)
	}
	if n := countWhere(t, database, "hive_reblogs", "account = ? AND post_id = ?", "bob", post.ID); n != 1 {
		t.Fatalf("reblog rows = %d, want 1", n)
	}
	if n := countWhere(t, database, "hive_feed_cache", "post_id = ? AND account_id = ?", post.ID, bob.ID); n != 1 {
		t.Errorf("feed rows = %d, want 1 (blogger's feed entry)", n)
	}
	type notifRow struct {
		SrcID int64
		DstID int64
		Score int16
	}
	var notifs []notifRow
	database.DB.WithContext(ctx).
		Table("hive_notifs").
		Select("src_id", "dst_id", "score").
		Where("type_id = ?", models.NotifyTypeReblog).
		Scan(&notifs)
	if len(notifs) != 1 {
		t.Fatalf("reblog notifs = %d, want 1", len(notifs))
	}
	if notifs[0].SrcID != bob.ID || notifs[0].DstID != alice.ID {
		t.Errorf("reblog notif src/dst = %d/%d, want bob(%d)/alice(%d)",
			notifs[0].SrcID, notifs[0].DstID, bob.ID, alice.ID)
	}
	// Seeded accounts carry no vote_weight: the rank lookup ranks bob 1st
	// of the two accounts -> bucket "top 0.02%" -> score 70.
	if notifs[0].Score != 70 {
		t.Errorf("reblog notif score = %d, want 70 (default_score rank bucket)", notifs[0].Score)
	}

	// 3. Duplicate reblog: no second row, but legacy emits another notif
	// (no dedup on this path) and the feed insert stays idempotent.
	dup := robustnessBlock(blockNum+2, ts, []string{"t3"},
		[]interface{}{
			[]interface{}{"custom_json_operation", customJSONOpWithID("follow", "bob", reblogJSON("bob", "alice", "root"))},
		},
	)
	if err := bp.ProcessBlock(ctx, dup, false); err != nil {
		t.Fatalf("ProcessBlock (dup): %v", err)
	}
	if n := countWhere(t, database, "hive_reblogs", "account = ? AND post_id = ?", "bob", post.ID); n != 1 {
		t.Errorf("reblog rows after duplicate = %d, want 1 (ON CONFLICT DO NOTHING)", n)
	}
	if n := countWhere(t, database, "hive_feed_cache", "post_id = ? AND account_id = ?", post.ID, bob.ID); n != 1 {
		t.Errorf("feed rows after duplicate = %d, want 1 (idempotent insert)", n)
	}
	if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeReblog); n != 2 {
		t.Errorf("reblog notifs after duplicate = %d, want 2 (legacy notifies on every reblog op, no dedup)", n)
	}
}

// TestReblogInitialSyncGatesFeedAndNotif: during initial sync a reblog
// writes the row but no feed entry and no notification.
func TestReblogInitialSyncGatesFeedAndNotif(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob")
	post := &models.Post{Author: "alice", Permlink: "root", Category: "life",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true}
	if err := database.DB.Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}
	accountRepo := db.NewAccountRepository(repo)
	bob, _ := accountRepo.GetByName(ctx, "bob")

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	block := robustnessBlock(30000000, "2019-01-01T00:00:00", []string{"t1"},
		[]interface{}{
			[]interface{}{"custom_json_operation", customJSONOpWithID("follow", "bob", reblogJSON("bob", "alice", "root"))},
		},
	)
	if err := bp.ProcessBlock(ctx, block, true); err != nil {
		t.Fatalf("ProcessBlock: %v", err)
	}
	if n := countWhere(t, database, "hive_reblogs", "1=1"); n != 1 {
		t.Errorf("reblog rows = %d, want 1 (the row itself is written in initial sync)", n)
	}
	if n := countWhere(t, database, "hive_feed_cache", "post_id = ? AND account_id = ?", post.ID, bob.ID); n != 0 {
		t.Errorf("feed rows = %d, want 0 (!is_initial_sync gate)", n)
	}
	if n := countWhere(t, database, "hive_notifs", "type_id = ?", models.NotifyTypeReblog); n != 0 {
		t.Errorf("reblog notifs = %d, want 0 (!is_initial_sync gate)", n)
	}
}

// --- §3.10③: block-rollback orphans for CachedPost.Delete/Undelete ---

// orphanCommentOp builds a comment whose parent does not exist: processing
// it fails the block with an infrastructure-class error (P1-7), which
// rolls the whole block back -- a deterministic way to abort a block AFTER
// earlier ops in it have run.
func orphanCommentOp() map[string]interface{} {
	return map[string]interface{}{
		"parent_author": "ghost", "parent_permlink": "missing",
		"author": "bob", "permlink": "orphan",
		"title": "t", "body": "b", "json_metadata": `{}`,
	}
}

// TestBlockRollbackLeavesNoCacheDeleteOrphans: a delete_comment whose
// block later fails must not leave the cache eviction behind. Legacy runs
// CachedPost.delete inside the process_multi transaction; before this PR
// the Go eviction wrote through the pool and survived the rollback, so a
// retried block deleted from a cache the successful retry would then
// re-populate only via later flushes (#404 audit registration).
func TestBlockRollbackLeavesNoCacheDeleteOrphans(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob")
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "life",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true}
	if err := database.DB.Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}
	if err := database.DB.WithContext(ctx).Exec(
		`INSERT INTO hive_posts_cache (post_id, author, permlink, category)
		 VALUES (?, 'alice', 'p1', 'life')`, post.ID).Error; err != nil {
		t.Fatalf("seed cache row: %v", err)
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	// Failing block: delete followed by an orphan comment (infra failure).
	failing := robustnessBlock(30000000, "2019-01-01T00:00:00", []string{"t1"},
		[]interface{}{
			[]interface{}{"delete_comment_operation", map[string]interface{}{"author": "alice", "permlink": "p1"}},
			[]interface{}{"comment_operation", orphanCommentOp()},
		},
	)
	if err := bp.ProcessBlock(ctx, failing, false); err == nil {
		t.Fatal("block with orphan comment: expected failure, got nil")
	}
	// The eviction must have rolled back with the block.
	var n int64
	database.DB.WithContext(ctx).Table("hive_posts_cache").
		Where("post_id = ?", post.ID).Count(&n)
	if n != 1 {
		t.Fatalf("cache rows after rolled-back delete = %d, want 1 (eviction must not survive the rollback)", n)
	}
	var deleted bool
	database.DB.WithContext(ctx).Table("hive_posts").
		Select("is_deleted").Where("id = ?", post.ID).Scan(&deleted)
	if deleted {
		t.Error("hive_posts.is_deleted survived the rollback")
	}

	// The same block without the poison op commits and evicts.
	good := robustnessBlock(30000000, "2019-01-01T00:00:00", []string{"t2"},
		[]interface{}{
			[]interface{}{"delete_comment_operation", map[string]interface{}{"author": "alice", "permlink": "p1"}},
		},
	)
	if err := bp.ProcessBlock(ctx, good, false); err != nil {
		t.Fatalf("ProcessBlock (good): %v", err)
	}
	database.DB.WithContext(ctx).Table("hive_posts_cache").
		Where("post_id = ?", post.ID).Count(&n)
	if n != 0 {
		t.Errorf("cache rows after committed delete = %d, want 0", n)
	}
}

// TestBlockRollbackLeavesNoUndeletePlaceholderOrphans: the placeholder rows
// CachedPost.Undelete force-creates must roll back with a failed block
// (they ride the block transaction now, as legacy's do inside process_multi).
func TestBlockRollbackLeavesNoUndeletePlaceholderOrphans(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice", "bob")
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "old-cat",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true, IsDeleted: true}
	if err := database.DB.Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}
	// Cache spans a HIGHER post id so the undelete takes the
	// force-create-placeholder branch.
	span := &models.Post{Author: "bob", Permlink: "p2", Category: "x",
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true}
	if err := database.DB.Create(span).Error; err != nil {
		t.Fatalf("seed span: %v", err)
	}
	if err := database.DB.WithContext(ctx).Exec(
		`INSERT INTO hive_posts_cache (post_id, author, permlink, category)
		 VALUES (?, 'bob', 'p2', 'x')`, span.ID).Error; err != nil {
		t.Fatalf("seed span cache: %v", err)
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})

	undeleteOp := func() []interface{} {
		return []interface{}{
			[]interface{}{"comment_operation", map[string]interface{}{
				"parent_author": "", "parent_permlink": "new-cat",
				"author": "alice", "permlink": "p1",
				"title": "t", "body": "b", "json_metadata": `{}`,
			}},
		}
	}

	// Failing block: undelete followed by an orphan comment (same
	// transaction: processBlockInTx walks ops in order).
	failingOps := append(undeleteOp(), []interface{}{"comment_operation", orphanCommentOp()})
	failing := robustnessBlock(30000000, "2019-01-01T00:00:00", []string{"t1"}, failingOps)
	if err := bp.ProcessBlock(ctx, failing, false); err == nil {
		t.Fatal("block with orphan comment: expected failure, got nil")
	}
	var n int64
	database.DB.WithContext(ctx).Table("hive_posts_cache").
		Where("post_id = ?", post.ID).Count(&n)
	if n != 0 {
		t.Fatalf("placeholder rows after rolled-back undelete = %d, want 0 (must not survive the rollback)", n)
	}
	var stillDeleted bool
	database.DB.WithContext(ctx).Table("hive_posts").
		Select("is_deleted").Where("id = ?", post.ID).Scan(&stillDeleted)
	if !stillDeleted {
		t.Error("undelete survived the rollback (is_deleted flipped early)")
	}

	// Good block: placeholder lands and the update is queued.
	good := robustnessBlock(30000000, "2019-01-01T00:00:00", []string{"t2"}, undeleteOp())
	if err := bp.ProcessBlock(ctx, good, false); err != nil {
		t.Fatalf("ProcessBlock (good): %v", err)
	}
	database.DB.WithContext(ctx).Table("hive_posts_cache").
		Where("post_id = ? AND category = ?", post.ID, "new-cat").Count(&n)
	if n != 1 {
		t.Errorf("placeholder rows after committed undelete = %d, want 1 with recomputed category", n)
	}
}

// --- §3.10②: audit-path undelete parity ---

// TestAuditCacheUndeleteCommunityAndFeed drives AuditCacheUndelete over a
// deleted root post whose on-chain category names a community: the restored
// row must carry the resolved community_id (block-path buildPost parity,
// PR#406) and re-enter the feed cache (legacy _insert_feed_cache).
func TestAuditCacheUndeleteCommunityAndFeed(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedAccounts(t, database, "alice")
	// hive_communities.id is not auto-increment (it is the community's
	// account id on chain); seed one explicitly.
	const communityID = int64(777)
	community := &models.Community{ID: communityID, Name: "hive-174017", Title: "test community",
		CreatedAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
	if err := database.DB.Create(community).Error; err != nil {
		t.Fatalf("seed community: %v", err)
	}
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "stale",
		CreatedAt: time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC), Depth: 0, IsValid: true, IsDeleted: true}
	if err := database.DB.Create(post).Error; err != nil {
		t.Fatalf("seed post: %v", err)
	}

	// get_content serves the post as a root published into the community
	// (parent_permlink == community name; created after START_DATE).
	content := steemdPostFor("alice", "p1", "restored body")
	content["parent_author"] = ""
	content["parent_permlink"] = "hive-174017"
	content["created"] = "2020-06-01T00:00:00"
	provider := &fakeSteemProvider{}
	provider.getContentBatch = func(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
		return []map[string]interface{}{content}, nil
	}

	cp := NewCachedPost(database.DB, provider)
	if err := AuditCacheUndelete(ctx, database.DB, cp, provider); err != nil {
		t.Fatalf("AuditCacheUndelete: %v", err)
	}

	var restored models.Post
	if err := database.DB.WithContext(ctx).
		Where("id = ?", post.ID).First(&restored).Error; err != nil {
		t.Fatalf("load restored post: %v", err)
	}
	if restored.IsDeleted {
		t.Error("post still deleted after audit undelete")
	}
	if !restored.CommunityID.Valid || restored.CommunityID.Int64 != communityID {
		t.Errorf("community_id = %v, want community %d (block-path buildPost parity)", restored.CommunityID, communityID)
	}
	if restored.Category != "hive-174017" {
		t.Errorf("category = %q, want recomputed from parent_permlink", restored.Category)
	}
	if !restored.CreatedAt.Equal(time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("created_at = %v, want the original row value (audit undelete does not touch created_at)", restored.CreatedAt)
	}

	// Root post re-enters the feed cache.
	var n int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").
		Where("post_id = ?", post.ID).Count(&n)
	if n != 1 {
		t.Errorf("feed rows after audit undelete = %d, want 1 (feed backfill for root posts)", n)
	}
	// The post re-entered the cache pipeline: the audit's trailing flush
	// wrote the cache row from the served get_content payload.
	database.DB.WithContext(ctx).Table("hive_posts_cache").
		Where("post_id = ?", post.ID).Count(&n)
	if n != 1 {
		t.Errorf("cache rows after audit undelete = %d, want 1 (flushed through the pipeline)", n)
	}
}
