package bridge

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/cache"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/api/condenser"
)

// RankedAPI provides ranked posts API methods
type RankedAPI struct {
	repo   *db.Repository
	cursor *condenser.Cursor
	cache  *cache.Cache
}

// NewRankedAPI creates a new ranked API
func NewRankedAPI(repo *db.Repository, database *db.DB, redisCache *cache.Cache) *RankedAPI {
	return &RankedAPI{
		repo:   repo,
		cursor: condenser.NewCursor(database.DB),
		cache:  redisCache,
	}
}

// GetRankedPosts handles bridge.get_ranked_posts
func (r *RankedAPI) GetRankedPosts(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	sort, _ := pMap["sort"].(string)
	if sort == "" {
		return nil, fmt.Errorf("missing required parameter: sort")
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
	_ = observer // TODO: Use for personalized content

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
		"trending": "trending",
		"hot":      "hot",
		"created":  "created",
		"promoted": "promoted",
		"payout":   "payout",
		"payout_comments": "payout_comments",
		"muted":    "muted", // Special case
	}

	querySort, ok := sortMap[sort]
	if !ok {
		return nil, fmt.Errorf("invalid sort type: %s", sort)
	}

	// Get post IDs
	ids, err := r.cursor.GetPostIDsByQuery(ctx.Request.Context(), querySort, startAuthor, startPermlink, limit, tag)
	if err != nil {
		return nil, err
	}

	// TODO: Load full post objects
	result := make([]interface{}, len(ids))
	for i, id := range ids {
		result[i] = map[string]interface{}{
			"id": id,
		}
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

	return result, nil
}

// getCacheTTL returns cache TTL based on sort type
func (r *RankedAPI) getCacheTTL(sort string) time.Duration {
	switch sort {
	case "created":
		return 3 * time.Second
	case "trending", "hot":
		return 300 * time.Second
	case "payout":
		return 30 * time.Second
	case "muted":
		return 600 * time.Second
	default:
		return 60 * time.Second
	}
}

// GetAccountPosts handles bridge.get_account_posts
func (r *RankedAPI) GetAccountPosts(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	sort, _ := pMap["sort"].(string)
	if sort == "" {
		return nil, fmt.Errorf("missing required parameter: sort")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, fmt.Errorf("missing required parameter: account")
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

	// Handle different sort types
	switch sort {
	case "blog":
		// Get from feed cache
		ids, err := r.cursor.GetPostIDsByBlog(ctx.Request.Context(), account, startAuthor, startPermlink, limit)
		if err != nil {
			return nil, err
		}
		result := make([]interface{}, len(ids))
		for i, id := range ids {
			result[i] = map[string]interface{}{"id": id}
		}
		return result, nil
	case "feed":
		// Similar to blog but personalized
		return r.GetAccountPosts(ctx, params) // TODO: Implement feed logic
	case "posts":
		// Author's posts only (no reblogs)
		// TODO: Implement
		return []interface{}{}, nil
	case "comments":
		// Author's comments
		// TODO: Implement
		return []interface{}{}, nil
	case "replies":
		// Replies to author's posts
		// TODO: Implement
		return []interface{}{}, nil
	case "payout":
		// Posts sorted by payout
		ids, err := r.cursor.GetPostIDsByQuery(ctx.Request.Context(), "payout", startAuthor, startPermlink, limit, "")
		if err != nil {
			return nil, err
		}
		result := make([]interface{}, len(ids))
		for i, id := range ids {
			result[i] = map[string]interface{}{"id": id}
		}
		return result, nil
	default:
		return nil, fmt.Errorf("invalid sort type: %s", sort)
	}
}

// GetTrendingTopics handles bridge.get_trending_topics
func (r *RankedAPI) GetTrendingTopics(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	limit := 10
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 25 {
			limit = 25
		}
	}

	// TODO: Query trending topics from tags
	// For now, return empty result
	return []interface{}{}, nil
}

