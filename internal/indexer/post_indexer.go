package indexer

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// PostIndexer handles post indexing
type PostIndexer struct {
	repo          *db.Repository
	cachedPost    *CachedPost
	logger        *zap.Logger
	notifyIndexer *NotifyIndexer
	// communities resolves community ids and post authorization for
	// buildPost (legacy Community.validated_id / is_post_valid). It is the
	// BlockProcessor's CommunityIndexer instance; the type is stateless, so
	// any instance sharing the same repository works.
	communities *CommunityIndexer
}

// NewPostIndexer creates a new post indexer. communities must be provided:
// buildPost's community_id resolution (P1-17) depends on it, and a nil
// injection would silently re-introduce the "community_id always NULL" bug --
// fail fast at construction instead (same policy as NewCustomOpProcessor).
func NewPostIndexer(repo *db.Repository, cachedPost *CachedPost, communities *CommunityIndexer, logger *zap.Logger) *PostIndexer {
	if communities == nil {
		panic("NewPostIndexer: communities must not be nil; inject the BlockProcessor's CommunityIndexer (P1-17)")
	}
	return &PostIndexer{
		repo:          repo,
		cachedPost:    cachedPost,
		logger:        logger,
		notifyIndexer: NewNotifyIndexer(repo),
		communities:   communities,
	}
}

// builtPost is the result of buildPost: the validated and normalized hive_posts
// fields derived from a comment op (legacy hive/indexer/posts.py _build_post).
type builtPost struct {
	isValid  bool
	isMuted  bool
	parentID sql.NullInt64
	depth    int16
	category string
	// communityID is NULL (invalid) for non-community posts.
	communityID sql.NullInt64
	// errPayload is legacy post['error']: only ever "not authorized" (see
	// buildPost for why the legacy reply-to-invalid/muted strings are dead).
	errPayload string
}

// buildPost validates and normalizes a comment operation (legacy
// Posts._build_post):
//
//   - top-level post (no parent_author): category is ALWAYS the on-chain
//     parent_permlink (never a json_metadata tag, P1-4), depth 0, no parent,
//     and -- past the communities launch date -- community_id resolves through
//     the community table when the category names a community (P1-17).
//   - comment: inherits parent_id, depth+1, category, community_id, is_valid
//     and is_muted from the parent row. A missing parent is an orphan comment
//     and fails loud (P1-7): legacy raises (tuple-unpack of a NULL row) which
//     crashes the sync; Go fails the block for retry. Downgrading to a root
//     post would corrupt depth/category/inheritance silently.
//
// After the branch, a community post whose author is unauthorized gets
// is_muted=true and the "not authorized" error payload (legacy sets
// error='not authorized' / is_muted=True; its earlier reply-to-invalid/muted
// error strings are unconditionally overwritten by `error = None` before this
// check and can never surface -- mirrored here by only ever producing the one
// payload).
func (pi *PostIndexer) buildPost(ctx context.Context, tx *gorm.DB, op map[string]interface{}, blockDate time.Time) (*builtPost, error) {
	parentAuthor, _ := op["parent_author"].(string)
	parentPermlink, _ := op["parent_permlink"].(string)

	bp := &builtPost{}

	if parentAuthor == "" {
		// Top-level post.
		bp.isValid = true
		bp.depth = 0
		bp.category = parentPermlink
		if blockDate.After(communityStartDate) {
			cid, err := pi.communities.ValidatedID(ctx, tx, parentPermlink)
			if err != nil {
				return nil, fmt.Errorf("failed to resolve community for category %q: %w", parentPermlink, err)
			}
			if cid != 0 {
				bp.communityID = sql.NullInt64{Int64: cid, Valid: true}
			}
		}
	} else {
		// Comment: inherit the parent's properties.
		postRepo := db.NewPostRepository(pi.repo.WithTx(tx))
		parent, err := postRepo.GetByAuthorPermlink(ctx, parentAuthor, parentPermlink)
		if err != nil {
			return nil, fmt.Errorf("failed to look up parent post %s/%s: %w", parentAuthor, parentPermlink, err)
		}
		if parent == nil {
			author, _ := op["author"].(string)
			permlink, _ := op["permlink"].(string)
			// Orphan comment: legacy posts.py crashes here (DB.query_row
			// with id=None unpacks to TypeError and halts the sync); never
			// downgrade to a root post (P1-7). Returned as a plain error so
			// the block fails and the poison-block observation kicks in.
			return nil, fmt.Errorf("orphan comment %s/%s: parent post %s/%s does not exist",
				author, permlink, parentAuthor, parentPermlink)
		}
		bp.parentID = sql.NullInt64{Int64: parent.ID, Valid: true}
		bp.depth = parent.Depth + 1
		bp.category = parent.Category
		bp.communityID = parent.CommunityID
		bp.isValid = parent.IsValid
		bp.isMuted = parent.IsMuted
	}

	// Community authorization check (legacy: `if community_id and is_valid
	// and not Community.is_post_valid(...)`).
	if bp.communityID.Valid && bp.isValid {
		ok, err := pi.communities.IsPostValid(ctx, tx, bp.communityID.Int64, op)
		if err != nil {
			return nil, fmt.Errorf("failed to validate community post authorization: %w", err)
		}
		if !ok {
			bp.errPayload = "not authorized"
			bp.isMuted = true
		}
	}

	return bp, nil
}

