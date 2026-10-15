package indexer

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/logging"
)

// Cache audit jobs. Port of hive/indexer/jobs.py — manual/periodic repair
// tools keeping hive_posts and hive_posts_cache consistent:
//   - AuditCacheMissing:  posts (not deleted) with no cache row → insert
//   - AuditCacheDeleted:  deleted posts that still have a cache row → evict
//   - AuditCacheUndelete: deleted posts that still exist on chain → restore

// jobsBatchStep mirrors the legacy 1M-id batch stride.
const jobsBatchStep = 1000000

type cacheJobs struct {
	db     *gorm.DB
	cp     *CachedPost
	posts  *PostIndexer
	steem  steem.Provider
	logger *zap.Logger
}

// AuditCacheMissing scans for hive_posts rows (not deleted) with no cache
// entry and queues them for insert, flushing per batch.
func AuditCacheMissing(ctx context.Context, database *gorm.DB, cp *CachedPost) error {
	j := &cacheJobs{db: database, cp: cp, logger: jobLogger()}
	lastID, err := j.lastCachedPostID(ctx)
	if err != nil {
		return err
	}
	steps := int(lastID/jobsBatchStep) + 1
	j.logger.Info("audit_cache_missing start", zap.Int64("last_cached_id", lastID), zap.Int("batches", steps))

	for idx := 0; idx < steps; idx++ {
		lbound, ubound := int64(idx*jobsBatchStep+1), int64((idx+1)*jobsBatchStep)
		type row struct {
			ID       int64
			Author   string
			Permlink string
		}
		var missing []row
		// Full-range LEFT JOIN scan; exempt from the session statement
		// timeout (maintenance SQL, §2.5).
		if err := db.WithoutStatementTimeout(ctx, j.db, func(tx *gorm.DB) error {
			return tx.Table("hive_posts hp").
				Select("hp.id", "hp.author", "hp.permlink").
				Joins("LEFT JOIN hive_posts_cache hpc ON hp.id = hpc.post_id").
				Where("hp.is_deleted = false AND hp.id BETWEEN ? AND ? AND hpc.post_id IS NULL", lbound, ubound).
				Scan(&missing).Error
		}); err != nil {
			return err
		}
		for _, r := range missing {
			cp.Insert(r.Author, r.Permlink, r.ID)
		}
		if len(missing) > 0 {
			if _, err := cp.Flush(ctx, true); err != nil {
				return fmt.Errorf("flush missing batch %d: %w", idx, err)
			}
		}
		j.logger.Info("audit_cache_missing batch",
			zap.Int64("lbound", lbound), zap.Int64("ubound", ubound),
			zap.Int("missing", len(missing)))
	}
	return nil
}

// AuditCacheDeleted scans for deleted posts that still have cache entries
// and evicts them from both cache tables.
func AuditCacheDeleted(ctx context.Context, database *gorm.DB, cp *CachedPost) error {
	j := &cacheJobs{db: database, cp: cp, logger: jobLogger()}
	lastID, err := j.lastCachedPostID(ctx)
	if err != nil {
		return err
	}
	steps := int(lastID/jobsBatchStep) + 1
	j.logger.Info("audit_cache_deleted start", zap.Int64("last_cached_id", lastID), zap.Int("batches", steps))

	for idx := 0; idx < steps; idx++ {
		lbound, ubound := int64(idx*jobsBatchStep+1), int64((idx+1)*jobsBatchStep)
		type row struct {
			ID       int64
			Author   string
			Permlink string
		}
		var extra []row
		// Full-range JOIN scan; exempt from the session statement timeout
		// (maintenance SQL, §2.5).
		if err := db.WithoutStatementTimeout(ctx, j.db, func(tx *gorm.DB) error {
			return tx.Table("hive_posts hp").
				Select("hp.id", "hp.author", "hp.permlink").
				Joins("JOIN hive_posts_cache hpc ON hp.id = hpc.post_id").
				Where("hp.is_deleted = true AND hp.id BETWEEN ? AND ?", lbound, ubound).
				Scan(&extra).Error
		}); err != nil {
			return err
		}
		for _, r := range extra {
			if err := cp.Delete(ctx, r.ID, r.Author, r.Permlink); err != nil {
				return err
			}
		}
		j.logger.Info("audit_cache_deleted batch",
			zap.Int64("lbound", lbound), zap.Int64("ubound", ubound),
			zap.Int("deleted", len(extra)))
	}
	return nil
}

