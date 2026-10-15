package condenser

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/logging"
)

// FollowAPI provides follow-related API methods
type FollowAPI struct {
	repo   *db.Repository
	cursor *Cursor
	logger *zap.Logger
}

// NewFollowAPI creates a new follow API
func NewFollowAPI(repo *db.Repository) *FollowAPI {
	return &FollowAPI{
		repo:   repo,
		logger: logging.GetLogger().With(zap.String("component", "condenser-api-follow")),
	}
}

// SetDatabase sets the database connection used by the follow cursor
// queries (created_at DESC pagination, mirroring legacy condenser cursor).
func (f *FollowAPI) SetDatabase(db *gorm.DB) {
	f.cursor = NewCursor(db)
}

// validFollowType defaults an omitted follow type to "blog" and rejects any
// other value, mirroring legacy valid_follow_type (helpers.py).
func validFollowType(followType string) (string, error) {
	if followType == "" {
		return "blog", nil
	}
	if followType != "blog" && followType != "ignore" {
		return "", apierrors.Publicf("invalid follow_type `%s`", followType)
	}
	return followType, nil
}

// legacyFollowerWithReputation builds a follow entry in the legacy condenser
// shape (methods.py _legacy_follower_with_reputation): the `reputation` value
// is the stored UI score from hive_accounts (NOT rep_to_raw — legacy passes
// the column through), and `what` is always a fixed-length ["", ""] pair
// filled from the state bitfield (bit 0 = blog, bit 1 = ignore).
func legacyFollowerWithReputation(follower string, reputation float64, following string, state int16) map[string]interface{} {
	what := []string{"", ""}
	if state&models.FollowStateBlog != 0 {
		what[0] = "blog"
	}
	if state&models.FollowStateIgnore != 0 {
		what[1] = "ignore"
	}
	return map[string]interface{}{
		"follower":   follower,
		"reputation": reputation,
		"following":  following,
		"what":       what,
	}
}

// parseFollowSeekParams parses the common (account, start, follow_type,
// limit) positional list of the follow cursor methods. limit is required
// and validated with legacy valid_limit semantics (methods.py declares
// `limit: int = None`, so an absent/JSON-null limit is "limit must be
// provided" — not a default; #412-① leniency resolved here).
func parseFollowSeekParams(params json.RawMessage) (account, start, followType string, limit int, err error) {
	arr, err := parseListParams(params, 4, 1)
	if err != nil {
		return "", "", "", 0, err
	}

	account = paramString(arr, 0)
	if account, err = apierrors.ValidAccount(account, false); err != nil {
		return "", "", "", 0, err
	}

	start = paramString(arr, 1)
	if start, err = apierrors.ValidAccount(start, true); err != nil {
		return "", "", "", 0, err
	}

	followType = paramString(arr, 2)
	if followType, err = validFollowType(followType); err != nil {
		return "", "", "", 0, err
	}

	if len(arr) <= 3 || arr[3] == nil {
		return "", "", "", 0, apierrors.PublicError("limit must be provided")
	}
	if l, ok := arr[3].(float64); ok {
		limit = int(l)
	}
	if limit, err = apierrors.ValidLimit(limit, 1000); err != nil {
		return "", "", "", 0, err
	}
	return account, start, followType, limit, nil
}

// parseFollowPageParams parses the common (account, page, page_size,
// follow_type) positional list of the by-page follow methods. page follows
// legacy valid_offset (only values below -1 are rejected); page_size
// follows legacy valid_limit(page_size, 100) — required, positive, ubound
// 100 (#412-① registered the old ubound-1000 clamp; resolved here).
func parseFollowPageParams(params json.RawMessage) (account string, page, pageSize int, followType string, err error) {
	arr, err := parseListParams(params, 4, 2)
	if err != nil {
		return "", 0, 0, "", err
	}

	account = paramString(arr, 0)
	if account, err = apierrors.ValidAccount(account, false); err != nil {
		return "", 0, 0, "", err
	}

	page = paramInt(arr, 1)
	// legacy valid_offset: >= -1. -1 itself passes validation upstream but
	// makes legacy fail on `OFFSET -1` (a SQL error); clamping to 0 is the
	// registered Go leniency.
	if page < -1 {
		return "", 0, 0, "", apierrors.PublicError("offset cannot be negative")
	}
	if page < 0 {
		page = 0
	}

	if len(arr) <= 2 || arr[2] == nil {
		return "", 0, 0, "", apierrors.PublicError("limit must be provided")
	}
	pageSize = paramInt(arr, 2)
	if pageSize, err = apierrors.ValidLimit(pageSize, 100); err != nil {
		return "", 0, 0, "", err
	}

	followType = paramString(arr, 3)
	if followType, err = validFollowType(followType); err != nil {
		return "", 0, 0, "", err
	}
	return account, page, pageSize, followType, nil
}

