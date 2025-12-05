package condenser

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
)

// BlogAPI provides blog-related API methods
type BlogAPI struct {
	repo   *db.Repository
	cursor *Cursor
}

// NewBlogAPI creates a new blog API
func NewBlogAPI(repo *db.Repository, database *db.DB) *BlogAPI {
	return &BlogAPI{
		repo:   repo,
		cursor: NewCursor(database.DB),
	}
}

// GetBlog handles condenser_api.get_blog
func (b *BlogAPI) GetBlog(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 1 {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	account, _ := p[0].(string)
	startEntryID := int64(0)
	if len(p) > 1 {
		if id, ok := p[1].(float64); ok {
			startEntryID = int64(id)
		}
	}
	limit := 20
	if len(p) > 2 {
		if l, ok := p[2].(float64); ok {
			limit = int(l)
			if limit > 100 {
				limit = 100
			}
		}
	}

	// Get account ID
	accountRepo := db.NewAccountRepository(b.repo)
	acc, err := accountRepo.GetByName(ctx.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return []interface{}{}, nil
	}

	// Query feed cache with entry_id pagination
	// TODO: Implement entry_id based pagination
	// For now, use simple limit
	ids, err := b.cursor.GetPostIDsByBlog(ctx.Request.Context(), account, "", "", limit)
	if err != nil {
		return nil, err
	}

	// Build blog entries
	result := make([]interface{}, len(ids))
	for i, id := range ids {
		result[i] = map[string]interface{}{
			"blog":      account,
			"entry_id":  startEntryID + int64(i),
			"comment": map[string]interface{}{
				"id": id,
			},
		}
	}

	return result, nil
}

// GetBlogEntries handles condenser_api.get_blog_entries
func (b *BlogAPI) GetBlogEntries(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// Similar to get_blog but returns minimal post references
	return b.GetBlog(ctx, params)
}

