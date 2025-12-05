package condenser

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
)

// ContentAPI provides content-related API methods
type ContentAPI struct {
	repo *db.Repository
}

// NewContentAPI creates a new content API
func NewContentAPI(repo *db.Repository) *ContentAPI {
	return &ContentAPI{repo: repo}
}

// GetContent handles condenser_api.get_content
func (c *ContentAPI) GetContent(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 2 {
		return nil, fmt.Errorf("missing required parameters: author, permlink")
	}

	author, _ := p[0].(string)
	permlink, _ := p[1].(string)

	postRepo := db.NewPostRepository(c.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil {
		return nil, err
	}
	if post == nil {
		return nil, nil
	}

	// TODO: Build full post object with cached data
	// For now, return basic post info
	return map[string]interface{}{
		"id":       post.ID,
		"author":   post.Author,
		"permlink": post.Permlink,
		"category": post.Category,
		"depth":    post.Depth,
	}, nil
}

// GetContentReplies handles condenser_api.get_content_replies
func (c *ContentAPI) GetContentReplies(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 2 {
		return nil, fmt.Errorf("missing required parameters: author, permlink")
	}

	author, _ := p[0].(string)
	permlink, _ := p[1].(string)

	// Get parent post
	postRepo := db.NewPostRepository(c.repo)
	parent, err := postRepo.GetByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil || parent == nil {
		return []interface{}{}, nil
	}

	// TODO: Query child posts
	// This should query hive_posts where parent_id = parent.ID

	return []interface{}{}, nil
}

