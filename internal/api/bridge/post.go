package bridge

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/api/objects"
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

// getDiscussionCTE is the recursive CTE that fetches the entire comment tree
// for a root post in a SINGLE query (replacing the legacy while-loop BFS that
// caused connection-pool exhaustion). See get-discussion-incident-and-otel-plan.md §11.2.
//
// Safety limits: MAX_DEPTH=50, MAX_THREAD_POSTS=500 (matching legacy).
//
// Moderation: article-blocked posts (list_type=1) and posts by user-blocked
// authors (list_type=3) are excluded at EVERY level of the recursion, so their
// subtrees are never traversed — mirrors legacy thread.py::_DISCUSSION_TREE_SQL.
const getDiscussionCTE = `
WITH RECURSIVE descendants AS (
    SELECT p.id, p.parent_id, 1 AS level
    FROM hive_posts p
    LEFT JOIN hive_posts_status s3 ON s3.list_type = 3 AND s3.author = p.author
    LEFT JOIN hive_posts_status s1 ON s1.list_type = 1 AND s1.post_id = p.id
    WHERE p.parent_id = ? AND p.is_deleted = '0'
      AND s3.id IS NULL AND s1.id IS NULL
    UNION ALL
    SELECT p.id, p.parent_id, d.level + 1
    FROM hive_posts p
    JOIN descendants d ON p.parent_id = d.id
    LEFT JOIN hive_posts_status s3 ON s3.list_type = 3 AND s3.author = p.author
    LEFT JOIN hive_posts_status s1 ON s1.list_type = 1 AND s1.post_id = p.id
    WHERE p.is_deleted = '0' AND d.level < 50
      AND s3.id IS NULL AND s1.id IS NULL
)
SELECT id, parent_id, level FROM descendants
ORDER BY level, id
LIMIT 500`

// descendantRow holds a CTE result row.
type descendantRow struct {
	ID       int64 `gorm:"column:id"`
	ParentID int64 `gorm:"column:parent_id"`
	Level    int   `gorm:"column:level"`
}

