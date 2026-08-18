package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/logging"
)

// CachedPost maintains the dirty-post queue and dual-writes to
// hive_posts_cache + hive_posts_cache_temp. Port of
// hive/indexer/cached_post.py (queue, flush, _update_batch, _sql, tag
// deltas, paidout/missing sweeps). Notifications (_notfs) are a hook
// filled in by PR#6d.
type CachedPost struct {
	db    *gorm.DB
	steem steem.Provider
	posts *PostsHelper

	// notifsHook is invoked per processed post when non-nil (PR#6d).
	notifsHook func(ctx context.Context, post map[string]interface{}, pid int64, level string, payout float64)

	logger *zap.Logger

	mu             sync.Mutex
	lastID         int64 // -1 until lazily loaded
	ids            map[string]int64
	noids          map[string]bool
	queue          map[string]int // url -> mode (priority)
	pendingPromted map[int64]float64
	votes          map[string][]string // url -> voters awaiting a vote notif
}

// Dirty levels, in order of decreasing priority (legacy LEVELS).
const (
	levelInsert = iota
	levelPayout
	levelUpdate
	levelUpvote
	levelRecount
)

var levelNames = map[int]string{
	levelInsert:  "insert",
	levelPayout:  "payout",
	levelUpdate:  "update",
	levelUpvote:  "upvote",
	levelRecount: "recount",
}

var levelByName = map[string]int{
	"insert": levelInsert, "payout": levelPayout, "update": levelUpdate,
	"upvote": levelUpvote, "recount": levelRecount,
}

// batchChunk is the steemd get_content batch size (legacy partition_all(1000)).
const batchChunk = 1000

// NewCachedPost creates a CachedPost. The steem provider is required for
// flush (get_content_batch).
func NewCachedPost(database *gorm.DB, provider steem.Provider) *CachedPost {
	return &CachedPost{
		db:             database,
		steem:          provider,
		posts:          NewPostsHelper(database),
		ids:            map[string]int64{},
		noids:          map[string]bool{},
		queue:          map[string]int{},
		pendingPromted: map[int64]float64{},
		votes:          map[string][]string{},
		lastID:         -1,
		logger:         logging.GetLogger().With(zap.String("component", "cached-post")),
	}
}

// SetNotifsHook installs the notification callback (PR#6d).
func (c *CachedPost) SetNotifsHook(fn func(ctx context.Context, post map[string]interface{}, pid int64, level string, payout float64)) {
	c.notifsHook = fn
}

// --- entry points ---

// UpdatePromotedAmount sets a pending promoted value for a post's next write.
func (c *CachedPost) UpdatePromotedAmount(postID int64, amount float64) {
	c.mu.Lock()
	c.pendingPromted[postID] = amount
	c.mu.Unlock()
}

// Insert handles a post created by a comment op.
func (c *CachedPost) Insert(author, permlink string, pid int64) {
	c.dirty(levelInsert, author, permlink, pid)
}

// Update handles a post updated by a comment op.
func (c *CachedPost) Update(author, permlink string, pid int64) {
	c.dirty(levelUpdate, author, permlink, pid)
}

// Vote handles a post dirtied by a vote op; voter queues a vote notif.
func (c *CachedPost) Vote(author, permlink string, pid int64, voter string) {
	c.dirty(levelUpvote, author, permlink, pid)
	if voter != "" {
		c.mu.Lock()
		url := author + "/" + permlink
		c.votes[url] = append(c.votes[url], voter)
		c.mu.Unlock()
	}
}

// Recount forces a child re-count.
func (c *CachedPost) Recount(author, permlink string, pid int64) {
	c.dirty(levelRecount, author, permlink, pid)
}

// Delete removes a post from both cache tables and its tags, and dequeues
// any pending write.
func (c *CachedPost) Delete(ctx context.Context, postID int64, author, permlink string) error {
	for _, table := range []string{"hive_posts_cache", "hive_posts_cache_temp", "hive_post_tags"} {
		if err := c.db.WithContext(ctx).Exec(
			fmt.Sprintf("DELETE FROM %s WHERE post_id = ?", table), postID).Error; err != nil {
			return err
		}
	}
	url := author + "/" + permlink
	c.mu.Lock()
	if _, queued := c.queue[url]; queued {
		delete(c.queue, url)
		delete(c.ids, url)
	}
	c.mu.Unlock()
	c.logger.Warn("deleted post from cache", zap.String("url", url))
	return nil
}

