package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/api/condenser"
	"github.com/steemit/hivemind/internal/api/objects"
	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/cache"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// RankedAPI provides ranked posts API methods
type RankedAPI struct {
	repo                 *db.Repository
	cursor               *condenser.Cursor
	cache                *cache.Cache
	loader               *objects.PostLoader
	recommendCommunities []string
}

// NewRankedAPI creates a new ranked API. recommendCommunities is the
// comma-separated HIVE_RECOMMEND_COMMUNITIES config used by
// bridge.get_trending_topics.
func NewRankedAPI(repo *db.Repository, database *db.DB, redisCache *cache.Cache, recommendCommunities string) *RankedAPI {
	var recommended []string
	for _, name := range strings.Split(recommendCommunities, ",") {
		if name = strings.TrimSpace(name); name != "" {
			recommended = append(recommended, name)
		}
	}
	return &RankedAPI{
		repo:                 repo,
		cursor:               condenser.NewCursor(database.DB),
		cache:                redisCache,
		loader:               objects.NewPostLoader(database.DB),
		recommendCommunities: recommended,
	}
}

// GetRankedPosts handles bridge.get_ranked_posts
// Query posts, sorted by the given method; returns full bridge post objects.
func (r *RankedAPI) GetRankedPosts(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_ranked_posts")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	sort, _ := pMap["sort"].(string)
	if sort == "" {
		return nil, apierrors.PublicError("missing required parameter: sort")
	}

	startAuthor := ""
	if sa, ok := pMap["start_author"].(string); ok {
		startAuthor = sa
	}
	startPermlink := ""
	if sp, ok := pMap["start_permlink"].(string); ok {
		startPermlink = sp
	}
	limit := 20
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}
	tag := ""
	if t, ok := pMap["tag"].(string); ok {
		tag = t
	}
	observer, _ := pMap["observer"].(string)
	_ = observer // TODO: observer context (tag='my' subscribed communities)

	// Generate cache key using hash to shorten long keys
	cacheKeyParts := []string{
		"bridge_get_ranked_posts",
		sort,
		startAuthor,
		startPermlink,
		fmt.Sprintf("%d", limit),
		tag,
	}
	cacheKey := cache.HashKey(cacheKeyParts...)

	// Check cache
	if r.cache != nil {
		var cachedResult []interface{}
		if err := r.cache.GetJSON(cacheKey, &cachedResult); err == nil {
			return cachedResult, nil
		}
	}

	// Map sort types
	sortMap := map[string]string{
		"trending":        "trending",
		"hot":             "hot",
		"created":         "created",
		"promoted":        "promoted",
		"payout":          "payout",
		"payout_comments": "payout_comments",
		"muted":           "muted",
	}

	querySort, ok := sortMap[sort]
	if !ok {
		return nil, apierrors.Publicf("invalid sort type: %s", sort)
	}

	// Get post IDs
	ids, err := r.cursor.GetPostIDsByQuery(ctx.Request.Context(), querySort, startAuthor, startPermlink, limit, tag)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	// Filter article-blocked posts (legacy hide_pids_by_ids).
	ids, err = r.filterHidden(ctx.Request.Context(), ids)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	// Load full bridge post objects
	result, err := r.loader.LoadPostsBridge(ctx.Request.Context(), ids, 0)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	// Cache result
	if r.cache != nil {
		ttl := r.getCacheTTL(sort)
		if err := r.cache.SetJSON(cacheKey, result, ttl); err != nil {
			// Log error but don't fail the request
			// TODO: Add logging
			_ = err
		}
	}

	telemetry.SetSpanSuccess(span)
	return result, nil
}

// filterHidden removes article-blocked posts from an id list.
func (r *RankedAPI) filterHidden(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return ids, nil
	}
	statusRepo := db.NewPostStatusRepository(r.repo)
	hidden, err := statusRepo.GetHiddenPostIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	if len(hidden) == 0 {
		return ids, nil
	}
	filtered := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !hidden[id] {
			filtered = append(filtered, id)
		}
	}
	return filtered, nil
}

// getCacheTTL returns cache TTL based on sort type
func (r *RankedAPI) getCacheTTL(sort string) time.Duration {
	switch sort {
	case "created":
		return 3 * time.Second
	case "trending", "hot":
		return 300 * time.Second
	case "payout", "payout_comments":
		return 30 * time.Second
	case "muted":
		return 600 * time.Second
	default:
		return 60 * time.Second
	}
}

