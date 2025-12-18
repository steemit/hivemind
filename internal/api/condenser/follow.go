package condenser

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
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
	start := ""
	if len(p) > 1 {
		start, _ = p[1].(string)
	}
	followType := "blog"
	if len(p) > 2 {
		followType, _ = p[2].(string)
	}
	limit := 1000
	if len(p) > 3 {
		if l, ok := p[3].(float64); ok {
			limit = int(l)
		}
	}

	if limit > 1000 {
		limit = 1000
	}
	if limit < 1 {
		limit = 100
	}

	// Get account ID
	accountRepo := db.NewAccountRepository(f.repo)
	acc, err := accountRepo.GetByName(c.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, fmt.Errorf("account not found: %s", account)
	}

	// Get start follower ID if provided
	var startFollowerID *int64
	if start != "" {
		startAcc, err := accountRepo.GetByName(c.Request.Context(), start)
		if err == nil && startAcc != nil {
			startFollowerID = &startAcc.ID
		}
	}

	// Query followers
	followRepo := db.NewFollowRepository(f.repo)
	follows, err := followRepo.GetFollowers(c.Request.Context(), acc.ID, startFollowerID, followType, limit)
	if err != nil {
		return nil, err
	}

	// Build result
	result := make([]interface{}, 0, len(follows))
	for _, follow := range follows {
		// Get follower account name
		followerAcc, err := accountRepo.GetByID(c.Request.Context(), follow.FollowerID)
		if err != nil || followerAcc == nil {
			continue
		}

		// Determine what array based on state
		what := []string{}
		if follow.State&models.FollowStateBlog != 0 {
			what = append(what, "blog")
		}
		if follow.State&models.FollowStateIgnore != 0 {
			what = append(what, "ignore")
		}

		result = append(result, map[string]interface{}{
			"follower":  followerAcc.Name,
			"following": account,
			"what":      what,
		})
	}

	return result, nil
}

// GetFollowing handles condenser_api.get_following
func (f *FollowAPI) GetFollowing(c *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	if len(p) < 1 {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	account, _ := p[0].(string)
	start := ""
	if len(p) > 1 {
		start, _ = p[1].(string)
	}
	followType := "blog"
	if len(p) > 2 {
		followType, _ = p[2].(string)
	}
	limit := 1000
	if len(p) > 3 {
		if l, ok := p[3].(float64); ok {
			limit = int(l)
		}
	}

	if limit > 1000 {
		limit = 1000
	}
	if limit < 1 {
		limit = 100
	}

	// Get account ID
	accountRepo := db.NewAccountRepository(f.repo)
	acc, err := accountRepo.GetByName(c.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, fmt.Errorf("account not found: %s", account)
	}

	// Get start following ID if provided
	var startFollowingID *int64
	if start != "" {
		startAcc, err := accountRepo.GetByName(c.Request.Context(), start)
		if err == nil && startAcc != nil {
			startFollowingID = &startAcc.ID
		}
	}

	// Query following
	followRepo := db.NewFollowRepository(f.repo)
	follows, err := followRepo.GetFollowing(c.Request.Context(), acc.ID, startFollowingID, followType, limit)
	if err != nil {
		return nil, err
	}

	// Build result
	result := make([]interface{}, 0, len(follows))
	for _, follow := range follows {
		// Get following account name
		followingAcc, err := accountRepo.GetByID(c.Request.Context(), follow.FollowingID)
		if err != nil || followingAcc == nil {
			continue
		}

		// Determine what array based on state
		what := []string{}
		if follow.State&models.FollowStateBlog != 0 {
			what = append(what, "blog")
		}
		if follow.State&models.FollowStateIgnore != 0 {
			what = append(what, "ignore")
		}

		result = append(result, map[string]interface{}{
			"follower":  account,
			"following": followingAcc.Name,
			"what":      what,
		})
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