// Undelete handles a reused author/permlink slot: insert a placeholder row
// (so the cache is aware of the id even after a restart) and queue an update.
func (c *CachedPost) Undelete(ctx context.Context, postID int64, author, permlink, category string) error {
	last, err := c.LastID(ctx)
	if err != nil {
		return err
	}
	if postID > last {
		c.Insert(author, permlink, postID)
		return nil
	}
	// Force-create dummy rows in both tables; the 1990 payout_at default
	// makes the row eligible for the next paidout sweep.
	vals := map[string]interface{}{
		"post_id":  postID,
		"author":   author,
		"permlink": permlink,
		"category": category,
	}
	for _, table := range []string{"hive_posts_cache", "hive_posts_cache_temp"} {
		if err := c.db.WithContext(ctx).Table(table).Create(vals).Error; err != nil {
			return err
		}
	}
	c.Update(author, permlink, postID)
	c.logger.Warn("undeleted post", zap.String("url", author+"/"+permlink))
	return nil
}

// PopPendingVoters removes and returns the voters queued for a vote notif.
func (c *CachedPost) PopPendingVoters(url string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	voters := c.votes[url]
	delete(c.votes, url)
	return voters
}

// dirty marks a post at the given level; priority only ever upgrades.
func (c *CachedPost) dirty(level int, author, permlink string, pid int64) {
	url := author + "/" + permlink
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, queued := c.queue[url]; !queued || cur > level {
		c.queue[url] = level
	}
	if pid > 0 {
		if prev, known := c.ids[url]; known && prev != pid {
			c.logger.Error("pid map conflict #78",
				zap.String("url", url),
				zap.Int64("known", prev), zap.Int64("new", pid))
		}
		c.ids[url] = pid
		delete(c.noids, url)
	} else if _, known := c.ids[url]; !known {
		c.noids[url] = true
	}
}

// PendingQueueLen reports the dirty queue size (diagnostics/tests).
func (c *CachedPost) PendingQueueLen() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.queue)
}

// LastID lazily reads MAX(post_id) from hive_posts_cache, then maintains it
// in memory via bumpLastID.
func (c *CachedPost) LastID(ctx context.Context) (int64, error) {
	c.mu.Lock()
	cached := c.lastID
	c.mu.Unlock()
	if cached != -1 {
		return cached, nil
	}
	var id int64
	if err := c.db.WithContext(ctx).
		Table("hive_posts_cache").
		Select("COALESCE(MAX(post_id), 0)").
		Scan(&id).Error; err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.lastID = id
	c.mu.Unlock()
	return id, nil
}

