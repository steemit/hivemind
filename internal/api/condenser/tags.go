package condenser

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
)

// TagsAPI provides tag-related API methods
type TagsAPI struct {
	repo *db.Repository
	db   *db.DB
}

// NewTagsAPI creates a new tags API
func NewTagsAPI(repo *db.Repository, database *db.DB) *TagsAPI {
	return &TagsAPI{repo: repo, db: database}
}

// GetTrendingTags handles condenser_api.get_trending_tags
func (t *TagsAPI) GetTrendingTags(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	_ = "" // afterTag - TODO: Use for pagination
	if len(p) > 0 {
		_, _ = p[0].(string) // afterTag
	}
	limit := 100
	if len(p) > 1 {
		if l, ok := p[1].(float64); ok {
			limit = int(l)
			if limit > 100 {
				limit = 100
			}
		}
	}

	// Query trending tags from hive_post_tags
	// TODO: Implement proper trending tags query with payout aggregation
	// For now, return empty result
	return []interface{}{}, nil
}

// GetAccountReputations handles condenser_api.get_account_reputations
func (t *TagsAPI) GetAccountReputations(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	accountLowerBound := ""
	if len(p) > 0 {
		accountLowerBound, _ = p[0].(string)
	}
	limit := 1000
	if len(p) > 1 {
		if l, ok := p[1].(float64); ok {
			limit = int(l)
			if limit > 1000 {
				limit = 1000
			}
		}
	}

	// Query account reputations
	query := t.db.DB.WithContext(ctx.Request.Context()).
		Table("hive_accounts").
		Select("name, reputation").
		Order("name ASC").
		Limit(limit)

	if accountLowerBound != "" {
		query = query.Where("name >= ?", accountLowerBound)
	}

	var results []struct {
		Name       string  `gorm:"column:name"`
		Reputation float64 `gorm:"column:reputation"`
	}
	if err := query.Scan(&results).Error; err != nil {
		return nil, err
	}

	reputations := make([]map[string]interface{}, len(results))
	for i, r := range results {
		reputations[i] = map[string]interface{}{
			"account":    r.Name,
			"reputation": r.Reputation,
		}
	}

	return map[string]interface{}{
		"reputations": reputations,
	}, nil
}

