package condenser

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/api/objects"
	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
)

// DiscussionsAPI provides discussion query API methods
type DiscussionsAPI struct {
	repo       *db.Repository
	cursor     *Cursor
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
	// Parse parameters (object, nested-query, or positional form).
	p, err := parseQueryParams(params, "tag", "start_author", "start_permlink", "limit", "truncate_body")
	if err != nil {
		return nil, err
	}

	startAuthor, _ := p["start_author"].(string)
	startPermlink, _ := p["start_permlink"].(string)
	limit := limitFromQuery(p, 20, 100)
	tag, _ := p["tag"].(string)

	// Get post IDs
	ids, err := d.cursor.GetPostIDsByQuery(ctx.Request.Context(), sort, startAuthor, startPermlink, limit, tag)
	if err != nil {
		return nil, err
	}

	// Load full post objects
	truncateBody := truncateBodyFromQuery(p)

	posts, err := d.postLoader.LoadPosts(ctx.Request.Context(), ids, truncateBody)
	if err != nil {
		return nil, err
	}

	return posts, nil
}

// GetDiscussionsByBlog handles condenser_api.get_discussions_by_blog
func (d *DiscussionsAPI) GetDiscussionsByBlog(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	p, err := parseQueryParams(params, "tag", "start_author", "start_permlink", "limit", "truncate_body")
	if err != nil {
		return nil, err
	}

	tag, _ := p["tag"].(string)
	startAuthor, _ := p["start_author"].(string)
	startPermlink, _ := p["start_permlink"].(string)
	limit := limitFromQuery(p, 20, 100)

	// tag parameter is actually the account name for blog queries
	account := tag
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: tag (account name)")
	}

	// Get post IDs from feed cache
	ids, err := d.cursor.GetPostIDsByBlog(ctx.Request.Context(), account, startAuthor, startPermlink, limit)
	if err != nil {
		return nil, err
	}

	// Load full post objects
	truncateBody := truncateBodyFromQuery(p)

	posts, err := d.postLoader.LoadPosts(ctx.Request.Context(), ids, truncateBody)
	if err != nil {
		return nil, err
	}

	return posts, nil
}

// GetDiscussionsByFeed handles condenser_api.get_discussions_by_feed
// The account's personalized feed: posts + resteems from everyone they follow,
// with reblogged_by attribution (mirrors legacy pids_by_feed_with_reblog +
// load_posts_reblogs).
func (d *DiscussionsAPI) GetDiscussionsByFeed(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	p, err := parseQueryParams(params, "tag", "start_author", "start_permlink", "limit", "truncate_body")
	if err != nil {
		return nil, err
	}

	tag, _ := p["tag"].(string)
	if tag == "" {
		return nil, apierrors.PublicError("`tag` cannot be blank")
	}
	account, err := apierrors.ValidAccount(tag, false)
	if err != nil {
		return nil, err
	}
	startAuthor, _ := p["start_author"].(string)
	if startAuthor != "" {
		startAuthor, err = apierrors.ValidAccount(startAuthor, false)
		if err != nil {
			return nil, err
		}
	}
	startPermlink, _ := p["start_permlink"].(string)
	if startPermlink != "" {
		if _, err := apierrors.ValidPermlink(startPermlink, false); err != nil {
			return nil, err
		}
	}
	limit := limitFromQuery(p, 20, 100)
	truncateBody := truncateBodyFromQuery(p)

	entries, err := d.cursor.GetPostIDsByFeedWithReblog(ctx.Request.Context(), account, startAuthor, startPermlink, limit)
	if err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(entries))
	rebloggers := make(map[int64]string, len(entries))
	for _, e := range entries {
		ids = append(ids, e.PostID)
		rebloggers[e.PostID] = e.Accounts
	}

	posts, err := d.postLoader.LoadPosts(ctx.Request.Context(), ids, truncateBody)
	if err != nil {
		return nil, err
	}

	// Merge reblogged_by (comma-joined names, author excluded) — mirrors
	// legacy condenser load_posts_reblogs.
	for _, post := range posts {
		pid, _ := post["id"].(int64)
		csv, ok := rebloggers[pid]
		if !ok || csv == "" {
			continue
		}
		author, _ := post["author"].(string)
		seen := make(map[string]bool)
		rby := make([]interface{}, 0, 4)
		for _, name := range strings.Split(csv, ",") {
			if name == "" || name == author || seen[name] {
				continue
			}
			seen[name] = true
			rby = append(rby, name)
		}
		if len(rby) > 0 {
			post["reblogged_by"] = rby
		}
	}

	return posts, nil
}
