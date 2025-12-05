package hive

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
)

// CommunityAPI provides community-related Hive API methods
type CommunityAPI struct {
	repo *db.Repository
}

// NewCommunityAPI creates a new community API
func NewCommunityAPI(repo *db.Repository) *CommunityAPI {
	return &CommunityAPI{repo: repo}
}

// GetCommunity handles bridge.get_community
func (c *CommunityAPI) GetCommunity(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	name, _ := pMap["name"].(string)
	if name == "" {
		return nil, fmt.Errorf("missing required parameter: name")
	}

	// TODO: Query community from hive_communities
	_ = name

	return map[string]interface{}{
		"name": name,
	}, nil
}

// GetCommunityContext handles bridge.get_community_context
func (c *CommunityAPI) GetCommunityContext(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	name, _ := pMap["name"].(string)
	account, _ := pMap["account"].(string)

	if name == "" || account == "" {
		return nil, fmt.Errorf("missing required parameters: name, account")
	}

	// TODO: Query community context (role, title, subscribed)
	return map[string]interface{}{
		"role":       "member",
		"title":      "",
		"subscribed": false,
	}, nil
}

// ListCommunities handles bridge.list_communities
func (c *CommunityAPI) ListCommunities(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	last := ""
	if l, ok := pMap["last"].(string); ok {
		last = l
	}
	limit := 100
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}

	// TODO: Query communities from hive_communities
	_ = last
	_ = limit

	return []interface{}{}, nil
}

// ListTopCommunities handles bridge.list_top_communities
func (c *CommunityAPI) ListTopCommunities(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	// TODO: Query top communities by rank
	return []interface{}{}, nil
}

// ListPopCommunities handles bridge.list_pop_communities
func (c *CommunityAPI) ListPopCommunities(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	limit := 25
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 25 {
			limit = 25
		}
	}

	// TODO: Query communities by new subscriber count
	_ = limit

	return []interface{}{}, nil
}

// ListCommunityRoles handles bridge.list_community_roles
func (c *CommunityAPI) ListCommunityRoles(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	community, _ := pMap["community"].(string)
	if community == "" {
		return nil, fmt.Errorf("missing required parameter: community")
	}

	// TODO: Query roles from hive_roles
	_ = community

	return []interface{}{}, nil
}

// ListSubscribers handles bridge.list_subscribers
func (c *CommunityAPI) ListSubscribers(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	community, _ := pMap["community"].(string)
	if community == "" {
		return nil, fmt.Errorf("missing required parameter: community")
	}

	// TODO: Query subscribers from hive_subscriptions
	_ = community

	return []interface{}{}, nil
}

// ListAllSubscriptions handles bridge.list_all_subscriptions
func (c *CommunityAPI) ListAllSubscriptions(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	// TODO: Query subscriptions from hive_subscriptions
	_ = account

	return []interface{}{}, nil
}

