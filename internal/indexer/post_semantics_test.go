package indexer

import (
	"context"
	"testing"
	"time"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/internal/steem"
)

// Post-semantics integration tests (planned PR#406 / 02-indexer.md §三 3.2-3.6):
// category source, community_id resolution, children recount chaining,
// undelete rebuild, and orphan-comment fail-loud. All run against a real
// (migrated) Postgres via withMigratedDB.

// preCommunityDate is before the communities launch (2019-10-22).
var preCommunityDate = time.Date(2016, 3, 24, 16, 5, 0, 0, time.UTC)

// postCommunityDate is after the launch.
var postCommunityDate = time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)

// seedSemanticsAccount inserts an account row and fails the test on error.
func seedSemanticsAccount(t *testing.T, database *db.DB, name string) {
	t.Helper()
	tx := database.DB.WithContext(context.Background()).Begin()
	if err := tx.Create(&models.Account{Name: name, CreatedAt: preCommunityDate}).Error; err != nil {
		t.Fatalf("seed account %s: %v", name, err)
	}
	tx.Commit()
}

// newSemanticsPostIndexer builds a PostIndexer wired to a community indexer.
func newSemanticsPostIndexer(database *db.DB, repo *db.Repository, provider steem.Provider) *PostIndexer {
	return NewPostIndexer(repo, NewCachedPost(database.DB, provider), NewCommunityIndexer(repo, nil, testLogger()), testLogger())
}

// commentOp builds a chain-shaped comment operation map.
func semanticsCommentOp(author, permlink, parentAuthor, parentPermlink, jsonMeta string) map[string]interface{} {
	return map[string]interface{}{
		"parent_author":   parentAuthor,
		"parent_permlink": parentPermlink,
		"author":          author,
		"permlink":        permlink,
		"title":           "title",
		"body":            "body",
		"json_metadata":   jsonMeta,
	}
}

// 3.2 (P1-4): the root post category is ALWAYS the on-chain parent_permlink,
// never the first json_metadata tag. The op shapes below are real chain
// samples fetched from api.steemit.com (2026-10-15), chosen for maximal
// discrimination: the first json tag differs from parent_permlink, so a
// json-metadata-derived category would produce a different value.
func TestRootCategoryFromParentPermlink(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	// tinker-bell/drugwars-game-diary-20261015-kew239 (block chain data):
	// parent_permlink=hive-160196, json tags=[zzan,kr,krsuccess,...].
	seedSemanticsAccount(t, database, "tinker-bell")
	seedSemanticsAccount(t, database, "dapawat")
	pi := newSemanticsPostIndexer(database, repo, &fakeSteemProvider{})

	tx := database.DB.WithContext(ctx).Begin()
	op := semanticsCommentOp("tinker-bell", "drugwars-game-diary-20261015-kew239", "",
		"hive-160196",
		`{"tags":["zzan","kr","krsuccess","drugwars","hpl","club5050","game"],"app":"SteemX","community":"hive-160196"}`)
	if err := pi.ProcessComment(ctx, tx, op, postCommunityDate, true); err != nil {
		t.Fatalf("ProcessComment root: %v", err)
	}
	tx.Commit()

	post, err := db.NewPostRepository(repo).GetByAuthorPermlink(ctx, "tinker-bell", "drugwars-game-diary-20261015-kew239")
	if err != nil || post == nil {
		t.Fatalf("post lookup: %v %v", post, err)
	}
	if post.Category != "hive-160196" {
		t.Errorf("category = %q, want parent_permlink %q (not the first json tag)", post.Category, "hive-160196")
	}
	if post.Depth != 0 || post.ParentID.Valid {
		t.Errorf("root post depth=%d parent=%v, want 0/invalid", post.Depth, post.ParentID)
	}

	// dapawat/actifit-...: parent_permlink=hive-193552, first tag=actifit.
	tx = database.DB.WithContext(ctx).Begin()
	op = semanticsCommentOp("dapawat", "actifit-dapawat-20261015t012101995z", "",
		"hive-193552", `{"tags":["actifit"],"app":"actifit/0.9.4"}`)
	if err := pi.ProcessComment(ctx, tx, op, postCommunityDate, true); err != nil {
		t.Fatalf("ProcessComment root 2: %v", err)
	}
	tx.Commit()
	post2, _ := db.NewPostRepository(repo).GetByAuthorPermlink(ctx, "dapawat", "actifit-dapawat-20261015t012101995z")
	if post2 == nil || post2.Category != "hive-193552" {
		t.Errorf("category = %v, want hive-193552", post2)
	}

	// A comment inherits the parent's category verbatim.
	tx = database.DB.WithContext(ctx).Begin()
	op = semanticsCommentOp("tinker-bell", "re-drugwars", "tinker-bell", "drugwars-game-diary-20261015-kew239", `{"tags":["zzan"]}`)
	if err := pi.ProcessComment(ctx, tx, op, postCommunityDate, true); err != nil {
		t.Fatalf("ProcessComment reply: %v", err)
	}
	tx.Commit()
	reply, _ := db.NewPostRepository(repo).GetByAuthorPermlink(ctx, "tinker-bell", "re-drugwars")
	if reply == nil || reply.Category != "hive-160196" || reply.Depth != 1 {
		t.Errorf("reply category=%v depth=%v, want hive-160196/1", reply, reply)
	}
}