// GetAccountPosts handles bridge.get_account_posts
// Posts for an account: blog, feed, posts, comments, replies, or payout.
func (r *RankedAPI) GetAccountPosts(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_account_posts")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	sort, _ := pMap["sort"].(string)
	if sort == "" {
		return nil, apierrors.PublicError("missing required parameter: sort")
	}
	validSorts := map[string]bool{
		"blog": true, "feed": true, "posts": true,
		"comments": true, "replies": true, "payout": true,
	}
	if !validSorts[sort] {
		return nil, apierrors.Publicf("invalid sort type: %s", sort)
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}
	account, err := apierrors.ValidAccount(account, false)
	if err != nil {
		return nil, err
	}

	startAuthor := ""
	if sa, ok := pMap["start_author"].(string); ok {
		startAuthor = sa
	}
	startPermlink := ""
	if sp, ok := pMap["start_permlink"].(string); ok {
		startPermlink = sp
	}
	limit := 20
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}

	// Author-blocked accounts (list_type=3) return nothing.
	statusRepo := db.NewPostStatusRepository(r.repo)
	authorHidden, err := statusRepo.IsAuthorHidden(ctx.Request.Context(), account)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if authorHidden {
		return []map[string]interface{}{}, nil
	}

	// For self-authored sorts, the seek post must belong to the account;
	// without a seek permlink the account itself is the start (legacy
	// normalizes start to (account, None) for these sorts).
	if startPermlink == "" {
		switch sort {
		case "posts", "comments", "replies", "payout":
			startAuthor = account
		}
	}
	if (sort == "posts" || sort == "comments") && startAuthor != account {
		return nil, apierrors.PublicError("account must match start author")
	}

	var result []map[string]interface{}
	reqCtx := ctx.Request.Context()
	switch sort {
	case "blog":
		ids, err := r.cursor.GetPostIDsByBlog(reqCtx, account, startAuthor, startPermlink, limit)
		if err != nil {
			return nil, err
		}
		ids, err = r.filterHidden(reqCtx, ids)
		if err != nil {
			return nil, err
		}
		result, err = r.loader.LoadPostsBridge(reqCtx, ids, 0)
		if err != nil {
			return nil, err
		}
		// Reblogs surface the account itself as reblogger.
		for _, post := range result {
			if author, _ := post["author"].(string); author != account {
				post["reblogged_by"] = []string{account}
			}
		}
	case "feed":
		entries, err := r.cursor.GetPostIDsByFeedWithReblog(reqCtx, account, startAuthor, startPermlink, limit)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, 0, len(entries))
		rebloggers := make(map[int64]string, len(entries))
		for _, e := range entries {
			ids = append(ids, e.PostID)
			rebloggers[e.PostID] = e.Accounts
		}
		result, err = r.loader.LoadPostsReblogsBridge(reqCtx, ids, rebloggers, 0)
		if err != nil {
			return nil, err
		}
	case "posts":
		ids, err := r.cursor.GetPostIDsByAccountPosts(reqCtx, account, startPermlink, limit)
		if err != nil {
			return nil, err
		}
		ids, err = r.filterHidden(reqCtx, ids)
		if err != nil {
			return nil, err
		}
		result, err = r.loader.LoadPostsBridge(reqCtx, ids, 0)
		if err != nil {
			return nil, err
		}
	case "comments":
		ids, err := r.cursor.GetPostIDsByAccountComments(reqCtx, account, startPermlink, limit)
		if err != nil {
			return nil, err
		}
		result, err = r.loader.LoadPostsBridge(reqCtx, ids, 0)
		if err != nil {
			return nil, err
		}
	case "replies":
		ids, err := r.cursor.GetPostIDsByRepliesToAccount(reqCtx, startAuthor, startPermlink, limit)
		if err != nil {
			return nil, err
		}
		result, err = r.loader.LoadPostsBridge(reqCtx, ids, 0)
		if err != nil {
			return nil, err
		}
	case "payout":
		ids, err := r.cursor.GetPostIDsByPayout(reqCtx, account, startAuthor, startPermlink, limit)
		if err != nil {
			return nil, err
		}
		ids, err = r.filterHidden(reqCtx, ids)
		if err != nil {
			return nil, err
		}
		result, err = r.loader.LoadPostsBridge(reqCtx, ids, 0)
		if err != nil {
			return nil, err
		}
	}

	telemetry.SetSpanSuccess(span)
	return result, nil
}

// GetTrendingTopics handles bridge.get_trending_topics
// Top communities first, then a fixed set of common tags.
func (r *RankedAPI) GetTrendingTopics(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		pMap = map[string]interface{}{}
	}
	limit := 10
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 25 {
			limit = 25
		}
	}

	out := []interface{}{}

	// Recommended communities (config), then by rank.
	var rows []models.Community
	query := r.repo.DB().WithContext(ctx.Request.Context()).
		Model(&models.Community{}).
		Select("name", "title").
		Where("rank > 0").
		Order("rank")
	if len(r.recommendCommunities) > 0 {
		var recommended []models.Community
		if err := r.repo.DB().WithContext(ctx.Request.Context()).
			Model(&models.Community{}).
			Select("name", "title").
			Where("name IN ?", r.recommendCommunities).
			Find(&recommended).Error; err != nil {
			return nil, err
		}
		for _, comm := range recommended {
			title := comm.Title
			if title == "" {
				title = comm.Name
			}
			out = append(out, []interface{}{comm.Name, title})
		}
		if len(out) < limit {
			query = query.Where("name NOT IN ?", r.recommendCommunities).Limit(limit - len(out))
		} else {
			query = query.Limit(0)
		}
	} else {
		query = query.Limit(limit)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, comm := range rows {
		title := comm.Title
		if title == "" {
			title = comm.Name
		}
		out = append(out, []interface{}{comm.Name, title})
	}

	// Fill the remainder with common tags (mirrors legacy fixed list).
	for _, tag := range []string{"photography", "travel", "gaming", "crypto", "newsteem", "music", "food"} {
		if len(out) >= limit {
			break
		}
		out = append(out, []interface{}{tag, "#" + tag})
	}

	return out, nil
}
