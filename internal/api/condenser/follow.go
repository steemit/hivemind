package condenser

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/pkg/logging"
)

// FollowAPI provides follow-related API methods
type FollowAPI struct {
	repo   *db.Repository
	logger *zap.Logger
}

// NewFollowAPI creates a new follow API
func NewFollowAPI(repo *db.Repository) *FollowAPI {
	return &FollowAPI{
		repo:   repo,
		logger: logging.GetLogger().With(zap.String("component", "condenser-api-follow")),
	}
}

// GetFollowers handles condenser_api.get_followers
func (f *FollowAPI) GetFollowers(c *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 1 {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	account, _ := p[0].(string)
	_ = account // TODO: Use account for query
	start := ""
	if len(p) > 1 {
		start, _ = p[1].(string)
	}
	_ = start // TODO: Use start for pagination
	followType := "blog"
	if len(p) > 2 {
		followType, _ = p[2].(string)
	}
	_ = followType // TODO: Use followType for filtering
	limit := 1000
	if len(p) > 3 {
		if l, ok := p[3].(float64); ok {
			limit = int(l)
		}
	}
	_ = limit // TODO: Use limit for query

	// TODO: Implement get_followers query
	// This should query hive_follows table with proper filtering

	return []interface{}{}, nil
}

// GetFollowing handles condenser_api.get_following
func (f *FollowAPI) GetFollowing(c *gin.Context, params json.RawMessage) (interface{}, error) {
	// Similar to GetFollowers but reversed
	return f.GetFollowers(c, params)
}

// GetFollowCount handles condenser_api.get_follow_count
func (f *FollowAPI) GetFollowCount(c *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 1 {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	account, _ := p[0].(string)

	accountRepo := db.NewAccountRepository(f.repo)
	acc, err := accountRepo.GetByName(c.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, fmt.Errorf("account not found: %s", account)
	}

	return map[string]interface{}{
		"account":        account,
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
		return nil, fmt.Errorf("missing required parameters: author, permlink")
	}

	author, _ := p[0].(string)
	permlink, _ := p[1].(string)
	_ = author   // TODO: Use author for query
	_ = permlink // TODO: Use permlink for query

	// TODO: Query hive_reblogs table to get accounts that reblogged this post
	return []string{}, nil
}