// 3.3 (P1-17): community_id resolution for top-level posts, mirroring
// legacy _build_post + Community.validated_id: only past the communities
// launch date, and only when the category names a registered community.
func TestCommunityIDResolution(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedSemanticsAccount(t, database, "blogger")
	seedSemanticsAccount(t, database, "hive-123456")
	tx := database.DB.WithContext(ctx).Begin()
	if err := tx.Create(&models.Community{ID: 100, Name: "hive-123456", TypeID: 1, CreatedAt: postCommunityDate}).Error; err != nil {
		t.Fatalf("seed community: %v", err)
	}
	tx.Commit()

	pi := newSemanticsPostIndexer(database, repo, &fakeSteemProvider{})

	process := func(permlink, category string, date time.Time) *models.Post {
		t.Helper()
		tx := database.DB.WithContext(ctx).Begin()
		op := semanticsCommentOp("blogger", permlink, "", category, `{"tags":["x"]}`)
		if err := pi.ProcessComment(ctx, tx, op, date, true); err != nil {
			t.Fatalf("ProcessComment %s: %v", permlink, err)
		}
		tx.Commit()
		post, _ := db.NewPostRepository(repo).GetByAuthorPermlink(ctx, "blogger", permlink)
		if post == nil {
			t.Fatalf("post %s missing", permlink)
		}
		return post
	}

	// Past START_DATE with a registered community category: id assigned.
	p := process("in-community", "hive-123456", postCommunityDate)
	if !p.CommunityID.Valid || p.CommunityID.Int64 != 100 {
		t.Errorf("community_id = %v, want 100", p.CommunityID)
	}

	// Before START_DATE: never a community (launch gate).
	p = process("pre-launch", "hive-123456", preCommunityDate)
	if p.CommunityID.Valid {
		t.Errorf("pre-launch community_id = %v, want NULL", p.CommunityID)
	}

	// Non-community category: NULL.
	p = process("plain-tag", "life", postCommunityDate)
	if p.CommunityID.Valid {
		t.Errorf("plain-tag community_id = %v, want NULL", p.CommunityID)
	}

	// Community-shaped name that was never registered: NULL.
	p = process("ghost-community", "hive-654321", postCommunityDate)
	if p.CommunityID.Valid {
		t.Errorf("ghost community_id = %v, want NULL", p.CommunityID)
	}

	// Malformed community name (type digit out of range): NULL.
	p = process("bad-community", "hive-456789", postCommunityDate)
	if p.CommunityID.Valid {
		t.Errorf("hive-4xxx community_id = %v, want NULL", p.CommunityID)
	}

	// A comment on a community post inherits community_id.
	tx = database.DB.WithContext(ctx).Begin()
	op := semanticsCommentOp("blogger", "re-in-community", "blogger", "in-community", `{"tags":["x"]}`)
	if err := pi.ProcessComment(ctx, tx, op, postCommunityDate, true); err != nil {
		t.Fatalf("ProcessComment reply: %v", err)
	}
	tx.Commit()
	reply, _ := db.NewPostRepository(repo).GetByAuthorPermlink(ctx, "blogger", "re-in-community")
	if reply == nil || !reply.CommunityID.Valid || reply.CommunityID.Int64 != 100 {
		t.Errorf("reply community_id = %v, want 100", reply)
	}
}

