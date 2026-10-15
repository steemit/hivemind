package condenser

import (
	"context"
	"fmt"
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

// GetPostIDsByQuery gets post IDs for a given query. This is the
// condenser_api flavored ranked query (legacy condenser cursor.pids_by_query,
// serving get_discussions_by_* and the payout discussion methods). The bridge
// flavored variant with toponly/tiebreaker/pin semantics is GetRankedPostIDs.
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

	// Filter by tag if provided. Payout sorts filter on the post's own
	// category directly instead of the tag index (legacy condenser
	// cursor.py:304-307); community tags additionally restrict trending/hot
	// to top-level posts.
	if tag != "" {
		if sort == "payout" || sort == "payout_comments" {
			query = query.Where("category = ?", tag)
		} else if strings.HasPrefix(tag, "hive-") {
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

	// Handle pagination. Legacy condenser cursor.py:319 seeks on the sort
	// field itself with an inclusive `field <= (SELECT field ... WHERE
	// post_id = :start_id)`. A missing cache row for the start post yields
	// NULL and thus no rows (the start post is outside the indexed data).
	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, startAuthor, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []int64{}, nil
		}

		field := "payout"
		switch sort {
		case "trending":
			field = "sc_trend"
		case "hot":
			field = "sc_hot"
		case "created":
			field = "post_id"
		case "promoted":
			field = "promoted"
		}
		query = query.Where(
			fmt.Sprintf("hive_posts_cache.%s <= (SELECT %s FROM hive_posts_cache WHERE post_id = ?)", field, field),
			startID,
		)
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

// Ranked-post constants mirroring legacy hive/server/bridge_api/cursor.py.
const (
	// rankedDefaultCommunityID is legacy DEFAULT_CID: the community whose
	// pinned posts are prepended to first pages of trending/created when no
	// specific community was requested.
	rankedDefaultCommunityID int64 = 1317453
	// rankedExcludedCommunityID is the pseudo-community excluded from the
	// tag='all' base filter (legacy hardcodes it in pids_by_community).
	rankedExcludedCommunityID int64 = 1337319
	// rankedPayoutWindow mirrors legacy PAYOUT_WINDOW: payout sorts only
	// consider posts whose payout falls between now+12h and now+36h.
	rankedPayoutWindow = "payout_at BETWEEN now() + interval '12 hours' AND now() + interval '36 hours'"
)

// rankedCacheTable mirrors legacy CacheRouter.get_table: hot sorts read the
// rolling 90-day window table (hive_posts_cache_temp), everything else (the
// muted sort) reads the main table.
func rankedCacheTable(sort string) string {
	switch sort {
	case "trending", "hot", "payout", "payout_comments", "created", "promoted":
		return "hive_posts_cache_temp"
	default:
		return "hive_posts_cache"
	}
}

// authorHiddenClause filters out posts whose author is user-blocked
// (hive_posts_status list_type=3). Mirrors the NOT EXISTS author filter of
// legacy pids_by_category / pids_by_community.
func authorHiddenClause(table string) string {
	return fmt.Sprintf(`NOT EXISTS (
	           SELECT 1 FROM hive_posts_status s
	            WHERE s.list_type = %d AND s.author = %s.author
	       )`, models.PostStatusUserBlock, table)
}

// scanPostIDs runs a raw single-column query and scans it into an int64
// slice.
func scanPostIDs(tx *gorm.DB) ([]int64, error) {
	var ids []int64
	if err := tx.Scan(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// GetRankedPostIDs returns ranked post IDs for bridge.get_ranked_posts.
// It mirrors legacy bridge cursor.pids_by_ranked: community dispatch for
// tag='my'/'all'/'hive-*' (vs category/tag mode otherwise), then first-page
// pinned prepends for trending/created.
func (c *Cursor) GetRankedPostIDs(ctx context.Context, sort, startAuthor, startPermlink string, limit int, tag string) ([]int64, error) {
	// Community dispatch. communityMode=false means category/tag mode.
	var cids []int64
	communityMode := false
	single := int64(0)
	switch {
	case tag == "my":
		// Legacy _subscribed(observer_id): without an observer the
		// subscription set is empty and pids_by_ranked returns []. Observer
		// context (tag='my' subscribed communities) is planned PR#417.
		return []int64{}, nil
	case tag == "all":
		cids = []int64{}
		communityMode = true
	case strings.HasPrefix(tag, "hive-"):
		cid, err := c.getCommunityIDByName(ctx, tag)
		if err != nil {
			return nil, err
		}
		if cid != 0 {
			cids = []int64{cid}
			communityMode = true
			single = cid
		}
	}
	if communityMode {
		tag = ""
	}

	startID := int64(0)
	if startPermlink != "" {
		id, err := c.GetPostIDByAuthorPermlink(ctx, startAuthor, startPermlink)
		if err != nil {
			return nil, err
		}
		if id == 0 {
			return []int64{}, nil
		}
		startID = id
	}

	var (
		pids []int64
		err  error
	)
	if communityMode {
		pids, err = c.rankedPidsByCommunity(ctx, cids, sort, startID, limit)
	} else {
		pids, err = c.rankedPidsByCategory(ctx, tag, sort, startID, limit)
	}
	if err != nil {
		return nil, err
	}

	// First page of trending/created: prepend pinned posts (community pins,
	// then admin pins). Mirrors the pids_by_ranked tail.
	if tag == "" && startID == 0 && (sort == "trending" || sort == "created") {
		pinnedCid := single
		if pinnedCid == 0 {
			pinnedCid = rankedDefaultCommunityID
		}
		prepend, err := c.pinnedPostIDs(ctx, pinnedCid)
		if err != nil {
			return nil, err
		}
		pids = prependRankedIDs(pids, prepend)

		// Legacy tests `not cids` here, which is falsy for both None and
		// [], so the admin-pin prepend also applies to tag='all'.
		if len(cids) == 0 {
			prepend, err = c.pinnedByStatusType(ctx, models.PostStatusArticlePin)
			if err != nil {
				return nil, err
			}
			pids = prependRankedIDs(pids, prepend)
		}
	}
	return pids, nil
}

// rankedPidsByCategory mirrors legacy bridge cursor.pids_by_category (used
// when the tag is blank or a plain tag).
func (c *Cursor) rankedPidsByCategory(ctx context.Context, tag, sort string, seekID int64, limit int) ([]int64, error) {
	//                   field       pending posts  comment promoted
	type categoryDef struct {
		field                             string
		pending, posts, comment, promoted bool
	}
	defs := map[string]categoryDef{
		"trending":        {"sc_trend", true, true, false, false},
		"hot":             {"sc_hot", true, true, false, false},
		"created":         {"post_id", false, true, false, false},
		"promoted":        {"promoted", true, false, false, true},
		"payout":          {"payout", true, false, false, false},
		"payout_comments": {"payout", true, false, true, false},
		"muted":           {"payout", true, false, false, false},
	}
	d, ok := defs[sort]
	if !ok {
		return nil, apierrors.Publicf("invalid sort type: %s", sort)
	}

	table := rankedCacheTable(sort)
	var where []string
	var args []interface{}
	if d.pending {
		where = append(where, "is_paidout = '0'")
	}
	if d.posts {
		where = append(where, "depth = 0")
	}
	if d.comment {
		where = append(where, "depth > 0")
	}
	if d.promoted {
		where = append(where, "promoted > 0")
	}
	if sort == "muted" {
		where = append(where, "is_grayed = '1' AND payout > 0")
	}
	if sort == "payout" {
		where = append(where, rankedPayoutWindow)
	}
	if tag != "" {
		if sort == "payout" || sort == "payout_comments" {
			// Payout sorts filter on the post's own category rather than
			// the tag index.
			where = append(where, "category = ?")
		} else {
			where = append(where, "post_id IN (SELECT post_id FROM hive_post_tags WHERE tag = ?)")
		}
		args = append(args, tag)
	}
	if seekID != 0 {
		// Two-tuple seek (legacy bridge cursor.py:156-157): strictly below
		// the seek value, or tied with it and a higher post_id. The single
		// inclusive `field <= seek` comparison loses/duplicates posts when
		// several posts share the same sort value across a page boundary.
		sval := fmt.Sprintf("(SELECT %s FROM %s WHERE post_id = ?)", d.field, table)
		where = append(where, fmt.Sprintf("(%s < %s OR (%s = %s AND post_id > ?))", d.field, sval, d.field, sval))
		args = append(args, seekID, seekID, seekID)
	}
	where = append(where, authorHiddenClause(table))
	args = append(args, apierrors.ClampLimit(limit))

	sql := fmt.Sprintf("SELECT post_id FROM %s WHERE %s ORDER BY %s DESC, post_id LIMIT ?",
		table, strings.Join(where, " AND "), d.field)
	return scanPostIDs(c.db.WithContext(ctx).Raw(sql, args...))
}

// rankedPidsByCommunity mirrors legacy bridge cursor.pids_by_community (used
// for tag='my'/'all'/'hive-*'). Differences from the category variant: every
// sort filters is_grayed, trending/hot/created/promoted are toponly, promoted
// and payout/muted are pending-only, and 'created' orders by created_at.
func (c *Cursor) rankedPidsByCommunity(ctx context.Context, cids []int64, sort string, seekID int64, limit int) ([]int64, error) {
	//                   field        pending toponly gray   promoted
	type communityDef struct {
		field                            string
		pending, toponly, gray, promoted bool
	}
	defs := map[string]communityDef{
		"trending": {"sc_trend", false, true, false, false},
		"hot":      {"sc_hot", false, true, false, false},
		"created":  {"created_at", false, true, false, false},
		"promoted": {"promoted", true, true, false, true},
		"payout":   {"payout", true, false, false, false},
		"muted":    {"payout", true, false, true, false},
	}
	d, ok := defs[sort]
	if !ok {
		// payout_comments has no community definition in legacy (asserts).
		return nil, fmt.Errorf("sort %q is not valid in community mode", sort)
	}

	table := rankedCacheTable(sort)
	var where []string
	var args []interface{}
	if len(cids) > 0 {
		// Performance window (legacy): specific communities only see recent
		// posts (7 days; 30 days for created).
		cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour)
		if sort == "created" {
			cutoff = time.Now().UTC().Add(-30 * 24 * time.Hour)
		}
		where = append(where, "community_id IN ? AND created_at >= ?")
		args = append(args, cids, cutoff)
	} else {
		// tag='all': every community post except the excluded pseudo one.
		where = append(where, "community_id IS NOT NULL AND community_id != ?")
		args = append(args, rankedExcludedCommunityID)
	}
	if d.gray {
		where = append(where, "is_grayed = '1'")
	} else {
		where = append(where, "is_grayed = '0'")
	}
	if d.toponly {
		where = append(where, "depth = 0")
	}
	if d.pending {
		where = append(where, "is_paidout = '0'")
	}
	if d.promoted {
		where = append(where, "promoted > 0")
	}
	if sort == "payout" {
		where = append(where, rankedPayoutWindow)
	}
	if seekID != 0 {
		sval := fmt.Sprintf("(SELECT %s FROM %s WHERE post_id = ?)", d.field, table)
		where = append(where, fmt.Sprintf("(%s < %s OR (%s = %s AND post_id > ?))", d.field, sval, d.field, sval))
		args = append(args, seekID, seekID, seekID)
	}
	where = append(where, authorHiddenClause(table))
	args = append(args, apierrors.ClampLimit(limit))

	sql := fmt.Sprintf("SELECT post_id FROM %s WHERE %s ORDER BY %s DESC, post_id LIMIT ?",
		table, strings.Join(where, " AND "), d.field)
	return scanPostIDs(c.db.WithContext(ctx).Raw(sql, args...))
}

// getCommunityIDByName resolves a community name to its hive_communities.id.
// Returns 0 when the community does not exist (legacy _get_community_id
// returns None and the caller falls back to category mode).
func (c *Cursor) getCommunityIDByName(ctx context.Context, name string) (int64, error) {
	var comm models.Community
	err := c.db.WithContext(ctx).
		Where("name = ?", name).
		Select("id").
		First(&comm).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, nil
		}
		return 0, err
	}
	return comm.ID, nil
}

// pinnedPostIDs returns ids of pinned, undeleted posts in a community
// (legacy bridge cursor._pinned).
func (c *Cursor) pinnedPostIDs(ctx context.Context, communityID int64) ([]int64, error) {
	sql := `SELECT id FROM hive_posts
	         WHERE is_pinned = '1'
	           AND is_deleted = '0'
	           AND community_id = ?
	     ORDER BY id DESC`
	return scanPostIDs(c.db.WithContext(ctx).Raw(sql, communityID))
}

// pinnedByStatusType returns post ids from hive_posts_status entries of the
// given list_type, newest first (legacy bridge cursor._pids_by_type). Used
// for the admin-pin (list_type=2) first-page prepend.
func (c *Cursor) pinnedByStatusType(ctx context.Context, listType int16) ([]int64, error) {
	sql := `SELECT post_id FROM hive_posts_status
	         WHERE list_type = ?
	     ORDER BY created_at DESC`
	return scanPostIDs(c.db.WithContext(ctx).Raw(sql, listType))
}

// prependRankedIDs moves any ids already present in `prepend` out of `pids`,
// then returns prepend + pids (the legacy pids_by_ranked prepend loop).
func prependRankedIDs(pids, prepend []int64) []int64 {
	if len(prepend) == 0 {
		return pids
	}
	skip := make(map[int64]bool, len(prepend))
	for _, pid := range prepend {
		skip[pid] = true
	}
	out := make([]int64, 0, len(pids)+len(prepend))
	out = append(out, prepend...)
	for _, pid := range pids {
		if !skip[pid] {
			out = append(out, pid)
		}
	}
	return out
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
		// Rebind the subquery's parent account to the seek post's parent
		// author (legacy bridge/condenser cursor pids_by_replies*:
		// `parent_account = row[0]`). The second page must keep querying
		// replies under the same parent account; keeping startAuthor (the
		// reply's own author) queries the wrong parent set from page 2 on.
		parentAccount = row.ParentAuthor
		args[0] = parentAccount
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

// FollowRow is one follower/following entry from the joined follow query,
// mirroring the legacy condenser cursor SELECT list (ha.name, ha.reputation,
// hf.state) — see cursor.py get_followers/get_following.
type FollowRow struct {
	Name       string  `gorm:"column:name"`
	Reputation float64 `gorm:"column:reputation"`
	State      int16   `gorm:"column:state"`
}

// followStateClause returns the hardcoded state filter for a follow type.
// Legacy hardcodes IN (2,3) for 'ignore' and IN (1,3) for everything else so
// the blog branch matches the partial index (condenser cursor.py).
func followStateClause(followType string) string {
	if followType == "ignore" {
		return "AND hf.state IN (2, 3)"
	}
	return "AND hf.state IN (1, 3)"
}

// GetFollowers returns followers of `account` (name, reputation, state),
// paged by the created_at DESC cursor. Mirrors legacy condenser
// cursor.get_followers: `start` seeks with created_at <= the start row's
// created_at, so the start entry itself is included in the page.
func (c *Cursor) GetFollowers(ctx context.Context, account, start, followType string, limit int) ([]FollowRow, error) {
	accountID, err := c.getAccountIDByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	sql := `
	    SELECT ha.name, ha.reputation, hf.state
	      FROM hive_follows hf
	     INNER JOIN hive_accounts ha ON hf.follower = ha.id
	     WHERE hf.following = ?
	       ` + followStateClause(followType)
	args := []interface{}{accountID}

	if start != "" {
		startID, err := c.getAccountIDByName(ctx, start)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return nil, apierrors.Publicf("account not found: `%s`", start)
		}
		sql += ` AND hf.created_at <= (
		             SELECT created_at FROM hive_follows
		              WHERE following = ? AND follower = ?)`
		args = append(args, accountID, startID)
	}
	sql += " ORDER BY hf.created_at DESC LIMIT ?"
	args = append(args, apierrors.ClampLimit(limit))

	var rows []FollowRow
	if err := c.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetFollowing returns accounts followed by `account` (name, reputation,
// state), paged by the created_at DESC cursor. Mirrors legacy condenser
// cursor.get_following (inclusive start seek, see GetFollowers).
func (c *Cursor) GetFollowing(ctx context.Context, account, start, followType string, limit int) ([]FollowRow, error) {
	accountID, err := c.getAccountIDByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	sql := `
	    SELECT ha.name, ha.reputation, hf.state
	      FROM hive_follows hf
	     INNER JOIN hive_accounts ha ON hf.following = ha.id
	     WHERE hf.follower = ?
	       ` + followStateClause(followType)
	args := []interface{}{accountID}

	if start != "" {
		startID, err := c.getAccountIDByName(ctx, start)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return nil, apierrors.Publicf("account not found: `%s`", start)
		}
		sql += ` AND hf.created_at <= (
		             SELECT created_at FROM hive_follows
		              WHERE follower = ? AND following = ?)`
		args = append(args, accountID, startID)
	}
	sql += " ORDER BY hf.created_at DESC LIMIT ?"
	args = append(args, apierrors.ClampLimit(limit))

	var rows []FollowRow
	if err := c.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetFollowersByPage returns followers of `account` (name, reputation,
// state) by offset pagination, ordered created_at DESC. Mirrors legacy
// condenser cursor.get_followers_by_page.
func (c *Cursor) GetFollowersByPage(ctx context.Context, account string, page, pageSize int, followType string) ([]FollowRow, error) {
	accountID, err := c.getAccountIDByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	sql := `
	    SELECT ha.name, ha.reputation, hf.state
	      FROM hive_follows hf
	     INNER JOIN hive_accounts ha ON hf.follower = ha.id
	     WHERE hf.following = ?
	       ` + followStateClause(followType) + `
	    ORDER BY hf.created_at DESC
	    LIMIT ? OFFSET ?`
	args := []interface{}{accountID, apierrors.ClampLimit(pageSize), page * pageSize}

	var rows []FollowRow
	if err := c.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetFollowingByPage returns accounts followed by `account` (name,
// reputation, state) by offset pagination, ordered created_at DESC. Mirrors
// legacy condenser cursor.get_following_by_page.
func (c *Cursor) GetFollowingByPage(ctx context.Context, account string, page, pageSize int, followType string) ([]FollowRow, error) {
	accountID, err := c.getAccountIDByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	sql := `
	    SELECT ha.name, ha.reputation, hf.state
	      FROM hive_follows hf
	     INNER JOIN hive_accounts ha ON hf.following = ha.id
	     WHERE hf.follower = ?
	       ` + followStateClause(followType) + `
	    ORDER BY hf.created_at DESC
	    LIMIT ? OFFSET ?`
	args := []interface{}{accountID, apierrors.ClampLimit(pageSize), page * pageSize}

	var rows []FollowRow
	if err := c.db.WithContext(ctx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
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

// GetPostIDsByBlog gets post IDs for an account's blog — bridge flavor.
// Compared to the plain listing it skips the account's own community posts
// that were not reblogged (legacy bridge cursor.pids_by_blog
// skip_condition). Legacy serves bridge.get_account_posts(sort=blog) and
// hive_api.list_account_blog from this variant.
func (c *Cursor) GetPostIDsByBlog(ctx context.Context, account string, startAuthor, startPermlink string, limit int) ([]int64, error) {
	return c.getPostIDsByBlog(ctx, account, startAuthor, startPermlink, limit, true)
}

// GetPostIDsByBlogCondenser is the condenser flavor of the blog query: no
// unreblogged-community-post skip (legacy condenser cursor.pids_by_blog).
func (c *Cursor) GetPostIDsByBlogCondenser(ctx context.Context, account string, startAuthor, startPermlink string, limit int) ([]int64, error) {
	return c.getPostIDsByBlog(ctx, account, startAuthor, startPermlink, limit, false)
}

// getPostIDsByBlog pages an account's blog (own posts + reblogs) from
// hive_feed_cache, newest first.
func (c *Cursor) getPostIDsByBlog(ctx context.Context, account string, startAuthor, startPermlink string, limit int, skipUnrebloggedCommunity bool) ([]int64, error) {
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

	// Ignore community posts which were not reblogged (legacy bridge
	// pids_by_blog skip_condition): a feed entry written for the account's
	// own top-level community post is only kept if the account also reblogged
	// it (hive_reblogs row).
	if skipUnrebloggedCommunity {
		query = query.Where(`NOT EXISTS (
			SELECT 1 FROM hive_posts p
			 WHERE p.id = hive_feed_cache.post_id
			   AND p.author = ?
			   AND p.is_deleted = '0'
			   AND p.depth = 0
			   AND p.community_id IS NOT NULL
			   AND NOT EXISTS (
			       SELECT 1 FROM hive_reblogs r
			        WHERE r.post_id = p.id AND r.account = ?))`, account, account)
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
