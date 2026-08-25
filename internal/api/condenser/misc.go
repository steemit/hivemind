package condenser

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/api/objects"
	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/internal/steem"
)

// MiscAPI provides miscellaneous API methods
type MiscAPI struct {
	repo       *db.Repository
	cursor     *Cursor
	postLoader *objects.PostLoader
	steemd     *steem.Client
}

// NewMiscAPI creates a new misc API. The steemd client is optional and only
// required by get_transaction; nil disables that method.
func NewMiscAPI(repo *db.Repository, database *db.DB, steemd *steem.Client) *MiscAPI {
	return &MiscAPI{
		repo:       repo,
		cursor:     NewCursor(database.DB),
		postLoader: objects.NewPostLoader(database.DB),
		steemd:     steemd,
	}
}

// GetDiscussionsByComments handles condenser_api.get_discussions_by_comments
// Comments made by start_author.
func (m *MiscAPI) GetDiscussionsByComments(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	p, err := parseQueryParams(params, "start_author", "start_permlink", "limit", "truncate_body")
	if err != nil {
		return nil, err
	}

	startAuthor, _ := p["start_author"].(string)
	if startAuthor == "" {
		return nil, apierrors.PublicError("`start_author` cannot be blank")
	}
	startAuthor, err = apierrors.ValidAccount(startAuthor, false)
	if err != nil {
		return nil, err
	}

	startPermlink, _ := p["start_permlink"].(string)
	if startPermlink != "" {
		if _, err := apierrors.ValidPermlink(startPermlink, false); err != nil {
			return nil, err
		}
	}
	limit := limitFromQuery(p, 20, 100)
	truncateBody := truncateBodyFromQuery(p)

	ids, err := m.cursor.GetPostIDsByAccountComments(ctx.Request.Context(), startAuthor, startPermlink, limit)
	if err != nil {
		return nil, err
	}
	return m.postLoader.LoadPosts(ctx.Request.Context(), ids, truncateBody)
}

// GetRepliesByLastUpdate handles condenser_api.get_replies_by_last_update
// All replies made to any of start_author's posts.
func (m *MiscAPI) GetRepliesByLastUpdate(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	p, err := parseQueryParams(params, "start_author", "start_permlink", "limit")
	if err != nil {
		return nil, err
	}

	startAuthor, _ := p["start_author"].(string)
	if startAuthor == "" {
		return nil, apierrors.PublicError("`start_author` cannot be blank")
	}
	startAuthor, err = apierrors.ValidAccount(startAuthor, false)
	if err != nil {
		return nil, err
	}

	startPermlink, _ := p["start_permlink"].(string)
	if startPermlink != "" {
		if _, err := apierrors.ValidPermlink(startPermlink, false); err != nil {
			return nil, err
		}
	}
	limit := limitFromQuery(p, 20, 100)

	ids, err := m.cursor.GetPostIDsByRepliesToAccount(ctx.Request.Context(), startAuthor, startPermlink, limit)
	if err != nil {
		return nil, err
	}
	return m.postLoader.LoadPosts(ctx.Request.Context(), ids, 0)
}

// GetDiscussionsByAuthorBeforeDate handles
// condenser_api.get_discussions_by_author_before_date. Returns the author's
// blog posts without reblogs. before_date is ignored, matching legacy
// (broken/ignored in steemd as well).
func (m *MiscAPI) GetDiscussionsByAuthorBeforeDate(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	p, err := parseQueryParams(params, "author", "start_permlink", "before_date", "limit")
	if err != nil {
		return nil, err
	}

	author, _ := p["author"].(string)
	if author == "" {
		return nil, apierrors.PublicError("`author` cannot be blank")
	}
	author, err = apierrors.ValidAccount(author, false)
	if err != nil {
		return nil, err
	}

	startPermlink, _ := p["start_permlink"].(string)
	if startPermlink != "" {
		if _, err := apierrors.ValidPermlink(startPermlink, false); err != nil {
			return nil, err
		}
	}
	limit := limitFromQuery(p, 10, 100)

	ids, err := m.cursor.GetPostIDsByBlogWithoutReblog(ctx.Request.Context(), author, startPermlink, limit)
	if err != nil {
		return nil, err
	}
	return m.postLoader.LoadPosts(ctx.Request.Context(), ids, 0)
}