// 3.6 (P1-7): an orphan comment (parent_author/permlink pointing at a
// nonexistent post) must fail loud -- never downgrade to a root post. Legacy
// crashes in _build_post (tuple-unpack of the NULL parent row); Go fails the
// op as an infrastructure-class error so the whole block rolls back and the
// sync retry/poison machinery takes over.
func TestOrphanCommentFailsLoud(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedSemanticsAccount(t, database, "alice")
	pi := newSemanticsPostIndexer(database, repo, &fakeSteemProvider{})

	tx := database.DB.WithContext(ctx).Begin()
	op := semanticsCommentOp("alice", "reply", "ghost", "missing-post", `{}`)
	err := pi.ProcessComment(ctx, tx, op, postCommunityDate, true)
	if err == nil {
		tx.Rollback()
		t.Fatal("orphan comment: expected error, got nil")
	}
	if !IsInfraError(err) {
		tx.Rollback()
		t.Fatalf("orphan comment error must be infra-class (block retry), got data-class: %v", err)
	}
	tx.Rollback()

	// No downgraded root row may survive.
	post, _ := db.NewPostRepository(repo).GetByAuthorPermlink(ctx, "alice", "reply")
	if post != nil {
		t.Fatalf("orphan comment downgraded to root post: %+v", post)
	}

	// Block-level: the whole block fails (and rolls back). The migrated
	// schema ships a genesis hive_blocks row, so compare against the
	// pre-block count.
	var blocksBefore int64
	database.DB.WithContext(ctx).Table("hive_blocks").Count(&blocksBefore)
	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	block := map[string]interface{}{
		"block_num":       float64(1),
		"block_id":        "0000000100000000000000000000000000000000",
		"previous":        "0000000000000000000000000000000000000000",
		"timestamp":       "2020-01-02T03:04:05",
		"transaction_ids": []interface{}{"aabbcc"},
		"transactions": []interface{}{
			map[string]interface{}{
				"operations": []interface{}{
					[]interface{}{"comment_operation", semanticsCommentOp("alice", "reply", "ghost", "missing-post", `{}`)},
				},
			},
		},
	}
	if err := bp.ProcessBlock(ctx, block, false); err == nil {
		t.Fatal("block with orphan comment: expected failure, got nil")
	}
	var n int64
	database.DB.WithContext(ctx).Table("hive_posts").Count(&n)
	if n != 0 {
		t.Errorf("hive_posts rows after rolled-back block = %d, want 0", n)
	}
	database.DB.WithContext(ctx).Table("hive_blocks").Count(&n)
	if n != blocksBefore {
		t.Errorf("hive_blocks rows after rolled-back block = %d, want pre-block %d", n, blocksBefore)
	}
}

