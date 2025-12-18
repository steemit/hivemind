package condenser

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/api/objects"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// ContentAPI provides content-related API methods
type ContentAPI struct {
	repo      *db.Repository
	database  *gorm.DB
	postLoader *objects.PostLoader
}

// NewContentAPI creates a new content API
func NewContentAPI(repo *db.Repository) *ContentAPI {
	return &ContentAPI{
		repo: repo,
		// Note: database should be passed in, but for now we'll get it from repo
	}
}

// SetDatabase sets the database connection for post loading
func (c *ContentAPI) SetDatabase(db *gorm.DB) {
	c.database = db
	c.postLoader = objects.NewPostLoader(db)
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

	// Build full post object with cached data
	if c.postLoader == nil {
		// Fallback if database not set
		return map[string]interface{}{
			"id":       post.ID,
			"author":   post.Author,
			"permlink": post.Permlink,
			"category": post.Category,
			"depth":    post.Depth,
		}, nil
	}

	posts, err := c.postLoader.LoadPosts(ctx.Request.Context(), []int64{post.ID}, 0)
	if err != nil || len(posts) == 0 {
		return nil, nil
	}

	return posts[0], nil
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

	// Query child posts
	var childPosts []models.Post
	if c.database == nil {
		return []interface{}{}, nil
	}
	if err := c.database.WithContext(ctx.Request.Context()).
		Where("parent_id = ? AND is_deleted = false", parent.ID).
		Order("created_at ASC").
		Find(&childPosts).Error; err != nil {
		return []interface{}{}, nil
	}

	if len(childPosts) == 0 {
		return []interface{}{}, nil
	}

	// Load full post objects
	childIDs := make([]int64, len(childPosts))
	for i, cp := range childPosts {
		childIDs[i] = cp.ID
	}

	if c.postLoader == nil {
		return []interface{}{}, nil
	}

	posts, err := c.postLoader.LoadPosts(ctx.Request.Context(), childIDs, 0)
	if err != nil {
		return []interface{}{}, nil
	}

	return posts, nil
}