// ProcessComment processes a comment operation: insert (new post), update
// (edit) or undelete (reuse of a deleted slot), mirroring legacy
// Posts.comment_op dispatch. All reads and writes ride the block transaction
// (P1-15). Cache-queue and feed-cache side effects are gated on
// !isInitialSync exactly as legacy gates on DbState.is_initial_sync() --
// during initial sync the caches are populated wholesale by the trailing
// RecoverMissingPosts/Rebuild instead.
func (pi *PostIndexer) ProcessComment(ctx context.Context, tx *gorm.DB, op map[string]interface{}, blockDate time.Time, isInitialSync bool) error {
	author, _ := op["author"].(string)
	permlink, _ := op["permlink"].(string)
	parentAuthor, _ := op["parent_author"].(string)
	parentPermlink, _ := op["parent_permlink"].(string)
	// title and body live in the post cache, not hive_posts; json_metadata
	// is not consulted at all for category (P1-4: category comes from
	// parent_permlink / parent inheritance).

	postRepo := db.NewPostRepository(pi.repo.WithTx(tx))

	existing, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
	if err != nil {
		return fmt.Errorf("failed to check post existence: %w", err)
	}

	if existing != nil && !existing.IsDeleted {
		// Edit of a live post: no hive_posts write (legacy Posts.update only
		// queues a cache refresh), so the row keeps its original values.
		if !isInitialSync && pi.cachedPost != nil {
			pi.cachedPost.Update(author, permlink, existing.ID)
		}
		return nil
	}

	// New or undeleted: derive the full row from the op (legacy _build_post).
	bp, err := pi.buildPost(ctx, tx, op, blockDate)
	if err != nil {
		return err
	}

	if existing != nil {
		// Undelete: re-allocate the deleted slot with a full rebuild (P1-6).
		// legacy Posts.undelete resets is_valid/is_muted/is_deleted/is_pinned/
		// parent_id/category/community_id/depth -- created_at is NOT touched.
		existing.IsDeleted = false
		existing.IsPinned = false
		existing.IsValid = bp.isValid
		existing.IsMuted = bp.isMuted
		existing.ParentID = bp.parentID
		existing.Category = bp.category
		existing.CommunityID = bp.communityID
		existing.Depth = bp.depth
		if err := tx.WithContext(ctx).Save(existing).Error; err != nil {
			return err
		}

		if !isInitialSync {
			if err := pi.writeErrorNotif(ctx, tx, author, existing.ID, bp.errPayload, blockDate); err != nil {
				return err
			}
			if pi.cachedPost != nil {
				if err := pi.cachedPost.Undelete(ctx, existing.ID, author, permlink, bp.category); err != nil {
					return fmt.Errorf("cached-post undelete failed for %s/%s: %w", author, permlink, err)
				}
			}
			// Re-enter the feed cache when a root post is reinstated.
			if err := pi.insertFeedCache(ctx, tx, existing.ID, bp.depth, author, blockDate); err != nil {
				return err
			}
		}
		// legacy Posts.undelete intentionally does NOT recount the parent
		// (only insert and delete do); the audit sweep reconciles children.
		return nil
	}

	// Insert (legacy Posts.insert).
	post := &models.Post{
		Author:      author,
		Permlink:    permlink,
		Category:    bp.category,
		CommunityID: bp.communityID,
		ParentID:    bp.parentID,
		CreatedAt:   blockDate,
		Depth:       bp.depth,
		IsDeleted:   false,
		IsPinned:    false,
		IsValid:     bp.isValid,
		IsMuted:     bp.isMuted,
	}
	if err := tx.WithContext(ctx).Create(post).Error; err != nil {
		return fmt.Errorf("failed to create post: %w", err)
	}

	if !isInitialSync {
		if err := pi.writeErrorNotif(ctx, tx, author, post.ID, bp.errPayload, blockDate); err != nil {
			return err
		}
		if pi.cachedPost != nil {
			pi.cachedPost.Insert(author, permlink, post.ID)
			// A new comment bumps its parent's child count; the recount
			// bubbles up the ancestor chain at flush time (cached_post
			// buildSQLs re-queues each recounted comment's own parent).
			if parentAuthor != "" {
				pi.cachedPost.Recount(parentAuthor, parentPermlink, bp.parentID.Int64)
			}
		}
		if err := pi.insertFeedCache(ctx, tx, post.ID, bp.depth, author, blockDate); err != nil {
			return err
		}
	}

	pi.logger.Debug("Created post",
		zap.String("author", author),
		zap.String("permlink", permlink),
		zap.Int16("depth", post.Depth))

	return nil
}

