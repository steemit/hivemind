package condenser

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/api/objects"
	"github.com/steemit/hivemind/internal/db"
)

// DiscussionsAPI provides discussion query API methods
type DiscussionsAPI struct {
	repo      *db.Repository
	cursor    *Cursor
	postLoader *objects.PostLoader
}

// NewDiscussionsAPI creates a new discussions API
func NewDiscussionsAPI(repo *db.Repository, database *db.DB) *DiscussionsAPI {
	return &DiscussionsAPI{
		repo:       repo,
		cursor:     NewCursor(database.DB),
		postLoader: objects.NewPostLoader(database.DB),
	}
}

// GetDiscussionsByTrending handles condenser_api.get_discussions_by_trending
func (d *DiscussionsAPI) GetDiscussionsByTrending(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	return d.getDiscussionsBySort(ctx, "trending", params)
}

// GetDiscussionsByHot handles condenser_api.get_discussions_by_hot
func (d *DiscussionsAPI) GetDiscussionsByHot(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	return d.getDiscussionsBySort(ctx, "hot", params)
}

// GetDiscussionsByCreated handles condenser_api.get_discussions_by_created
func (d *DiscussionsAPI) GetDiscussionsByCreated(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	return d.getDiscussionsBySort(ctx, "created", params)
}

// GetDiscussionsByPromoted handles condenser_api.get_discussions_by_promoted
func (d *DiscussionsAPI) GetDiscussionsByPromoted(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	return d.getDiscussionsBySort(ctx, "promoted", params)
}

// getDiscussionsBySort is a helper that handles all sort-based discussion queries
func (d *DiscussionsAPI) getDiscussionsBySort(ctx *gin.Context, sort string, params json.RawMessage) (interface{}, error) {
	// Parse parameters
	var p map[string]interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		// Try array format
		var arr []interface{}
		if err2 := json.Unmarshal(params, &arr); err2 != nil {
			return nil, fmt.Errorf("invalid parameters format")
		}
		// Convert array to map (legacy format)
		p = make(map[string]interface{})
		if len(arr) > 0 {
			if m, ok := arr[0].(map[string]interface{}); ok {
				p = m
			}
		}
	}

	startAuthor := ""
	if sa, ok := p["start_author"].(string); ok {
		startAuthor = sa
	}
	startPermlink := ""
	if sp, ok := p["start_permlink"].(string); ok {
		startPermlink = sp
	}
	limit := 20
	if l, ok := p["limit"].(float64); ok {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}
	tag := ""
	if t, ok := p["tag"].(string); ok {
		tag = t
	}

	// Get post IDs
	ids, err := d.cursor.GetPostIDsByQuery(ctx.Request.Context(), sort, startAuthor, startPermlink, limit, tag)
	if err != nil {
		return nil, err
	}

	// Load full post objects
	truncateBody := 0
	if tb, ok := p["truncate_body"].(float64); ok {
		truncateBody = int(tb)
	}

	posts, err := d.postLoader.LoadPosts(ctx.Request.Context(), ids, truncateBody)
	if err != nil {
		return nil, err
	}

	return posts, nil
}

// GetDiscussionsByBlog handles condenser_api.get_discussions_by_blog
func (d *DiscussionsAPI) GetDiscussionsByBlog(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p map[string]interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		var arr []interface{}
		if err2 := json.Unmarshal(params, &arr); err2 != nil {
			return nil, fmt.Errorf("invalid parameters format")
		}
		if len(arr) > 0 {
			if m, ok := arr[0].(map[string]interface{}); ok {
				p = m
			}
		}
	}

	tag := ""
	if t, ok := p["tag"].(string); ok {
		tag = t
	}
	startAuthor := ""
	if sa, ok := p["start_author"].(string); ok {
		startAuthor = sa
	}
	startPermlink := ""
	if sp, ok := p["start_permlink"].(string); ok {
		startPermlink = sp
	}
	limit := 20
	if l, ok := p["limit"].(float64); ok {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}

	// tag parameter is actually the account name for blog queries
	account := tag
	if account == "" {
		return nil, fmt.Errorf("missing required parameter: tag (account name)")
	}

	// Get post IDs from feed cache
	ids, err := d.cursor.GetPostIDsByBlog(ctx.Request.Context(), account, startAuthor, startPermlink, limit)
	if err != nil {
		return nil, err
	}

	// Load full post objects
	truncateBody := 0
	if tb, ok := p["truncate_body"].(float64); ok {
		truncateBody = int(tb)
	}

	posts, err := d.postLoader.LoadPosts(ctx.Request.Context(), ids, truncateBody)
	if err != nil {
		return nil, err
	}

	return posts, nil
}

// GetDiscussionsByFeed handles condenser_api.get_discussions_by_feed
func (d *DiscussionsAPI) GetDiscussionsByFeed(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// Feed is similar to blog but includes posts from followed accounts
	// For now, use blog implementation
	return d.GetDiscussionsByBlog(ctx, params)
}