// GetPostDiscussionsByPayout handles condenser_api.get_post_discussions_by_payout
// Top-level posts, sorted by payout.
func (m *MiscAPI) GetPostDiscussionsByPayout(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	return m.getDiscussionsByPayout(ctx, params, "payout")
}

// GetCommentDiscussionsByPayout handles condenser_api.get_comment_discussions_by_payout
// Comments, sorted by payout.
func (m *MiscAPI) GetCommentDiscussionsByPayout(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	return m.getDiscussionsByPayout(ctx, params, "payout_comments")
}

// getDiscussionsByPayout is a helper for payout queries
func (m *MiscAPI) getDiscussionsByPayout(ctx *gin.Context, params json.RawMessage, sort string) (interface{}, error) {
	p, err := parseQueryParams(params, "start_author", "start_permlink", "limit", "tag", "truncate_body")
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
	tag, _ := p["tag"].(string)
	truncateBody := truncateBodyFromQuery(p)

	ids, err := m.cursor.GetPostIDsByQuery(ctx.Request.Context(), sort, startAuthor, startPermlink, limit, tag)
	if err != nil {
		return nil, err
	}
	return m.postLoader.LoadPosts(ctx.Request.Context(), ids, truncateBody)
}

// GetTransaction handles condenser_api.get_transaction
// Resolves the trx_id to a block via hive_trxid_block_num, then fetches the
// full transaction from steemd.
func (m *MiscAPI) GetTransaction(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	arr, err := parseListParams(params, 1, 1)
	if err != nil {
		return nil, err
	}
	trxID := paramString(arr, 0)
	if trxID == "" {
		return nil, apierrors.PublicError("missing required parameter: trx_id")
	}

	var blockNum int64
	err = m.repo.DB().WithContext(ctx.Request.Context()).
		Model(&models.TransactionBlock{}).
		Where("trx_id = ?", trxID).
		Select("block_num").
		Scan(&blockNum).Error
	if err != nil {
		return nil, err
	}
	if blockNum == 0 {
		return nil, apierrors.PublicError("trx_id does not exist")
	}

	if m.steemd == nil {
		return nil, apierrors.PublicError("transaction lookup not yet implemented")
	}

	block, err := m.steemd.GetBlock(ctx.Request.Context(), blockNum)
	if err != nil {
		return nil, err
	}

	trxIDs, _ := block["transaction_ids"].([]string)
	trxIndex := -1
	for i, id := range trxIDs {
		if id == trxID {
			trxIndex = i
			break
		}
	}
	if trxIndex == -1 {
		return nil, apierrors.PublicError("invalid trx_id")
	}
	transactions, _ := block["transactions"].([]interface{})
	if trxIndex >= len(transactions) {
		return nil, apierrors.PublicError("invalid trx_id")
	}

	trx, ok := transactions[trxIndex].(map[string]interface{})
	if !ok {
		return nil, apierrors.PublicError("invalid trx_id")
	}
	trx["transaction_id"] = trxID
	trx["block_num"] = blockNum
	trx["transaction_num"] = trxIndex
	return trx, nil
}

// GetState handles condenser_api.get_state
// The full get_state router (path parsing + feed_price/props assembly) is a
// large legacy-compat surface aimed at the retired condenser frontend; it is
// intentionally not approximated here (per repo policy: explicit error
// instead of a wrong-shaped stub).
func (m *MiscAPI) GetState(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	return nil, apierrors.PublicError("get_state is not implemented; use bridge/condenser methods instead")
}

// GetAccountVotes handles condenser_api.get_account_votes (dummy method)
func (m *MiscAPI) GetAccountVotes(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// This method is no longer supported
	return nil, apierrors.PublicError("get_account_votes is no longer supported")
}

// limitFromQuery extracts and clamps the limit from a query params map.
func limitFromQuery(p map[string]interface{}, def, ubound int) int {
	limit := def
	if l, ok := p["limit"].(float64); ok {
		limit = int(l)
	}
	if limit, err := apierrors.ValidLimit(limit, ubound); err == nil {
		return limit
	}
	return def
}

// truncateBodyFromQuery extracts the optional truncate_body length.
func truncateBodyFromQuery(p map[string]interface{}) int {
	if tb, ok := p["truncate_body"].(float64); ok && tb > 0 {
		return int(tb)
	}
	return 0
}