func (c *CachedPost) bumpLastID(ctx context.Context, nextID int64) error {
	last, err := c.LastID(ctx)
	if err != nil || nextID <= last {
		return err
	}
	if gap := nextID - last - 1; gap > 0 {
		if err := c.ensureSafeGap(ctx, last, nextID); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.lastID = nextID
	c.mu.Unlock()
	return nil
}

// ensureSafeGap is the legacy paranoid check: no undeleted posts may exist
// in a cache gap.
func (c *CachedPost) ensureSafeGap(ctx context.Context, lastID, nextID int64) error {
	var missing int64
	if err := c.db.WithContext(ctx).
		Table("hive_posts").
		Select("COUNT(*)").
		Where("id BETWEEN ? AND ? AND is_deleted = '0'", lastID+1, nextID-1).
		Scan(&missing).Error; err != nil {
		return err
	}
	if missing > 0 {
		return fmt.Errorf("found cache gap: %d --> %d (%d missing)", lastID, nextID, missing)
	}
	return nil
}

// --- flush ---

// Flush processes all dirty posts: resolves missing ids, drains the queue by
// level (highest priority first), fetches from steemd in batches, and
// dual-writes. Returns per-level counts.
func (c *CachedPost) Flush(ctx context.Context, trx bool) (map[string]int, error) {
	if _, err := c.loadNoids(ctx); err != nil {
		return nil, err
	}

	counts := map[string]int{}
	var tuples []dirtyTuple

	c.mu.Lock()
	for level := levelInsert; level <= levelRecount; level++ {
		var urls []string
		for url, mode := range c.queue {
			if mode == level {
				if _, ok := c.ids[url]; !ok {
					c.mu.Unlock()
					return nil, fmt.Errorf("missing id for %s", url)
				}
				urls = append(urls, url)
			}
		}
		sort.Strings(urls) // deterministic
		counts[levelNames[level]] = len(urls)
		for _, url := range urls {
			tuples = append(tuples, dirtyTuple{url, c.ids[url], level})
			delete(c.queue, url)
		}
	}
	c.mu.Unlock()

	if err := c.updateBatchTuples(ctx, tuples, trx); err != nil {
		return counts, err
	}

	// Clean id map entries that were not re-queued during processing.
	c.mu.Lock()
	for _, t := range tuples {
		if _, queued := c.queue[t.url]; !queued {
			delete(c.ids, t.url)
		}
	}
	c.mu.Unlock()
	return counts, nil
}

// loadNoids resolves ids for URLs marked dirty without one.
func (c *CachedPost) loadNoids(ctx context.Context) (int, error) {
	c.mu.Lock()
	pending := map[string]bool{}
	for url := range c.noids {
		if _, known := c.ids[url]; !known {
			pending[url] = true
		}
	}
	c.noids = map[string]bool{}
	c.mu.Unlock()
	if len(pending) == 0 {
		return 0, nil
	}
	resolved, err := c.posts.URLsToIDs(ctx, pending)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	for url, id := range resolved {
		c.ids[url] = id
	}
	c.mu.Unlock()
	if len(resolved) != len(pending) {
		for url := range pending {
			if _, ok := resolved[url]; !ok {
				c.logger.Error("missing id for dirty url", zap.String("url", url))
			}
		}
	}
	return len(resolved), nil
}

// DirtyPaidouts marks all posts paid out before date but not yet flagged.
func (c *CachedPost) DirtyPaidouts(ctx context.Context, date string) (int, error) {
	var ids []int64
	if err := c.db.WithContext(ctx).
		Table("hive_posts_cache").
		Select("post_id").
		Where("is_paidout = '0' AND payout_at <= ?", date).
		Scan(&ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	type row struct {
		ID       int64
		Author   string
		Permlink string
	}
	var rows []row
	if err := c.db.WithContext(ctx).
		Table("hive_posts").
		Select("id", "author", "permlink").
		Where("id IN ?", ids).
		Scan(&rows).Error; err != nil {
		return 0, err
	}
	for _, r := range rows {
		c.dirty(levelPayout, r.Author, r.Permlink, r.ID)
	}
	return len(rows), nil
}

// RecoverMissingPosts is the startup routine that cycles through posts
// present in hive_posts but absent from the cache (initial sync, or an
// interrupted fast-sync).
func (c *CachedPost) RecoverMissingPosts(ctx context.Context) error {
	gap, err := c.dirtyMissing(ctx)
	if err != nil {
		return err
	}
	c.logger.Info("missing post cache entries", zap.Int64("gap", gap))
	for {
		counts, err := c.Flush(ctx, true)
		if err != nil {
			return err
		}
		if counts["insert"] == 0 {
			return nil
		}
		lastGap := gap
		if gap, err = c.dirtyMissing(ctx); err != nil {
			return err
		}
		if gap == lastGap {
			c.logger.Warn("ignoring inserts -- may be deleted", zap.Int64("gap", gap))
			return nil
		}
	}
}

func (c *CachedPost) dirtyMissing(ctx context.Context) (int64, error) {
	lastCached, err := c.LastID(ctx)
	if err != nil {
		return 0, err
	}
	lastPost, err := c.posts.LastID(ctx)
	if err != nil {
		return 0, err
	}
	gap := lastPost - lastCached
	if gap <= 0 {
		return 0, nil
	}
	type row struct {
		ID       int64
		Author   string
		Permlink string
		Promoted float64
	}
	var rows []row
	if err := c.db.WithContext(ctx).
		Table("hive_posts").
		Select("id", "author", "permlink", "promoted").
		Where("is_deleted = '0' AND id > ?", lastCached).
		Order("id").
		Limit(1000000).
		Scan(&rows).Error; err != nil {
		return gap, err
	}
	for _, r := range rows {
		if r.Promoted > 0 {
			c.UpdatePromotedAmount(r.ID, r.Promoted)
		}
		c.dirty(levelInsert, r.Author, r.Permlink, r.ID)
	}
	return gap, nil
}

// --- batch processing ---

type cacheQuery struct {
	fn func(tx *gorm.DB) error
}

type dirtyTuple struct {
	url   string
	id    int64
	level int
}

// updateBatchTuples fetches the dirty posts from steemd (in id-sorted
// batches of 1000), merges core hive_posts fields, generates the dual-write
// queries, and executes each batch (optionally in one transaction).
func (c *CachedPost) updateBatchTuples(ctx context.Context, tuples []dirtyTuple, trx bool) error {
	sort.Slice(tuples, func(i, j int) bool { return tuples[i].id < tuples[j].id })

	for start := 0; start < len(tuples); start += batchChunk {
		end := start + batchChunk
		if end > len(tuples) {
			end = len(tuples)
		}
		chunk := tuples[start:end]

		postArgs := make([][]string, len(chunk))
		for i, t := range chunk {
			a, pl, _ := splitURL(t.url)
			postArgs[i] = []string{a, pl}
		}
		posts, err := c.steem.GetContentBatch(ctx, postArgs)
		if err != nil {
			return fmt.Errorf("get_content_batch: %w", err)
		}
		core, err := c.getCoreFields(ctx, chunk)
		if err != nil {
			return err
		}

		var queries []cacheQuery
		for i, t := range chunk {
			post := posts[i]
			if author, _ := post["author"].(string); author != "" {
				// Merge core hive_posts fields into the steemd post
				// (category/gray/hide/community) exactly as legacy does.
				if cf, ok := core[t.id]; ok {
					post["category"] = cf.category
					if cf.communityID != nil {
						post["community_id"] = *cf.communityID
					} else {
						post["community_id"] = nil
					}
					post["gray"] = cf.isMuted
					post["hide"] = !cf.isValid
				}
				qs, err := c.buildSQLs(ctx, t.id, post, t.level)
				if err != nil {
					return err
				}
				queries = append(queries, qs...)
			} else {
				// Blank steemd post: deleted or node lag — re-queue (DEFER).
				var row struct {
					ID        int64
					Author    string
					Permlink  string
					IsDeleted bool
				}
				if err := c.db.WithContext(ctx).
					Table("hive_posts").
					Select("id", "author", "permlink", "is_deleted").
					Where("id = ?", t.id).
					Scan(&row).Error; err != nil {
					return err
				}
				if row.IsDeleted {
					c.logger.Error("found deleted post", zap.Int64("id", t.id), zap.String("level", levelNames[t.level]))
				} else {
					c.logger.Warn("post not found -- DEFER",
						zap.String("level", levelNames[t.level]),
						zap.Int64("id", t.id), zap.String("url", t.url))
					c.dirty(t.level, row.Author, row.Permlink, t.id)
				}
			}
			if err := c.bumpLastID(ctx, t.id); err != nil {
				return err
			}
		}

		if err := c.execQueries(ctx, queries, trx); err != nil {
			return err
		}
		c.logger.Info("dual-write batch complete", zap.Int("posts", len(chunk)))
	}
	return nil
}

func (c *CachedPost) execQueries(ctx context.Context, queries []cacheQuery, trx bool) error {
	if len(queries) == 0 {
		return nil
	}
	if !trx {
		for _, q := range queries {
			if err := q.fn(c.db.WithContext(ctx)); err != nil {
				return err
			}
		}
		return nil
	}
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, q := range queries {
			if err := q.fn(tx); err != nil {
				return err
			}
		}
		return nil
	})
}

