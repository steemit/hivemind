package bridge

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/pkg/telemetry"
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
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_post")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)
	observer, _ := pMap["observer"].(string)

	telemetry.AddSpanAttributes(span, map[string]string{
		"author":   author,
		"permlink": permlink,
	})

	if author == "" || permlink == "" {
		return nil, fmt.Errorf("missing required parameters: author, permlink")
	}

	postRepo := db.NewPostRepository(p.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if post == nil {
		return nil, nil
	}

	// Record metrics
	telemetry.PostsFetched.WithLabelValues("bridge.get_post").Inc()
	telemetry.SetSpanSuccess(span)

	// TODO: Build full post object with cached data and observer context
	_ = observer

	return map[string]interface{}{
		"id":       post.ID,
		"author":   post.Author,
		"permlink": post.Permlink,
		"category": post.Category,
	}, nil
}

// NormalizePost handles bridge.normalize_post
// Normalizes a post object for consistent output format
func (p *PostAPI) NormalizePost(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.normalize_post")
	defer span.End()

	// The input should be a post object that needs to be normalized
	var postObj map[string]interface{}
	if err := json.Unmarshal(params, &postObj); err != nil {
		return nil, fmt.Errorf("invalid post object")
	}

	// Normalize post fields
	normalized := make(map[string]interface{})

	// Copy and normalize standard fields
	if author, ok := postObj["author"]; ok {
		normalized["author"] = author
	}
	if permlink, ok := postObj["permlink"]; ok {
		normalized["permlink"] = permlink
	}
	if category, ok := postObj["category"]; ok {
		normalized["category"] = category
	}
	if title, ok := postObj["title"]; ok {
		normalized["title"] = title
	}
	if body, ok := postObj["body"]; ok {
		normalized["body"] = body
	}

	// Ensure json_metadata is a valid object
	if metadata, ok := postObj["json_metadata"]; ok {
		switch v := metadata.(type) {
		case string:
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(v), &parsed); err == nil {
				normalized["json_metadata"] = parsed
			} else {
				normalized["json_metadata"] = map[string]interface{}{}
			}
		case map[string]interface{}:
			normalized["json_metadata"] = v
		default:
			normalized["json_metadata"] = map[string]interface{}{}
		}
	} else {
		normalized["json_metadata"] = map[string]interface{}{}
	}

	// Copy other common fields
	for _, field := range []string{
		"id", "created", "last_update", "depth", "children",
		"net_rshares", "abs_rshares", "vote_rshares",
		"total_payout_value", "curator_payout_value", "pending_payout_value",
		"promoted", "author_reputation", "net_votes", "active_votes",
		"url", "root_author", "root_permlink", "root_title",
	} {
		if val, ok := postObj[field]; ok {
			normalized[field] = val
		}
	}

	telemetry.SetSpanSuccess(span)
	return normalized, nil
}

// GetPostHeader handles bridge.get_post_header
// Returns minimal post information (author, permlink, title, category)
func (p *PostAPI) GetPostHeader(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_post_header")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)

	telemetry.AddSpanAttributes(span, map[string]string{
		"author":   author,
		"permlink": permlink,
	})

	if author == "" || permlink == "" {
		return nil, fmt.Errorf("missing required parameters: author, permlink")
	}

	postRepo := db.NewPostRepository(p.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if post == nil {
		return nil, nil
	}

	telemetry.PostsFetched.WithLabelValues("bridge.get_post_header").Inc()
	telemetry.SetSpanSuccess(span)

	return map[string]interface{}{
		"author":   post.Author,
		"permlink": post.Permlink,
		"title":    "", // TODO: Get from cached post data
		"category": post.Category,
		"depth":    post.Depth,
	}, nil
}

// GetDiscussion handles bridge.get_discussion
// Returns a discussion thread with all replies
func (p *PostAPI) GetDiscussion(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_discussion")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)
	observer, _ := pMap["observer"].(string)

	telemetry.AddSpanAttributes(span, map[string]string{
		"author":   author,
		"permlink": permlink,
	})

	if author == "" || permlink == "" {
		return nil, fmt.Errorf("missing required parameters: author, permlink")
	}

	postRepo := db.NewPostRepository(p.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if post == nil {
		return nil, nil
	}

	// Build discussion map with root post
	discussion := make(map[string]interface{})
	rootKey := author + "/" + permlink

	discussion[rootKey] = map[string]interface{}{
		"id":       post.ID,
		"author":   post.Author,
		"permlink": post.Permlink,
		"category": post.Category,
		"depth":    post.Depth,
		"children": post.Children,
	}

	// TODO: Fetch all replies recursively
	// For now, return just the root post

	_ = observer

	telemetry.PostsFetched.WithLabelValues("bridge.get_discussion").Inc()
	telemetry.SetSpanSuccess(span)

	return discussion, nil
}