// writeErrorNotif emits the legacy `error` notification for a post that was
// muted by community authorization rules (legacy: Notify('error', dst=author,
// payload=post['error'])). Missing payload means nothing to write.
func (pi *PostIndexer) writeErrorNotif(ctx context.Context, tx *gorm.DB, author string, postID int64, payload string, blockDate time.Time) error {
	if payload == "" {
		return nil
	}
	accountRepo := db.NewAccountRepository(pi.repo.WithTx(tx))
	account, err := accountRepo.GetByName(ctx, author)
	if err != nil {
		return fmt.Errorf("failed to get account %s for error notif: %w", author, err)
	}
	if account == nil {
		// Author not yet registered (same-block ordering edge). legacy
		// would crash here (Accounts.get_id asserts); Go degrades to
		// skipping the notification. dst_id is nullable in both schemas,
		// so a NULL insert would be expressible -- skipping is the chosen
		// benign divergence; queries filter on dst_id anyway.
		pi.logger.Warn("post error notif skipped: author account missing",
			zap.String("author", author), zap.Int64("post_id", postID))
		return nil
	}
	if err := pi.notifyIndexer.WithTx(tx).Write(ctx, models.NotifyTypeError, blockDate, nil, &account.ID, nil, &postID, &payload, nil); err != nil {
		return fmt.Errorf("failed to write post error notif for %s/%d: %w", author, postID, err)
	}
	return nil
}

