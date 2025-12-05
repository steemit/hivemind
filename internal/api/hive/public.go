package hive

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
)

// PublicAPI provides public Hive API methods
type PublicAPI struct {
	repo *db.Repository
}

// NewPublicAPI creates a new public API
func NewPublicAPI(repo *db.Repository) *PublicAPI {
	return &PublicAPI{repo: repo}
}

// GetAccount handles hive_api.get_account
func (p *PublicAPI) GetAccount(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	name, _ := pMap["name"].(string)
	observer, _ := pMap["observer"].(string)

	if name == "" {
		return nil, fmt.Errorf("missing required parameter: name")
	}

	accountRepo := db.NewAccountRepository(p.repo)
	account, err := accountRepo.GetByName(ctx.Request.Context(), name)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, nil
	}

	// TODO: Build full account object with observer context
	_ = observer

	return map[string]interface{}{
		"id":          account.ID,
		"name":        account.Name,
		"display_name": account.DisplayName.String,
		"about":       account.About.String,
		"reputation":  account.Reputation,
		"followers":   account.Followers,
		"following":   account.Following,
	}, nil
}

// GetAccounts handles hive_api.get_accounts
func (p *PublicAPI) GetAccounts(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	namesInterface, ok := pMap["names"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("missing or invalid parameter: names")
	}

	if len(namesInterface) > 100 {
		return nil, fmt.Errorf("too many names (max 100)")
	}

	names := make([]string, len(namesInterface))
	for i, n := range namesInterface {
		names[i], _ = n.(string)
	}

	accountRepo := db.NewAccountRepository(p.repo)
	accounts, err := accountRepo.GetByNames(ctx.Request.Context(), names)
	if err != nil {
		return nil, err
	}

	result := make([]interface{}, len(accounts))
	for i, acc := range accounts {
		result[i] = map[string]interface{}{
			"id":   acc.ID,
			"name": acc.Name,
		}
	}

	return result, nil
}

// ListFollowers handles hive_api.list_followers
func (p *PublicAPI) ListFollowers(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	start := ""
	if s, ok := pMap["start"].(string); ok {
		start = s
	}
	limit := 50
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}

	// TODO: Query followers from hive_follows
	_ = start
	_ = limit

	return []interface{}{}, nil
}

// ListFollowing handles hive_api.list_following
func (p *PublicAPI) ListFollowing(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// Similar to ListFollowers but reversed
	return p.ListFollowers(ctx, params)
}

// ListAllMuted handles hive_api.list_all_muted
func (p *PublicAPI) ListAllMuted(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	// TODO: Query muted accounts from hive_follows where state includes ignore
	return []string{}, nil
}

// ListAccountBlog handles hive_api.list_account_blog
func (p *PublicAPI) ListAccountBlog(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	limit := 10
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 50 {
			limit = 50
		}
	}

	// TODO: Query from feed cache
	_ = limit

	return []interface{}{}, nil
}

// ListAccountPosts handles hive_api.list_account_posts
func (p *PublicAPI) ListAccountPosts(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// Similar to ListAccountBlog but only posts (no reblogs)
	return p.ListAccountBlog(ctx, params)
}

// ListAccountFeed handles hive_api.list_account_feed
func (p *PublicAPI) ListAccountFeed(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// Similar to ListAccountBlog but personalized feed
	return p.ListAccountBlog(ctx, params)
}

