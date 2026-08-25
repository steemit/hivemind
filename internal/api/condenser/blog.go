package condenser

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/api/objects"
	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
)

// BlogAPI provides blog-related API methods
type BlogAPI struct {
	repo       *db.Repository
	cursor     *Cursor
	postLoader *objects.PostLoader
}

// NewBlogAPI creates a new blog API
func NewBlogAPI(repo *db.Repository, database *db.DB) *BlogAPI {
	return &BlogAPI{
		repo:       repo,
		cursor:     NewCursor(database.DB),
		postLoader: objects.NewPostLoader(database.DB),
	}
}

// GetBlog handles condenser_api.get_blog
// Posts for an author's blog (w/ reblogs), paged by entry index. Equivalent
// to get_discussions_by_blog, but uses offset-based pagination.
func (b *BlogAPI) GetBlog(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	arr, err := parseListParams(params, 3, 2)
	if err != nil {
		return nil, err
	}

	account := paramString(arr, 0)
	account, err = apierrors.ValidAccount(account, false)
	if err != nil {
		return nil, err
	}

	startIndex := paramInt(arr, 1)
	limit := paramInt(arr, 2)
	// legacy _get_blog: an omitted limit returns entries 0..start_index.
	if limit == 0 {
		limit = startIndex + 1
	}
	if limit, err = apierrors.ValidLimit(limit, 500); err != nil {
		return nil, err
	}
	if startIndex < 0 {
		return nil, apierrors.Publicf("invalid start entry_id: %d", startIndex)
	}

	entries, err := b.loadBlogEntries(ctx, account, startIndex, limit)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// GetBlogEntries handles condenser_api.get_blog_entries
// Interface identical to get_blog, but returns minimalistic post references:
// {blog, entry_id, author, permlink, reblog_on}.
func (b *BlogAPI) GetBlogEntries(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	arr, err := parseListParams(params, 3, 2)
	if err != nil {
		return nil, err
	}

	account := paramString(arr, 0)
	account, err = apierrors.ValidAccount(account, false)
	if err != nil {
		return nil, err
	}

	startIndex := paramInt(arr, 1)
	limit := paramInt(arr, 2)
	if limit == 0 {
		limit = startIndex + 1
	}
	if limit, err = apierrors.ValidLimit(limit, 500); err != nil {
		return nil, err
	}
	if startIndex < 0 {
		return nil, apierrors.Publicf("invalid start entry_id: %d", startIndex)
	}

	fullEntries, err := b.loadBlogEntries(ctx, account, startIndex, limit)
	if err != nil {
		return nil, err
	}

	// Replace the full comment object with author/permlink references.
	out := make([]map[string]interface{}, 0, len(fullEntries))
	for _, entry := range fullEntries {
		post, _ := entry["comment"].(map[string]interface{})
		entry["author"] = post["author"]
		entry["permlink"] = post["permlink"]
		delete(entry, "comment")
		out = append(out, entry)
	}
	return out, nil
}

// loadBlogEntries pages the blog by entry index and builds the legacy entry
// list: {blog, entry_id, comment, reblogged_on}, entry_id descending.
func (b *BlogAPI) loadBlogEntries(ctx *gin.Context, account string, startIndex, limit int) ([]map[string]interface{}, error) {
	resolvedStart, ids, err := b.cursor.GetPostIDsByBlogByIndex(ctx.Request.Context(), account, startIndex, limit)
	if err != nil {
		return nil, err
	}

	posts, err := b.postLoader.LoadPosts(ctx.Request.Context(), ids, 0)
	if err != nil {
		return nil, err
	}

	out := make([]map[string]interface{}, 0, len(posts))
	idx := resolvedStart
	for _, post := range posts {
		author, _ := post["author"].(string)
		created, _ := post["created"].(string)
		reblog := author != account
		reblogOn := "1970-01-01T00:00:00"
		if reblog {
			reblogOn = created
		}
		out = append(out, map[string]interface{}{
			"blog":         account,
			"entry_id":     idx,
			"comment":      post,
			"reblogged_on": reblogOn,
		})
		idx--
	}
	return out, nil
}