// insertFeedCache adds a root post to the feed cache (legacy
// Posts._insert_feed_cache): only depth-0 posts enter the feed, keyed by the
// author's account id.
func (pi *PostIndexer) insertFeedCache(ctx context.Context, tx *gorm.DB, postID int64, depth int16, author string, blockDate time.Time) error {
	if depth != 0 {
		return nil
	}
	accountRepo := db.NewAccountRepository(pi.repo.WithTx(tx))
	account, err := accountRepo.GetByName(ctx, author)
	if err != nil {
		return fmt.Errorf("failed to get account %s for feed cache: %w", author, err)
	}
	if account == nil {
		// See writeErrorNotif: legacy would crash (hive_feed_cache.
		// account_id is NOT NULL in both schemas, a NULL insert raises
		// IntegrityError); Go skips the entry with a warning instead.
		pi.logger.Warn("feed cache insert skipped: author account missing",
			zap.String("author", author), zap.Int64("post_id", postID))
		return nil
	}
	feedCacheRepo := db.NewFeedCacheRepository(pi.repo.WithTx(tx))
	if err := feedCacheRepo.Insert(ctx, postID, account.ID, blockDate); err != nil {
		return fmt.Errorf("failed to insert into feed cache: %w", err)
	}
	return nil
}

// ProcessDelete processes a delete_comment operation (legacy Posts.delete):
// mark the slot deleted, evict feed-cache rows (root posts), drop the cache
// rows, and recount the parent when a comment is deleted. Cache and feed
// side effects are gated on !isInitialSync like legacy. The post lookup and
// feed eviction ride the block transaction (P1-15).
func (pi *PostIndexer) ProcessDelete(ctx context.Context, tx *gorm.DB, op map[string]interface{}, isInitialSync bool) error {
	author, _ := op["author"].(string)
	permlink, _ := op["permlink"].(string)

	postRepo := db.NewPostRepository(pi.repo.WithTx(tx))
	post, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
	if err != nil {
		return fmt.Errorf("failed to get post: %w", err)
	}
	if post == nil {
		// Deleting an unknown post is a no-op. legacy actually crashes on
		// this path (_get_parent_by_child_id(None) asserts); the no-op is
		// an intentional, safer divergence.
		return nil
	}

	// Mark as deleted
	post.IsDeleted = true
	if err := tx.WithContext(ctx).Save(post).Error; err != nil {
		return fmt.Errorf("failed to delete post: %w", err)
	}

	if isInitialSync {
		return nil
	}

	// Evict from both cache tables, tags, and the dirty queue. Fail the
	// block on error: a swallowed failure would leave stale cache rows
	// behind (legacy lets the exception halt the sync).
	if pi.cachedPost != nil {
		if err := pi.cachedPost.Delete(ctx, post.ID, author, permlink); err != nil {
			return fmt.Errorf("cached-post delete failed for %s/%s: %w", author, permlink, err)
		}
	}

	if post.Depth == 0 {
		// A deleted root post evicts ALL feed entries (the author's own plus
		// every reblog), mirroring legacy FeedCache.delete(post_id).
		if err := tx.WithContext(ctx).Where("post_id = ?", post.ID).
			Delete(&models.FeedCache{}).Error; err != nil {
			return fmt.Errorf("failed to evict feed cache for deleted post: %w", err)
		}
	} else if pi.cachedPost != nil {
		// A deleted comment forces a child recount on its parent (legacy
		// _get_parent_by_child_id + CachedPost.recount). The parent row is
		// read through the block transaction; its absence is a consistency
		// break (legacy asserts it exists) and fails the block.
		var parent models.Post
		err := tx.WithContext(ctx).
			Select("id", "author", "permlink").
			Where("id = ?", post.ParentID).
			First(&parent).Error
		if err != nil {
			return fmt.Errorf("parent of %d not found for recount: %w", post.ID, err)
		}
		pi.cachedPost.Recount(parent.Author, parent.Permlink, parent.ID)
	}

	pi.logger.Debug("Deleted post",
		zap.String("author", author),
		zap.String("permlink", permlink))

	return nil
}
