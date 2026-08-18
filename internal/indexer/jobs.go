package indexer

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	"gorm.io/gorm"

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
		if err := j.db.WithContext(ctx).
			Table("hive_posts hp").
			Select("hp.id", "hp.author", "hp.permlink").
			Joins("LEFT JOIN hive_posts_cache hpc ON hp.id = hpc.post_id").
			Where("hp.is_deleted = false AND hp.id BETWEEN ? AND ? AND hpc.post_id IS NULL", lbound, ubound).
			Scan(&missing).Error; err != nil {
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
		if err := j.db.WithContext(ctx).
			Table("hive_posts hp").
			Select("hp.id", "hp.author", "hp.permlink").
			Joins("JOIN hive_posts_cache hpc ON hp.id = hpc.post_id").
			Where("hp.is_deleted = true AND hp.id BETWEEN ? AND ?", lbound, ubound).
			Scan(&extra).Error; err != nil {
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
	j := &cacheJobs{db: database, cp: cp, steem: provider, logger: jobLogger()}
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
		if err := j.db.WithContext(ctx).
			Table("hive_posts").
			Select("id", "author", "permlink").
			Where("is_deleted = true AND id BETWEEN ? AND ?", lbound, ubound).
			Scan(&rows).Error; err != nil {
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
func (j *cacheJobs) undeletePost(ctx context.Context, post map[string]interface{}, pid int64) error {
	author := getStr(post, "author")
	permlink := getStr(post, "permlink")
	category := getStr(post, "category")
	depth, _ := numberInt(post["depth"])

	updates := map[string]interface{}{
		"is_deleted": false,
		"is_pinned":  false,
		"is_valid":   true,
		"is_muted":   false,
		"category":   category,
		"depth":      depth,
	}
	// parent_id from the on-chain parent (comments).
	if pa := getStr(post, "parent_author"); pa != "" {
		if pp := getStr(post, "parent_permlink"); pp != "" {
			var parentID int64
			if err := j.db.WithContext(ctx).
				Table("hive_posts").
				Select("id").
				Where("author = ? AND permlink = ?", pa, pp).
				Scan(&parentID).Error; err == nil && parentID > 0 {
				updates["parent_id"] = parentID
			}
		}
	}
	if created, err := ParseTime(getStr(post, "created")); err == nil {
		updates["created_at"] = created
	}

	if err := j.db.WithContext(ctx).
		Table("hive_posts").
		Where("id = ?", pid).
		Updates(updates).Error; err != nil {
		return err
	}
	return j.cp.Undelete(ctx, pid, author, permlink, category)
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
