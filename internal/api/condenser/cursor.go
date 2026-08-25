package condenser

import (
	"context"
	"strings"
	"time"

	"github.com/steemit/hivemind/internal/apierrors"

	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// Cursor provides cursor-based pagination queries
type Cursor struct {
	db *gorm.DB
}

// NewCursor creates a new cursor
func NewCursor(database *gorm.DB) *Cursor {
	return &Cursor{db: database}
}

// GetPostIDByAuthorPermlink gets post ID by author and permlink
func (c *Cursor) GetPostIDByAuthorPermlink(ctx context.Context, author, permlink string) (int64, error) {
	var post models.Post
	err := c.db.WithContext(ctx).
		Where("author = ? AND permlink = ?", author, permlink).
		Select("id").
		First(&post).Error

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, nil
		}
		return 0, err
	}
	return post.ID, nil
}

// GetPostIDsByQuery gets post IDs for a given query
func (c *Cursor) GetPostIDsByQuery(ctx context.Context, sort, startAuthor, startPermlink string, limit int, tag string) ([]int64, error) {
	// Validate sort type
	validSorts := map[string]bool{
		"trending":        true,
		"hot":             true,
		"created":         true,
		"promoted":        true,
		"payout":          true,
		"payout_comments": true,
		"muted":           true,
	}
	if !validSorts[sort] {
		return nil, apierrors.Publicf("invalid sort type: %s", sort)
	}

	// Build query based on sort type
	query := c.db.WithContext(ctx).
		Model(&models.PostCache{}).
		Select("hive_posts_cache.post_id")

	// Apply filters based on sort type
	switch sort {
	case "trending":
		query = query.Where("is_paidout = ?", false).
			Order("sc_trend DESC")
	case "hot":
		query = query.Where("is_paidout = ?", false).
			Order("sc_hot DESC")
	case "created":
		query = query.Where("depth = ?", 0).
			Order("hive_posts_cache.post_id DESC")
	case "promoted":
		query = query.Where("is_paidout = ? AND promoted > ?", false, 0).
			Order("promoted DESC")
	case "payout":
		query = query.Where("is_paidout = ? AND depth = ?", false, 0).
			Order("payout DESC")
	case "payout_comments":
		query = query.Where("is_paidout = ? AND depth > ?", false, 0).
			Order("payout DESC")
	case "muted":
		// Grayed posts with pending payout, by payout (bridge muted sort).
		query = query.Where("is_paidout = ? AND is_grayed = ? AND payout > ?", false, true, 0).
			Order("payout DESC")
	}

	// Filter by tag if provided
	if tag != "" {
		if strings.HasPrefix(tag, "hive-") {
			// Community tag
			query = query.Where("category = ?", tag)
			if sort == "trending" || sort == "hot" {
				query = query.Where("depth = ?", 0)
			}
		} else {
			// Regular tag - join with hive_post_tags
			query = query.Joins("INNER JOIN hive_post_tags ON hive_posts_cache.post_id = hive_post_tags.post_id").
				Where("hive_post_tags.tag = ?", tag)
		}
	}

	// Handle pagination
	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, startAuthor, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []int64{}, nil
		}

		// Get the sort field value for the start post
		var startValue interface{}
		switch sort {
		case "trending":
			var cache models.PostCache
			if err := c.db.WithContext(ctx).Where("post_id = ?", startID).Select("sc_trend").First(&cache).Error; err == nil {
				startValue = cache.SCTrend
			}
		case "hot":
			var cache models.PostCache
			if err := c.db.WithContext(ctx).Where("post_id = ?", startID).Select("sc_hot").First(&cache).Error; err == nil {
				startValue = cache.SCHot
			}
		case "created", "payout", "payout_comments", "muted":
			startValue = startID
		}

		if startValue != nil {
			switch sort {
			case "trending":
				query = query.Where("sc_trend <= ?", startValue)
			case "hot":
				query = query.Where("sc_hot <= ?", startValue)
			case "created", "payout", "payout_comments", "muted":
				query = query.Where("hive_posts_cache.post_id <= ?", startValue)
			}
		}
	}

	// Apply limit (clamped: a negative SQL LIMIT is treated as unbounded,
	// which would full-scan the cache table).
	query = query.Limit(apierrors.ClampLimit(limit))

	// Execute query
	var results []struct {
		PostID int64 `gorm:"column:post_id"`
	}
	if err := query.Scan(&results).Error; err != nil {
		return nil, err
	}

	// Extract post IDs
	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.PostID
	}

	return ids, nil
}

