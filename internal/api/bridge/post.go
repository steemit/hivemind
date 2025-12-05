package bridge

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
)

// PostAPI provides post-related Bridge API methods
type PostAPI struct {
	repo *db.Repository
}

// NewPostAPI creates a new post API
func NewPostAPI(repo *db.Repository) *PostAPI {
	return &PostAPI{repo: repo}
}

// GetPost handles bridge.get_post
func (p *PostAPI) GetPost(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)
	observer, _ := pMap["observer"].(string)

	if author == "" || permlink == "" {
		return nil, fmt.Errorf("missing required parameters: author, permlink")
	}

	postRepo := db.NewPostRepository(p.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil {
		return nil, err
	}
	if post == nil {
		return nil, nil
	}

	// TODO: Build full post object with cached data and observer context
	_ = observer

	return map[string]interface{}{
		"id":       post.ID,
		"author":   post.Author,
		"permlink": post.Permlink,
		"category": post.Category,
	}, nil
}