type coreFields struct {
	category    string
	communityID *int64
	isMuted     bool
	isValid     bool
}

// getCoreFields loads immutable/authoritative fields from hive_posts.
func (c *CachedPost) getCoreFields(ctx context.Context, tuples []dirtyTuple) (map[int64]coreFields, error) {
	ids := make([]int64, 0, len(tuples))
	for _, t := range tuples {
		ids = append(ids, t.id)
	}
	type row struct {
		ID          int64
		Category    string
		CommunityID *int64
		IsMuted     bool
		IsValid     bool
	}
	var rows []row
	if err := c.db.WithContext(ctx).
		Table("hive_posts").
		Select("id", "category", "community_id", "is_muted", "is_valid").
		Where("id IN ?", ids).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := map[int64]coreFields{}
	for _, r := range rows {
		out[r.ID] = coreFields{r.Category, r.CommunityID, r.IsMuted, r.IsValid}
	}
	return out, nil
}

// buildSQLs generates the dual-write queries for one post (legacy _sql).
func (c *CachedPost) buildSQLs(ctx context.Context, pid int64, post map[string]interface{}, level int) ([]cacheQuery, error) {
	author := getStr(post, "author")
	permlink := getStr(post, "permlink")
	if author == "" {
		return nil, fmt.Errorf("post %d is blank", pid)
	}

	vals := map[string]interface{}{"post_id": pid}

	if level == levelInsert {
		depth, err := numberInt(post["depth"])
		if err != nil {
			return nil, err
		}
		vals["author"] = author
		vals["permlink"] = permlink
		vals["category"] = getStr(post, "category")
		vals["depth"] = depth
	}

	var basic PostBasic
	if level == levelInsert || level == levelPayout || level == levelUpdate {
		basic = ComputePostBasic(post)

		created, err := ParseTime(getStr(post, "created"))
		if err != nil {
			return nil, fmt.Errorf("created: %w", err)
		}
		updated, err := ParseTime(getStr(post, "last_update"))
		if err != nil {
			return nil, fmt.Errorf("last_update: %w", err)
		}
		payoutAt, err := ParseTime(basic.PayoutAt)
		if err != nil {
			return nil, fmt.Errorf("payout_at: %w", err)
		}

		if cid, ok := post["community_id"]; ok && cid != nil {
			switch v := cid.(type) {
			case int64:
				vals["community_id"] = v
			case float64:
				vals["community_id"] = int64(v)
			}
		}
		vals["created_at"] = created
		vals["updated_at"] = updated
		vals["title"] = getStr(post, "title")
		vals["payout_at"] = payoutAt
		vals["preview"] = basic.Preview
		vals["body"] = basic.Body
		vals["img_url"] = basic.Image
		vals["is_nsfw"] = basic.IsNSFW
		vals["is_declined"] = basic.IsPayoutDeclined
		vals["is_full_power"] = basic.IsFullPower
		vals["is_paidout"] = basic.IsPaidout
		vals["json"], _ = json.Marshal(basic.JSONMetadata)
		vals["raw_json"], _ = json.Marshal(LegacyPostFields(post))
	}

	// Pull out any pending promoted amount.
	c.mu.Lock()
	if bal, ok := c.pendingPromted[pid]; ok {
		delete(c.pendingPromted, pid)
		vals["promoted"] = bal
	}
	c.mu.Unlock()

	payout, err := ComputePostPayout(post)
	if err != nil {
		return nil, err
	}
	stats, err := ComputePostStats(post)
	if err != nil {
		return nil, err
	}

	// Community posts override the gray/hide display flags with the
	// authoritative hive_posts columns.
	if cid := post["community_id"]; cid != nil {
		gray, _ := post["gray"].(bool)
		hide, _ := post["hide"].(bool)
		stats.Gray = gray
		stats.Hide = hide
	}

	children, err := numberInt(post["children"])
	if err != nil {
		return nil, err
	}
	if children > 32767 {
		children = 32767
	}

	vals["payout"] = payout.Payout
	vals["rshares"] = payout.RShares
	vals["votes"] = payout.CSVotes
	vals["sc_trend"] = payout.SCTrend
	vals["sc_hot"] = payout.SCHot
	vals["flag_weight"] = stats.FlagWeight
	vals["total_votes"] = stats.TotalVotes
	vals["up_votes"] = stats.UpVotes
	vals["is_hidden"] = stats.Hide
	vals["is_grayed"] = stats.Gray
	vals["author_rep"] = stats.AuthorRep
	vals["children"] = children

	var queries []cacheQuery

	// Dual-write insert or update for main + temp.
	for _, table := range []string{"hive_posts_cache", "hive_posts_cache_temp"} {
		tbl := table
		if level == levelInsert {
			v := vals
			queries = append(queries, cacheQuery{fn: func(tx *gorm.DB) error {
				return tx.Table(tbl).Create(v).Error
			}})
		} else {
			v := vals
			queries = append(queries, cacheQuery{fn: func(tx *gorm.DB) error {
				return tx.Table(tbl).Where("post_id = ?", pid).Updates(v).Error
			}})
		}
	}

	// Tag deltas for root posts on insert/update.
	if level == levelInsert || level == levelUpdate {
		if depth, _ := numberInt(post["depth"]); depth == 0 {
			tagQs, err := c.tagSQLs(ctx, pid, basic.Tags, level != levelInsert)
			if err != nil {
				return nil, err
			}
			queries = append(queries, tagQs...)
		}
	}

	// Recounting a comment re-queues its parent for the next pass.
	if level == levelRecount {
		if depth, _ := numberInt(post["depth"]); depth > 0 {
			c.Recount(getStr(post, "parent_author"), getStr(post, "parent_permlink"), 0)
		}
	}

	// Notification hook (PR#6d).
	if c.notifsHook != nil {
		c.notifsHook(ctx, post, pid, levelNames[level], payout.Payout)
	}

	return queries, nil
}