// 3.5 (P1-6): undelete fully rebuilds the row from the new op (legacy
// Posts.undelete + _build_post): is_valid/is_muted/is_pinned reset, parent/
// category/community_id/depth recomputed, created_at untouched, and the feed
// cache entry restored for root posts.
func TestUndeleteFullRebuild(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedSemanticsAccount(t, database, "alice")
	seedSemanticsAccount(t, database, "bob")
	pi := newSemanticsPostIndexer(database, repo, &fakeSteemProvider{})

	process := func(op map[string]interface{}, date time.Time) error {
		tx := database.DB.WithContext(ctx).Begin()
		err := pi.ProcessComment(ctx, tx, op, date, false)
		if err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit().Error
	}
	del := func(author, permlink string) error {
		tx := database.DB.WithContext(ctx).Begin()
		err := pi.ProcessDelete(ctx, tx, map[string]interface{}{"author": author, "permlink": permlink}, false)
		if err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit().Error
	}

	// Two roots in different categories.
	if err := process(semanticsCommentOp("alice", "root-a", "", "cat-a", `{}`), postCommunityDate); err != nil {
		t.Fatalf("root-a: %v", err)
	}
	if err := process(semanticsCommentOp("bob", "root-b", "", "cat-b", `{}`), postCommunityDate); err != nil {
		t.Fatalf("root-b: %v", err)
	}

	// A comment on root-a; then delete it.
	if err := process(semanticsCommentOp("alice", "moving", "alice", "root-a", `{}`), postCommunityDate); err != nil {
		t.Fatalf("comment: %v", err)
	}
	postRepo := db.NewPostRepository(repo)
	moving, _ := postRepo.GetByAuthorPermlink(ctx, "alice", "moving")
	if moving == nil || moving.Depth != 1 || moving.Category != "cat-a" {
		t.Fatalf("comment seed wrong: %+v", moving)
	}
	createdAt := moving.CreatedAt

	// Pin it artificially (community pin ops are out of scope here); undelete
	// must reset is_pinned (legacy UPDATE sets is_pinned='0').
	database.DB.WithContext(ctx).
		Table("hive_posts").Where("id = ?", moving.ID).Update("is_pinned", true)

	if err := del("alice", "moving"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	// Undelete as a comment on root-b: parent/category/depth all recompute.
	if err := process(semanticsCommentOp("alice", "moving", "bob", "root-b", `{"tags":["whatever"]}`), postCommunityDate); err != nil {
		t.Fatalf("undelete: %v", err)
	}

	after, _ := postRepo.GetByAuthorPermlink(ctx, "alice", "moving")
	rootB, _ := postRepo.GetByAuthorPermlink(ctx, "bob", "root-b")
	if after == nil || rootB == nil {
		t.Fatalf("posts missing after undelete: %+v %+v", after, rootB)
	}
	if after.IsDeleted || after.IsPinned {
		t.Errorf("undelete flags: is_deleted=%v is_pinned=%v, want false/false", after.IsDeleted, after.IsPinned)
	}
	if !after.ParentID.Valid || after.ParentID.Int64 != rootB.ID {
		t.Errorf("undelete parent = %v, want root-b id %d", after.ParentID, rootB.ID)
	}
	if after.Category != "cat-b" || after.Depth != 1 {
		t.Errorf("undelete category/depth = %q/%d, want cat-b/1", after.Category, after.Depth)
	}
	if !after.CreatedAt.Equal(createdAt) {
		t.Errorf("undelete created_at = %v, want original %v (legacy undelete does not touch created_at)", after.CreatedAt, createdAt)
	}

	// Root-post undelete restores the feed cache entry.
	accountRepo := db.NewAccountRepository(repo)
	alice, _ := accountRepo.GetByName(ctx, "alice")
	if err := del("alice", "root-a"); err != nil {
		t.Fatalf("delete root-a: %v", err)
	}
	var n int64
	database.DB.WithContext(ctx).Table("hive_feed_cache").Where("post_id = ?", func() int64 {
		p, _ := postRepo.GetByAuthorPermlink(ctx, "alice", "root-a")
		return p.ID
	}()).Count(&n)
	if n != 0 {
		t.Errorf("feed rows after root delete = %d, want 0", n)
	}
	if err := process(semanticsCommentOp("alice", "root-a", "", "cat-a", `{}`), postCommunityDate); err != nil {
		t.Fatalf("undelete root: %v", err)
	}
	rootA, _ := postRepo.GetByAuthorPermlink(ctx, "alice", "root-a")
	database.DB.WithContext(ctx).Table("hive_feed_cache").
		Where("post_id = ? AND account_id = ?", rootA.ID, alice.ID).Count(&n)
	if n != 1 {
		t.Errorf("feed rows after root undelete = %d, want 1 (feed cache backfill)", n)
	}

	// The undeleted post re-enters the cache pipeline (queued for a write).
	if pi.cachedPost.PendingQueueLen() == 0 {
		t.Error("undeleted post not queued for cache write")
	}
}

// CachedPost.Undelete placeholder path: when the cache already spans the
// post's id, a dummy row is force-created with the recomputed category.
func TestCachedPostUndeletePlaceholder(t *testing.T) {
	database, _, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	tx := database.DB.WithContext(ctx).Begin()
	tx.Create(&models.Account{Name: "alice", CreatedAt: preCommunityDate})
	tx.Create(&models.Account{Name: "bob", CreatedAt: preCommunityDate})
	tx.Commit()
	post := &models.Post{Author: "alice", Permlink: "p1", Category: "recomputed-cat",
		CreatedAt: postCommunityDate, Depth: 0, IsValid: true}
	database.DB.WithContext(ctx).Create(post)

	// Cache spans a HIGHER post id, forcing the placeholder branch.
	span := &models.Post{Author: "bob", Permlink: "p2", Category: "x",
		CreatedAt: postCommunityDate, Depth: 0, IsValid: true}
	database.DB.WithContext(ctx).Create(span)
	tx = database.DB.WithContext(ctx).Begin()
	tx.Exec("INSERT INTO hive_posts_cache (post_id, author, permlink, category) VALUES (?, 'bob', 'p2', 'x')", span.ID)
	tx.Commit()

	cp := NewCachedPost(database.DB, &fakeSteemProvider{})
	if err := cp.Undelete(ctx, post.ID, "alice", "p1", "recomputed-cat"); err != nil {
		t.Fatalf("Undelete: %v", err)
	}
	var n int64
	database.DB.WithContext(ctx).Table("hive_posts_cache").
		Where("post_id = ? AND category = ?", post.ID, "recomputed-cat").Count(&n)
	if n != 1 {
		t.Errorf("placeholder rows = %d, want 1 with recomputed category", n)
	}
	if cp.PendingQueueLen() != 1 {
		t.Errorf("queue = %d, want 1 (update queued after placeholder)", cp.PendingQueueLen())
	}
}

// steemdCommentFor builds a get_content-style post map for a comment with the
// given chain-of-ancestry fields (the recount bubble reads parent_author /
// parent_permlink from the steemd payload).
func steemdCommentFor(author, permlink, parentAuthor, parentPermlink string, depth, children int) map[string]interface{} {
	m := steemdPostFor(author, permlink, "comment body")
	m["category"] = parentPermlink
	m["depth"] = depth
	m["children"] = children
	m["parent_author"] = parentAuthor
	m["parent_permlink"] = parentPermlink
	return m
}

// 3.4 (P1-5): creating/deleting a comment queues a recount of the DIRECT
// parent only; the bubble up the ancestor chain happens at flush time (each
// recounted comment re-queues its own parent, legacy cached_post._sql
// "if recounting, update the parent next pass"). Gated on !isInitialSync.
func TestCommentRecountChaining(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedSemanticsAccount(t, database, "alice")
	seedSemanticsAccount(t, database, "bob")

	// Steemd view of the chain: root <- c1 <- c2, children counts as chain
	// state would report them (root sees all 2 descendants, c1 sees 1).
	steemd := map[string]map[string]interface{}{}
	provider := &fakeSteemProvider{getContentBatch: func(_ context.Context, posts [][]string) ([]map[string]interface{}, error) {
		out := make([]map[string]interface{}, len(posts))
		for i, p := range posts {
			if m, ok := steemd[p[0]+"/"+p[1]]; ok {
				out[i] = m
			} else {
				out[i] = map[string]interface{}{}
			}
		}
		return out, nil
	}}

	pi := newSemanticsPostIndexer(database, repo, provider)
	postRepo := db.NewPostRepository(repo)

	process := func(op map[string]interface{}, initial bool) error {
		tx := database.DB.WithContext(ctx).Begin()
		err := pi.ProcessComment(ctx, tx, op, postCommunityDate, initial)
		if err != nil {
			tx.Rollback()
			return err
		}
		return tx.Commit().Error
	}

	// Initial-sync mode first: no cache queueing may happen at all.
	if err := process(semanticsCommentOp("alice", "root", "", "life", `{}`), true); err != nil {
		t.Fatalf("root: %v", err)
	}
	if err := process(semanticsCommentOp("bob", "c1", "alice", "root", `{}`), true); err != nil {
		t.Fatalf("c1: %v", err)
	}
	if pi.cachedPost.PendingQueueLen() != 0 {
		t.Fatalf("initial-sync queue = %d, want 0 (cache work is deferred to the trailing rebuild)", pi.cachedPost.PendingQueueLen())
	}

	// Play the trailing recovery (what RecoverMissingPosts does after initial
	// sync) so the cache spans root+c1; a live insert bumping lastID past
	// uncached ids would otherwise trip the ensureSafeGap guard.
	steemd["alice/root"] = steemdCommentFor("alice", "root", "", "life", 0, 1)
	steemd["bob/c1"] = steemdCommentFor("bob", "c1", "alice", "root", 1, 0)
	if err := pi.cachedPost.RecoverMissingPosts(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	// Live mode: create the grandchild comment. Steemd now reports the
	// post-initial-sync children counts (root 2, c1 1).
	steemd["alice/root"] = steemdCommentFor("alice", "root", "", "life", 0, 2)
	steemd["bob/c1"] = steemdCommentFor("bob", "c1", "alice", "root", 1, 1)
	steemd["alice/c2"] = steemdCommentFor("alice", "c2", "bob", "c1", 2, 0)
	if err := process(semanticsCommentOp("alice", "c2", "bob", "c1", `{}`), false); err != nil {
		t.Fatalf("c2: %v", err)
	}

	// c2 was queued at insert level; the DIRECT parent c1 at recount level.
	if got := pi.cachedPost.PendingQueueLen(); got != 2 {
		t.Fatalf("queue after comment = %d, want 2 (insert c2 + recount c1)", got)
	}

	// Flush #1: c2 written (insert), c1 recounted; recounting c1 (a comment)
	// must bubble the recount up to the root.
	counts, err := pi.cachedPost.Flush(ctx, false)
	if err != nil {
		t.Fatalf("flush1: %v", err)
	}
	if counts["insert"] != 1 || counts["recount"] != 1 {
		t.Fatalf("flush1 counts = %v, want 1 insert + 1 recount", counts)
	}
	if got := pi.cachedPost.PendingQueueLen(); got != 1 {
		t.Fatalf("queue after flush1 = %d, want 1 (root bubbled recount)", got)
	}

	// Flush #2: the root recount lands; children column carries the chain
	// count from the steemd payload.
	if _, err := pi.cachedPost.Flush(ctx, false); err != nil {
		t.Fatalf("flush2: %v", err)
	}
	root, _ := postRepo.GetByAuthorPermlink(ctx, "alice", "root")
	c1, _ := postRepo.GetByAuthorPermlink(ctx, "bob", "c1")
	var rootChildren int16
	database.DB.WithContext(ctx).Table("hive_posts_cache").
		Where("post_id = ?", root.ID).Select("children").Scan(&rootChildren)
	if rootChildren != 2 {
		t.Errorf("root cache children = %d, want 2 (bubbled recount)", rootChildren)
	}
	var c1Children int16
	database.DB.WithContext(ctx).Table("hive_posts_cache").
		Where("post_id = ?", c1.ID).Select("children").Scan(&c1Children)
	if c1Children != 1 {
		t.Errorf("c1 cache children = %d, want 1", c1Children)
	}

	// Deleting a comment recounts its parent.
	tx := database.DB.WithContext(ctx).Begin()
	if err := pi.ProcessDelete(ctx, tx, map[string]interface{}{"author": "alice", "permlink": "c2"}, false); err != nil {
		t.Fatalf("delete c2: %v", err)
	}
	tx.Commit()
	if got := pi.cachedPost.PendingQueueLen(); got != 1 {
		t.Fatalf("queue after delete = %d, want 1 (recount of c1)", got)
	}

	// Initial-sync delete: no recount queueing.
	tx = database.DB.WithContext(ctx).Begin()
	if err := pi.ProcessDelete(ctx, tx, map[string]interface{}{"author": "bob", "permlink": "c1"}, true); err != nil {
		t.Fatalf("delete c1 (initial): %v", err)
	}
	tx.Commit()
	if got := pi.cachedPost.PendingQueueLen(); got != 1 {
		t.Fatalf("queue after initial-sync delete = %d, want 1 (unchanged)", got)
	}
}

// Editing a live post is cache-only: legacy Posts.update performs no
// hive_posts write, so the row keeps its original values.
func TestEditIsCacheOnly(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()
	ctx := context.Background()

	seedSemanticsAccount(t, database, "alice")
	pi := newSemanticsPostIndexer(database, repo, &fakeSteemProvider{})

	tx := database.DB.WithContext(ctx).Begin()
	if err := pi.ProcessComment(ctx, tx, semanticsCommentOp("alice", "p1", "", "orig-cat", `{"tags":["orig"]}`), postCommunityDate, false); err != nil {
		tx.Rollback()
		t.Fatalf("create: %v", err)
	}
	tx.Commit()

	// Edit with a different (decoy) category/tag set: nothing may change.
	tx = database.DB.WithContext(ctx).Begin()
	if err := pi.ProcessComment(ctx, tx, semanticsCommentOp("alice", "p1", "", "decoy-cat", `{"tags":["decoy"]}`), postCommunityDate, false); err != nil {
		tx.Rollback()
		t.Fatalf("edit: %v", err)
	}
	tx.Commit()

	post, _ := db.NewPostRepository(repo).GetByAuthorPermlink(ctx, "alice", "p1")
	if post == nil || post.Category != "orig-cat" {
		t.Errorf("category after edit = %v, want orig-cat (edit must not touch hive_posts)", post)
	}
	// The edit queued a cache update.
	if pi.cachedPost.PendingQueueLen() != 1 {
		t.Errorf("queue after edit = %d, want 1 (cache update)", pi.cachedPost.PendingQueueLen())
	}
}