// FeedEntry pairs a post ID with the CSV of account names that surfaced it
// in a feed (string_agg over hive_feed_cache, mirrors legacy
// pids_by_feed_with_reblog).
type FeedEntry struct {
	PostID   int64  `gorm:"column:post_id"`
	Accounts string `gorm:"column:accounts"`
}

// getAccountIDByName resolves an account name to its hive_accounts.id.
// Returns 0 when the account does not exist.
func (c *Cursor) getAccountIDByName(ctx context.Context, name string) (int64, error) {
	var acc models.Account
	err := c.db.WithContext(ctx).
		Where("name = ?", name).
		Select("id").
		First(&acc).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, nil
		}
		return 0, err
	}
	return acc.ID, nil
}

// GetPostIDsByAccountPosts returns top-level posts (depth = 0) authored by
// `account`. Mirrors legacy bridge cursor.pids_by_posts.
func (c *Cursor) GetPostIDsByAccountPosts(ctx context.Context, account, startPermlink string, limit int) ([]int64, error) {
	query := c.db.WithContext(ctx).
		Model(&models.Post{}).
		Select("id").
		Where("author = ? AND is_deleted = ? AND depth = ?", account, false, 0)

	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, account, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []int64{}, nil
		}
		query = query.Where("id <= ?", startID)
	}

	var results []struct {
		ID int64 `gorm:"column:id"`
	}
	// `depth` in ORDER BY is a no-op, but forces an ix3 index scan (see #189)
	if err := query.Order("id DESC, depth").Limit(apierrors.ClampLimit(limit)).Scan(&results).Error; err != nil {
		return nil, err
	}

	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids, nil
}

// GetPostIDsByAccountComments returns comments (depth > 0) authored by
// `account`. Mirrors legacy cursor.pids_by_account_comments /
// bridge cursor.pids_by_comments (identical SQL).
func (c *Cursor) GetPostIDsByAccountComments(ctx context.Context, account, startPermlink string, limit int) ([]int64, error) {
	query := c.db.WithContext(ctx).
		Model(&models.Post{}).
		Select("id").
		Where("author = ? AND is_deleted = ? AND depth > ?", account, false, 0)

	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, account, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []int64{}, nil
		}
		query = query.Where("id <= ?", startID)
	}

	var results []struct {
		ID int64 `gorm:"column:id"`
	}
	if err := query.Order("id DESC, depth").Limit(apierrors.ClampLimit(limit)).Scan(&results).Error; err != nil {
		return nil, err
	}

	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids, nil
}

// GetPostIDsByBlogWithoutReblog returns an author's blog posts without
// reblogs. Mirrors legacy cursor.pids_by_blog_without_reblog. before_date is
// ignored, matching legacy (broken in steemd as well).
func (c *Cursor) GetPostIDsByBlogWithoutReblog(ctx context.Context, account, startPermlink string, limit int) ([]int64, error) {
	query := c.db.WithContext(ctx).
		Model(&models.Post{}).
		Select("id").
		Where("author = ? AND is_deleted = ? AND depth = ?", account, false, 0)

	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, account, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []int64{}, nil
		}
		query = query.Where("id <= ?", startID)
	}

	var results []struct {
		ID int64 `gorm:"column:id"`
	}
	if err := query.Order("id DESC").Limit(apierrors.ClampLimit(limit)).Scan(&results).Error; err != nil {
		return nil, err
	}

	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids, nil
}