// tagSQLs generates tag deltas (delete removed + insert added with
// ON CONFLICT DO NOTHING — collation can produce duplicate conflicts).
func (c *CachedPost) tagSQLs(ctx context.Context, pid int64, tags []string, diff bool) ([]cacheQuery, error) {
	next := map[string]bool{}
	for _, t := range tags {
		next[t] = true
	}
	curr := map[string]bool{}
	if diff {
		var existing []string
		if err := c.db.WithContext(ctx).
			Table("hive_post_tags").
			Select("tag").
			Where("post_id = ?", pid).
			Scan(&existing).Error; err != nil {
			return nil, err
		}
		for _, t := range existing {
			curr[t] = true
		}
	}

	var queries []cacheQuery
	var toRemove []string
	for t := range curr {
		if !next[t] {
			toRemove = append(toRemove, t)
		}
	}
	if len(toRemove) > 0 {
		sort.Strings(toRemove)
		queries = append(queries, cacheQuery{fn: func(tx *gorm.DB) error {
			return tx.Exec("DELETE FROM hive_post_tags WHERE post_id = ? AND tag IN ?",
				pid, toRemove).Error
		}})
	}

	var toAdd []string
	for t := range next {
		if !curr[t] {
			toAdd = append(toAdd, t)
		}
	}
	if len(toAdd) > 0 {
		sort.Strings(toAdd)
		values := make([]string, 0, len(toAdd))
		args := make([]interface{}, 0, len(toAdd)*2)
		for _, t := range toAdd {
			values = append(values, "(?, ?)")
			args = append(args, pid, t)
		}
		sql := "INSERT INTO hive_post_tags (post_id, tag) VALUES " +
			strings.Join(values, ", ") + " ON CONFLICT DO NOTHING"
		queries = append(queries, cacheQuery{fn: func(tx *gorm.DB) error {
			return tx.Exec(sql, args...).Error
		}})
	}
	return queries, nil
}