// GetDiscussion handles bridge.get_discussion.
// Returns a discussion thread with all replies using a single CTE query
// (not the legacy while-loop BFS that caused connection-pool exhaustion).
func (p *PostAPI) GetDiscussion(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	rootCtx, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_discussion")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)
	observer, _ := pMap["observer"].(string)
	_ = observer

	telemetry.AddSpanAttributes(span, map[string]string{
		"author":   author,
		"permlink": permlink,
	})

	if author == "" || permlink == "" {
		return nil, fmt.Errorf("missing required parameters: author, permlink")
	}

	// Sub-span: resolve root post ID.
	pidCtx, pidSpan := telemetry.StartSpanWithName(rootCtx, "db.get_post_id")
	postRepo := db.NewPostRepository(p.repo)
	rootPost, err := postRepo.GetByAuthorPermlink(pidCtx, author, permlink)
	pidSpan.End()
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	if rootPost == nil {
		return nil, nil
	}
	// Legacy resolves the root with `is_deleted = '0'` in SQL
	// (thread.py::_get_post_id); deleted roots yield an empty discussion.
	if rootPost.IsDeleted {
		return nil, nil
	}

	// Sub-span: moderation checks — hide the whole discussion when the root
	// author is user-blocked (list_type=3) or the root post is article-blocked
	// (list_type=1). Mirrors thread.py::get_discussion entry checks.
	hideCtx, hideSpan := telemetry.StartSpanWithName(rootCtx, "discussion.hide_check")
	statusRepo := db.NewPostStatusRepository(p.repo)
	authorHidden, err := statusRepo.IsAuthorHidden(hideCtx, rootPost.Author)
	if err != nil {
		hideSpan.End()
		telemetry.RecordSpanError(span, err)
		return nil, err
	}
	postHidden := false
	if !authorHidden {
		postHidden, err = statusRepo.IsPostHidden(hideCtx, rootPost.ID)
		if err != nil {
			hideSpan.End()
			telemetry.RecordSpanError(span, err)
			return nil, err
		}
	}
	hideSpan.End()
	if authorHidden || postHidden {
		return nil, nil
	}

	// Sub-span: CTE tree walk — the single query that replaces the legacy
	// while-loop. Records depth/post_count for observability.
	treeCtx, treeSpan := telemetry.StartSpanWithName(rootCtx, "discussion.tree_walk")
	var descendants []descendantRow
	if err := p.repo.DB().WithContext(treeCtx).Raw(getDiscussionCTE, rootPost.ID).Scan(&descendants).Error; err != nil {
		treeSpan.End()
		telemetry.RecordSpanError(span, err)
		return nil, fmt.Errorf("failed to fetch discussion tree: %w", err)
	}

	maxDepth := 0
	if len(descendants) > 0 {
		maxDepth = descendants[len(descendants)-1].Level // sorted by level
	}
	telemetry.AddSpanAttributes(treeSpan, map[string]string{
		"discussion.depth":       fmt.Sprintf("%d", maxDepth),
		"discussion.posts_count": fmt.Sprintf("%d", len(descendants)),
	})
	treeSpan.End()

	// Collect all post IDs (root + descendants).
	allIDs := make([]int64, 0, len(descendants)+1)
	allIDs = append(allIDs, rootPost.ID)
	parentMap := make(map[int64]int64) // child_id → parent_id
	idSet := make(map[int64]bool)
	idSet[rootPost.ID] = true
	for _, d := range descendants {
		if !idSet[d.ID] {
			allIDs = append(allIDs, d.ID)
			idSet[d.ID] = true
		}
		parentMap[d.ID] = d.ParentID
	}

	// Sub-span: load post objects via LoadPostsKeyed (bridge_api shape).
	postsCtx, postsSpan := telemetry.StartSpanWithName(rootCtx, "discussion.load_posts")
	loader := objects.NewPostLoader(p.repo.DB())
	postsByKeyed, err := loader.LoadPostsKeyed(postsCtx, allIDs, 0)
	postsSpan.End()
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, fmt.Errorf("failed to load posts: %w", err)
	}

	// Build the discussion tree: map[author/permlink] → post object.
	// Each post gets a "replies" key listing its children's keys.
	discussion := make(map[string]interface{})
	keyByID := make(map[int64]string) // post_id → "author/permlink"

	// First pass: create keys for all posts.
	for pid, obj := range postsByKeyed {
		pa, _ := obj["author"].(string)
		pp, _ := obj["permlink"].(string)
		keyByID[pid] = pa + "/" + pp
	}

	// Second pass: build tree structure.
	childrenByKey := make(map[string][]string) // parent_key → [child_keys...]
	for _, d := range descendants {
		childKey, ok := keyByID[d.ID]
		if !ok {
			continue // post not in cache, skip
		}
		parentKey, ok := keyByID[d.ParentID]
		if !ok {
			parentKey = keyByID[rootPost.ID] // root
		}
		childrenByKey[parentKey] = append(childrenByKey[parentKey], childKey)

		// Ensure the child has a "replies" key initialized.
		obj := postsByKeyed[d.ID]
		if obj != nil {
			if _, has := obj["replies"]; !has {
				obj["replies"] = []string{}
			}
		}
	}

	// Third pass: assign replies arrays and populate discussion map.
	for pid, obj := range postsByKeyed {
		key := keyByID[pid]
		if children, ok := childrenByKey[key]; ok {
			obj["replies"] = children
		} else {
			obj["replies"] = []string{}
		}
		discussion[key] = obj
	}

	telemetry.PostsFetched.WithLabelValues("bridge.get_discussion").Add(float64(len(allIDs)))
	telemetry.SetSpanSuccess(span)

	return discussion, nil
}