// GetPostIDsByRepliesToAccount returns replies made to any of `startAuthor`'s
// posts. First page: startAuthor = the account being replied to. Subsequent
// pages: pass the last loaded reply's author/permlink. Mirrors legacy
// cursor.pids_by_replies_to_account.
func (c *Cursor) GetPostIDsByRepliesToAccount(ctx context.Context, startAuthor, startPermlink string, limit int) ([]int64, error) {
	parentAccount := startAuthor
	seek := ""
	args := []interface{}{parentAccount}

	if startPermlink != "" {
		var row struct {
			ParentAuthor string `gorm:"column:parent_author"`
			ID           int64  `gorm:"column:id"`
		}
		err := c.db.WithContext(ctx).Raw(
			`SELECT parent.author AS parent_author, child.id
			   FROM hive_posts child
			   JOIN hive_posts parent ON child.parent_id = parent.id
			  WHERE child.author = ? AND child.permlink = ?`,
			startAuthor, startPermlink,
		).Scan(&row).Error
		if err != nil {
			return nil, err
		}
		if row.ID == 0 {
			return []int64{}, nil
		}
		parentAccount = row.ParentAuthor
		seek = "AND id <= ?"
		args = append(args, row.ID)
	}

	sql := `
	   SELECT id FROM hive_posts
	    WHERE parent_id IN (SELECT id FROM hive_posts
	                         WHERE author = ?
	                           AND is_deleted = '0'
	                      ORDER BY id DESC
	                         LIMIT 10000) ` + seek + `
	      AND is_deleted = '0'
	 ORDER BY id DESC
	    LIMIT ?`
	args = append(args, apierrors.ClampLimit(limit))

	var results []struct {
		ID int64 `gorm:"column:id"`
	}
	if err := c.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, err
	}

	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.ID
	}
	return ids, nil
}

// GetPostIDsByPayout returns an author's posts sorted by pending payout,
// reading the hot-data window from hive_posts_cache_temp (mirrors legacy
// bridge cursor.pids_by_payout which reads CacheRouter.TEMP_TABLE).
func (c *Cursor) GetPostIDsByPayout(ctx context.Context, account, startAuthor, startPermlink string, limit int) ([]int64, error) {
	seek := ""
	var args []interface{}

	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, startAuthor, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []int64{}, nil
		}
		seek = `AND (payout < (SELECT payout FROM hive_posts_cache_temp WHERE post_id = ?)
                  OR (payout = (SELECT payout FROM hive_posts_cache_temp WHERE post_id = ?) AND post_id > ?))`
		args = append(args, startID, startID, startID)
	}

	sql := `
	    SELECT post_id
	      FROM hive_posts_cache_temp
	     WHERE author = ?
	       AND is_paidout = '0' ` + seek + `
	 ORDER BY payout DESC, post_id
	    LIMIT ?`
	args = append(args, account, apierrors.ClampLimit(limit))

	var results []struct {
		PostID int64 `gorm:"column:post_id"`
	}
	if err := c.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, err
	}

	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.PostID
	}
	return ids, nil
}

