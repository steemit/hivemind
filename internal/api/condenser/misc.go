package condenser

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
)

// MiscAPI provides miscellaneous API methods
type MiscAPI struct {
	repo   *db.Repository
	cursor *Cursor
}

// NewMiscAPI creates a new misc API
func NewMiscAPI(repo *db.Repository, database *db.DB) *MiscAPI {
	return &MiscAPI{
		repo:   repo,
		cursor: NewCursor(database.DB),
	}
}

// GetDiscussionsByComments handles condenser_api.get_discussions_by_comments
func (m *MiscAPI) GetDiscussionsByComments(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p map[string]interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		var arr []interface{}
		if err2 := json.Unmarshal(params, &arr); err2 != nil {
			return nil, fmt.Errorf("invalid parameters format")
		}
		if len(arr) > 0 {
			if mp, ok := arr[0].(map[string]interface{}); ok {
				p = mp
			}
		}
	}

	startAuthor := ""
	if sa, ok := p["start_author"].(string); ok {
		startAuthor = sa
	}
	if startAuthor == "" {
		return nil, fmt.Errorf("start_author cannot be blank")
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

	// Query comments by author
	// TODO: Implement pids_by_account_comments query
	// For now, return empty result
	_ = startPermlink // TODO: Use for pagination
	return []interface{}{}, nil
}

// GetRepliesByLastUpdate handles condenser_api.get_replies_by_last_update
func (m *MiscAPI) GetRepliesByLastUpdate(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 1 {
		return nil, fmt.Errorf("missing required parameter: start_author")
	}

	startAuthor, _ := p[0].(string)
	startPermlink := ""
	if len(p) > 1 {
		startPermlink, _ = p[1].(string)
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

	// TODO: Query replies to author's posts
	_ = startAuthor
	_ = startPermlink
	_ = limit

	return []interface{}{}, nil
}

// GetDiscussionsByAuthorBeforeDate handles condenser_api.get_discussions_by_author_before_date
func (m *MiscAPI) GetDiscussionsByAuthorBeforeDate(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 1 {
		return nil, fmt.Errorf("missing required parameter: author")
	}

	author, _ := p[0].(string)
	startPermlink := ""
	if len(p) > 1 {
		startPermlink, _ = p[1].(string)
	}
	beforeDate := ""
	if len(p) > 2 {
		beforeDate, _ = p[2].(string)
	}
	limit := 10
	if len(p) > 3 {
		if l, ok := p[3].(float64); ok {
			limit = int(l)
			if limit > 100 {
				limit = 100
			}
		}
	}

	// Query author's blog posts (without reblogs) before date
	// TODO: Implement query
	_ = author
	_ = startPermlink
	_ = beforeDate
	_ = limit

	return []interface{}{}, nil
}

// GetPostDiscussionsByPayout handles condenser_api.get_post_discussions_by_payout
func (m *MiscAPI) GetPostDiscussionsByPayout(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// Similar to get_discussions_by_payout but only root posts
	return m.getDiscussionsByPayout(ctx, params, true)
}

// GetCommentDiscussionsByPayout handles condenser_api.get_comment_discussions_by_payout
func (m *MiscAPI) GetCommentDiscussionsByPayout(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// Similar to get_discussions_by_payout but only comments
	return m.getDiscussionsByPayout(ctx, params, false)
}

// getDiscussionsByPayout is a helper for payout queries
func (m *MiscAPI) getDiscussionsByPayout(ctx *gin.Context, params json.RawMessage, rootOnly bool) (interface{}, error) {
	var p map[string]interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		var arr []interface{}
		if err2 := json.Unmarshal(params, &arr); err2 != nil {
			return nil, fmt.Errorf("invalid parameters format")
		}
		if len(arr) > 0 {
			if mp, ok := arr[0].(map[string]interface{}); ok {
				p = mp
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

	// Use payout sort
	sort := "payout"
	if !rootOnly {
		sort = "payout_comments"
	}

	ids, err := m.cursor.GetPostIDsByQuery(ctx.Request.Context(), sort, startAuthor, startPermlink, limit, tag)
	if err != nil {
		return nil, err
	}

	result := make([]interface{}, len(ids))
	for i, id := range ids {
		result[i] = map[string]interface{}{
			"id": id,
		}
	}

	return result, nil
}

// GetTransaction handles condenser_api.get_transaction
func (m *MiscAPI) GetTransaction(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 1 {
		return nil, fmt.Errorf("missing required parameter: trx_id")
	}

	trxID, _ := p[0].(string)

	// Query transaction from hive_trxid_block_num
	// TODO: Implement transaction lookup
	_ = trxID

	return nil, fmt.Errorf("transaction lookup not yet implemented")
}

// GetState handles condenser_api.get_state
func (m *MiscAPI) GetState(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 1 {
		return nil, fmt.Errorf("missing required parameter: path")
	}

	path, _ := p[0].(string)

	// TODO: Implement get_state logic
	// This is a complex method that handles various path formats
	_ = path

	return map[string]interface{}{
		"feed_price": map[string]interface{}{},
		"props":      map[string]interface{}{},
		"tags":       map[string]interface{}{},
		"accounts":   map[string]interface{}{},
		"content":    map[string]interface{}{},
	}, nil
}

// GetAccountVotes handles condenser_api.get_account_votes (dummy method)
func (m *MiscAPI) GetAccountVotes(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// This method is no longer supported
	return nil, fmt.Errorf("get_account_votes is no longer supported")
}