// GetFollowers handles condenser_api.get_followers
func (f *FollowAPI) GetFollowers(c *gin.Context, params json.RawMessage) (interface{}, error) {
	account, start, followType, limit, err := parseFollowSeekParams(params)
	if err != nil {
		return nil, err
	}

	rows, err := f.cursor.GetFollowers(c.Request.Context(), account, start, followType, limit)
	if err != nil {
		return nil, err
	}

	result := make([]interface{}, 0, len(rows))
	for _, row := range rows {
		result = append(result, legacyFollowerWithReputation(row.Name, row.Reputation, account, row.State))
	}
	return result, nil
}

// GetFollowing handles condenser_api.get_following
func (f *FollowAPI) GetFollowing(c *gin.Context, params json.RawMessage) (interface{}, error) {
	account, start, followType, limit, err := parseFollowSeekParams(params)
	if err != nil {
		return nil, err
	}

	rows, err := f.cursor.GetFollowing(c.Request.Context(), account, start, followType, limit)
	if err != nil {
		return nil, err
	}

	result := make([]interface{}, 0, len(rows))
	for _, row := range rows {
		result = append(result, legacyFollowerWithReputation(account, row.Reputation, row.Name, row.State))
	}
	return result, nil
}

// GetFollowCount handles condenser_api.get_follow_count
func (f *FollowAPI) GetFollowCount(c *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 1 {
		return nil, apierrors.PublicError("missing required parameter: account")
	}

	account, _ := p[0].(string)
	if _, err := apierrors.ValidAccount(account, false); err != nil {
		return nil, err
	}

	accountRepo := db.NewAccountRepository(f.repo)
	acc, err := accountRepo.GetByName(c.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	return map[string]interface{}{
		"account":         account,
		"following_count": acc.Following,
		"follower_count":  acc.Followers,
	}, nil
}

// GetRebloggedBy handles condenser_api.get_reblogged_by
func (f *FollowAPI) GetRebloggedBy(c *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 2 {
		return nil, apierrors.PublicError("missing required parameters: author, permlink")
	}

	author, _ := p[0].(string)
	permlink, _ := p[1].(string)
	if _, err := apierrors.ValidAccount(author, false); err != nil {
		return nil, err
	}
	if _, err := apierrors.ValidPermlink(permlink, false); err != nil {
		return nil, err
	}

	// Get post ID
	postRepo := db.NewPostRepository(f.repo)
	post, err := postRepo.GetByAuthorPermlink(c.Request.Context(), author, permlink)
	if err != nil {
		return nil, err
	}
	if post == nil {
		return []string{}, nil
	}

	// Get reblog accounts
	reblogRepo := db.NewReblogRepository(f.repo)
	accounts, err := reblogRepo.GetAccountNamesByPostID(c.Request.Context(), post.ID)
	if err != nil {
		return nil, err
	}

	return accounts, nil
}

// GetFollowersByPage handles condenser_api.get_followers_by_page
func (f *FollowAPI) GetFollowersByPage(c *gin.Context, params json.RawMessage) (interface{}, error) {
	account, page, pageSize, followType, err := parseFollowPageParams(params)
	if err != nil {
		return nil, err
	}

	rows, err := f.cursor.GetFollowersByPage(c.Request.Context(), account, page, pageSize, followType)
	if err != nil {
		return nil, err
	}

	result := make([]interface{}, 0, len(rows))
	for _, row := range rows {
		result = append(result, legacyFollowerWithReputation(row.Name, row.Reputation, account, row.State))
	}
	return result, nil
}

// GetFollowingByPage handles condenser_api.get_following_by_page
func (f *FollowAPI) GetFollowingByPage(c *gin.Context, params json.RawMessage) (interface{}, error) {
	account, page, pageSize, followType, err := parseFollowPageParams(params)
	if err != nil {
		return nil, err
	}

	rows, err := f.cursor.GetFollowingByPage(c.Request.Context(), account, page, pageSize, followType)
	if err != nil {
		return nil, err
	}

	result := make([]interface{}, 0, len(rows))
	for _, row := range rows {
		result = append(result, legacyFollowerWithReputation(account, row.Reputation, row.Name, row.State))
	}
	return result, nil
}