// GetPostIDsByFeedWithReblog returns the personalized feed for `account`
// (posts + reblogs from accounts they follow), with the reblogger names
// aggregated per post. Mirrors legacy bridge cursor.pids_by_feed_with_reblog:
// single GROUP BY query over hive_feed_cache joined with hive_follows.
func (c *Cursor) GetPostIDsByFeedWithReblog(ctx context.Context, account, startAuthor, startPermlink string, limit int) ([]FeedEntry, error) {
	accountID, err := c.getAccountIDByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if accountID == 0 {
		return []FeedEntry{}, nil
	}

	seek := ""
	var args []interface{}

	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, startAuthor, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []FeedEntry{}, nil
		}

		// Pre-fetch the seek timestamp to avoid a nested subquery in HAVING.
		var startCreatedAt struct {
			CreatedAt time.Time `gorm:"column:created_at"`
		}
		err = c.db.WithContext(ctx).Raw(
			`SELECT MIN(fc.created_at) AS created_at
			   FROM hive_feed_cache fc
			  INNER JOIN hive_follows hf
			      ON fc.account_id = hf.following
			     AND hf.follower = ?
			     AND hf.state IN (1, 3)
			  WHERE fc.post_id = ?`,
			accountID, startID,
		).Scan(&startCreatedAt).Error
		if err != nil {
			return nil, err
		}
		if !startCreatedAt.CreatedAt.IsZero() {
			seek = "HAVING MIN(hive_feed_cache.created_at) <= ?"
			args = append(args, startCreatedAt.CreatedAt)
		}
	}

	cutoff := time.Now().AddDate(0, -1, 0)
	sql := `
	    SELECT post_id, string_agg(name, ',') AS accounts
	      FROM hive_feed_cache
	     INNER JOIN hive_follows
	         ON account_id = hive_follows.following
	        AND hive_follows.follower = ?
	        AND hive_follows.state IN (1, 3)
	     INNER JOIN hive_accounts ON hive_follows.following = hive_accounts.id
	     WHERE hive_feed_cache.created_at > ? ` + seek + `
	  GROUP BY post_id
	 ORDER BY MIN(hive_feed_cache.created_at) DESC
	    LIMIT ?`
	args = append(args, accountID, cutoff, apierrors.ClampLimit(limit))

	var results []FeedEntry
	if err := c.db.WithContext(ctx).Raw(sql, args...).Scan(&results).Error; err != nil {
		return nil, err
	}
	return results, nil
}

// GetPostIDsByBlogByIndex pages an author's blog (w/ reblogs) by entry index.
// (acct, -1) or (acct, 0) resolves to the newest entry; the returned slice is
// ordered newest-first like legacy pids_by_blog_by_index.
func (c *Cursor) GetPostIDsByBlogByIndex(ctx context.Context, account string, startIndex, limit int) (int, []int64, error) {
	accountID, err := c.getAccountIDByName(ctx, account)
	if err != nil {
		return 0, nil, err
	}
	if accountID == 0 {
		return 0, []int64{}, nil
	}

	if startIndex == -1 || startIndex == 0 {
		var count int64
		if err := c.db.WithContext(ctx).Model(&models.FeedCache{}).
			Where("account_id = ?", accountID).
			Count(&count).Error; err != nil {
			return 0, nil, err
		}
		startIndex = int(count) - 1
		if startIndex < 0 {
			return 0, []int64{}, nil
		}
	}

	offset := startIndex - limit + 1
	if offset < 0 {
		return 0, nil, apierrors.Publicf("start_index and limit combination is invalid (%d, %d)", startIndex, limit)
	}

	var results []struct {
		PostID int64 `gorm:"column:post_id"`
	}
	if err := c.db.WithContext(ctx).
		Model(&models.FeedCache{}).
		Select("post_id").
		Where("account_id = ?", accountID).
		Order("created_at").
		Offset(offset).
		Limit(apierrors.ClampLimit(limit)).
		Scan(&results).Error; err != nil {
		return 0, nil, err
	}

	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.PostID
	}
	// Reverse so the result is newest-first.
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
	return startIndex, ids, nil
}