// AuditCacheUndelete scans deleted posts, re-checks them on chain via
// get_content_batch, and restores the ones steemd still serves
// (mirrors legacy audit_cache_undelete + Posts.undelete).
func AuditCacheUndelete(ctx context.Context, database *gorm.DB, cp *CachedPost, provider steem.Provider) error {
	repo := db.NewRepository(database)
	// The PostIndexer built here is jobs-local but wraps the SHARED
	// CachedPost (the dirty queue Sync flushes); PostIndexer and
	// CommunityIndexer are otherwise stateless query wrappers, so a
	// jobs-local instance cannot lose writes the way a second FollowIndexer
	// or CachedPost would (audit P0-4 class).
	j := &cacheJobs{
		db:     database,
		cp:     cp,
		posts:  NewPostIndexer(repo, cp, NewCommunityIndexer(repo, cp, jobLogger()), jobLogger()),
		steem:  provider,
		logger: jobLogger(),
	}
	lastID, err := j.lastPostID(ctx)
	if err != nil {
		return err
	}
	steps := int(lastID/jobsBatchStep) + 1
	j.logger.Info("audit_cache_undelete start", zap.Int64("last_post_id", lastID), zap.Int("batches", steps))

	for idx := 0; idx < steps; idx++ {
		lbound, ubound := int64(idx*jobsBatchStep+1), int64((idx+1)*jobsBatchStep)
		type row struct {
			ID       int64
			Author   string
			Permlink string
		}
		var rows []row
		// Full-range scan; exempt from the session statement timeout
		// (maintenance SQL, §2.5).
		if err := db.WithoutStatementTimeout(ctx, j.db, func(tx *gorm.DB) error {
			return tx.Table("hive_posts").
				Select("id", "author", "permlink").
				Where("is_deleted = true AND id BETWEEN ? AND ?", lbound, ubound).
				Scan(&rows).Error
		}); err != nil {
			return err
		}
		if len(rows) == 0 {
			continue
		}

		postArgs := make([][]string, len(rows))
		for i, r := range rows {
			postArgs[i] = []string{r.Author, r.Permlink}
		}
		posts, err := provider.GetContentBatch(ctx, postArgs)
		if err != nil {
			return err
		}

		recovered := 0
		for i, r := range rows {
			if getStr(posts[i], "author") == "" {
				continue // still gone on chain
			}
			if err := j.undeletePost(ctx, posts[i], r.ID); err != nil {
				j.logger.Warn("undelete failed", zap.Int64("id", r.ID), zap.Error(err))
				continue
			}
			recovered++
		}
		j.logger.Info("audit_cache_undelete batch",
			zap.Int64("lbound", lbound), zap.Int64("ubound", ubound),
			zap.Int("recovered", recovered))
		if recovered > 0 {
			if _, err := cp.Flush(ctx, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// undeletePost restores a deleted hive_posts row from its on-chain state
// (legacy Posts.undelete) and re-enters the cache pipeline.
//
// Field derivation reuses the block-path rebuild (PostIndexer.buildPost --
// the same routine the comment-op undelete path runs, PR#406): category from
// parent_permlink (root) or parent inheritance (comment), community_id via
// the community table behind the START_DATE gate, is_valid/is_muted from
// parent inheritance plus the community post-authorization check. Legacy's
// audit path runs the very same Posts.undelete/_build_post (jobs.py
// audit_cache_undelete -> Posts.undelete); hive_posts.created_at is NOT
// touched (legacy's UPDATE does not include it either). The root post also
// re-enters the feed cache (legacy _insert_feed_cache) keyed by the
// author's account id.
//
// Deliberate divergence: legacy writes the community `error` notification
// from Posts.undelete on this path too; the audit is a repair tool and
// re-notifying on repair would fabricate history, so the notification is
// suppressed here.
func (j *cacheJobs) undeletePost(ctx context.Context, post map[string]interface{}, pid int64) error {
	author := getStr(post, "author")
	permlink := getStr(post, "permlink")

	created, err := ParseTime(getStr(post, "created"))
	if err != nil {
		return fmt.Errorf("parse created: %w", err)
	}

	// buildPost consumes the fields a comment op carries; a get_content map
	// (author/permlink/parent_author/parent_permlink) satisfies it, exactly
	// as legacy _build_post runs on the content map in the audit path. The
	// START_DATE community gate is evaluated against the post's on-chain
	// creation time (legacy passes date=post['created'] here).
	bp, err := j.posts.buildPost(ctx, j.db.WithContext(ctx), post, created)
	if err != nil {
		return fmt.Errorf("rebuild post fields: %w", err)
	}

	updates := map[string]interface{}{
		"is_deleted": false,
		"is_pinned":  false,
		"is_valid":   bp.isValid,
		"is_muted":   bp.isMuted,
		"category":   bp.category,
		"depth":      bp.depth,
	}
	if bp.parentID.Valid {
		updates["parent_id"] = bp.parentID.Int64
	} else {
		updates["parent_id"] = nil
	}
	if bp.communityID.Valid {
		updates["community_id"] = bp.communityID.Int64
	} else {
		updates["community_id"] = nil
	}

	if err := j.db.WithContext(ctx).
		Table("hive_posts").
		Where("id = ?", pid).
		Updates(updates).Error; err != nil {
		return err
	}

	// Feed-cache backfill (legacy _insert_feed_cache): only root posts
	// enter the feed.
	if err := j.posts.insertFeedCache(ctx, j.db.WithContext(ctx), pid, bp.depth, author, created); err != nil {
		return err
	}
	return j.cp.Undelete(ctx, pid, author, permlink, bp.category)
}

func (j *cacheJobs) lastPostID(ctx context.Context) (int64, error) {
	var id int64
	err := j.db.WithContext(ctx).
		Table("hive_posts").
		Select("COALESCE(MAX(id), 0)").
		Scan(&id).Error
	return id, err
}

func (j *cacheJobs) lastCachedPostID(ctx context.Context) (int64, error) {
	var id int64
	err := j.db.WithContext(ctx).
		Table("hive_posts_cache").
		Select("COALESCE(MAX(post_id), 0)").
		Scan(&id).Error
	return id, err
}

func jobLogger() *zap.Logger {
	return logging.GetLogger().With(zap.String("component", "cache-jobs"))
}

// RunCacheJobs executes all three audits in order; used by the optional
// periodic scheduler and available for manual invocation.
func RunCacheJobs(ctx context.Context, database *gorm.DB, cp *CachedPost, provider steem.Provider) error {
	if err := AuditCacheMissing(ctx, database, cp); err != nil {
		return err
	}
	if err := AuditCacheDeleted(ctx, database, cp); err != nil {
		return err
	}
	return AuditCacheUndelete(ctx, database, cp, provider)
}