// GetFollowerNames returns names of accounts following `account`
// (created_at DESC cursor, mirroring legacy condenser cursor.get_followers).
func (c *Cursor) GetFollowerNames(ctx context.Context, account, start string, limit int) ([]string, error) {
	accountID, err := c.getAccountIDByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	sql := `
	    SELECT ha.name
	      FROM hive_follows hf
	     INNER JOIN hive_accounts ha ON hf.follower = ha.id
	     WHERE hf.following = ?
	       AND hf.state IN (1, 3)`
	args := []interface{}{accountID}

	if start != "" {
		startID, err := c.getAccountIDByName(ctx, start)
		if err != nil {
			return nil, err
		}
		if startID != 0 {
			sql += ` AND hf.created_at <= (
			             SELECT created_at FROM hive_follows
			              WHERE following = ? AND follower = ?)`
			args = append(args, accountID, startID)
		}
	}
	sql += " ORDER BY hf.created_at DESC LIMIT ?"
	args = append(args, apierrors.ClampLimit(limit))

	var names []string
	if err := c.db.WithContext(ctx).Raw(sql, args...).Scan(&names).Error; err != nil {
		return nil, err
	}
	return names, nil
}

// GetFollowingNames returns names of accounts followed by `account`
// (created_at DESC cursor, mirroring legacy condenser cursor.get_following).
func (c *Cursor) GetFollowingNames(ctx context.Context, account, start string, limit int) ([]string, error) {
	accountID, err := c.getAccountIDByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	sql := `
	    SELECT ha.name
	      FROM hive_follows hf
	     INNER JOIN hive_accounts ha ON hf.following = ha.id
	     WHERE hf.follower = ?
	       AND hf.state IN (1, 3)`
	args := []interface{}{accountID}

	if start != "" {
		startID, err := c.getAccountIDByName(ctx, start)
		if err != nil {
			return nil, err
		}
		if startID != 0 {
			sql += ` AND hf.created_at <= (
			             SELECT created_at FROM hive_follows
			              WHERE follower = ? AND following = ?)`
			args = append(args, accountID, startID)
		}
	}
	sql += " ORDER BY hf.created_at DESC LIMIT ?"
	args = append(args, apierrors.ClampLimit(limit))

	var names []string
	if err := c.db.WithContext(ctx).Raw(sql, args...).Scan(&names).Error; err != nil {
		return nil, err
	}
	return names, nil
}

// GetMutedNames returns names of all accounts muted by `account`
// (state includes the ignore bit). Mirrors legacy hive_api list_all_muted.
func (c *Cursor) GetMutedNames(ctx context.Context, account string) ([]string, error) {
	accountID, err := c.getAccountIDByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	sql := `
	    SELECT a.name
	      FROM hive_follows f
	      JOIN hive_accounts a ON f.following = a.id
	     WHERE f.follower = ? AND f.state IN (2, 3)`

	var names []string
	if err := c.db.WithContext(ctx).Raw(sql, accountID).Scan(&names).Error; err != nil {
		return nil, err
	}
	return names, nil
}

// GetPostIDsByBlog gets post IDs for an account's blog
func (c *Cursor) GetPostIDsByBlog(ctx context.Context, account string, startAuthor, startPermlink string, limit int) ([]int64, error) {
	// Get account ID
	accountRepo := db.NewAccountRepository(db.NewRepository(c.db))
	acc, err := accountRepo.GetByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return []int64{}, nil
	}

	query := c.db.WithContext(ctx).
		Model(&models.FeedCache{}).
		Select("post_id").
		Where("account_id = ?", acc.ID)

	// Handle pagination
	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, startAuthor, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []int64{}, nil
		}

		// Get created_at for start post
		var startCache models.FeedCache
		if err := c.db.WithContext(ctx).
			Where("account_id = ? AND post_id = ?", acc.ID, startID).
			First(&startCache).Error; err == nil {
			query = query.Where("created_at <= ?", startCache.CreatedAt)
		}
	}

	query = query.Order("created_at DESC").Limit(apierrors.ClampLimit(limit))

	var results []struct {
		PostID int64 `gorm:"column:post_id"`
	}
	if err := query.Scan(&results).Error; err != nil {
		return nil, err
	}

	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.PostID
	}

	return ids, nil
}
