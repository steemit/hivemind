package condenser

import (
	"encoding/json"
	"fmt"
	"math"

	"github.com/steemit/hivemind/internal/api/objects"

	"github.com/steemit/hivemind/internal/apierrors"

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
// Top tags among pending posts, with counts and total payouts.
func (t *TagsAPI) GetTrendingTags(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	arr, err := parseListParams(params, 2, 1)
	if err != nil {
		return nil, err
	}

	startTag := paramString(arr, 0)
	if _, err := apierrors.ValidTag(startTag, true); err != nil {
		return nil, err
	}
	limit := 250
	if len(arr) > 1 {
		limit = paramInt(arr, 1)
	}
	if limit, err = apierrors.ValidLimit(limit, 250); err != nil {
		return nil, err
	}

	seek := ""
	args := []interface{}{}
	if startTag != "" {
		seek = `HAVING SUM(payout) <= (
		    SELECT SUM(payout) FROM hive_posts_cache
		     WHERE is_paidout = '0' AND category = ?)`
		args = append(args, startTag)
	}

	sql := `
	  SELECT category,
	         COUNT(*) AS total_posts,
	         SUM(CASE WHEN depth = 0 THEN 1 ELSE 0 END) AS top_posts,
	         SUM(payout) AS total_payouts
	    FROM hive_posts_cache
	   WHERE is_paidout = '0'
	GROUP BY category ` + seek + `
	ORDER BY SUM(payout) DESC
	   LIMIT ?`
	args = append(args, limit)

	var rows []struct {
		Category     string  `gorm:"column:category"`
		TotalPosts   int64   `gorm:"column:total_posts"`
		TopPosts     int64   `gorm:"column:top_posts"`
		TotalPayouts float64 `gorm:"column:total_payouts"`
	}
	if err := t.db.DB.WithContext(ctx.Request.Context()).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]map[string]interface{}, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]interface{}{
			"name":          r.Category,
			"comments":      r.TotalPosts - r.TopPosts,
			"top_posts":     r.TopPosts,
			"total_payouts": fmt.Sprintf("%.3f SBD", r.TotalPayouts),
		})
	}
	return out, nil
}

// GetAccountReputations handles condenser_api.get_account_reputations.
// Each entry uses the legacy `name` key and converts the stored UI score to
// its approximate raw value via rep_to_raw (cursor.py:267 → normalize.py).
func (t *TagsAPI) GetAccountReputations(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var p []interface{}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}

	accountLowerBound := ""
	if len(p) > 0 {
		accountLowerBound, _ = p[0].(string)
	}
	// legacy valid_limit(limit, 1000) over a `limit: int = None` signature:
	// the limit is required (absent/JSON-null -> "limit must be provided"),
	// positive and <= 1000. Resolves the #412-① registered leniency (the
	// old clamp-to-1000 behavior, including its negative-limit guard).
	if len(p) <= 1 || p[1] == nil {
		return nil, apierrors.PublicError("limit must be provided")
	}
	limit := 0
	if l, ok := p[1].(float64); ok {
		limit = int(l)
	}
	validated, err := apierrors.ValidLimit(limit, 1000)
	if err != nil {
		return nil, err
	}
	limit = validated

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
			"name":       r.Name,
			"reputation": repToRawInt(r.Reputation),
		}
	}

	return map[string]interface{}{
		"reputations": reputations,
	}, nil
}

// repToRawInt is legacy rep_to_raw in its integer form: the float conversion
// truncated toward zero (Python int()), with saturation guards for values
// outside int64 range (float(6) DB extremes).
func repToRawInt(rep float64) int64 {
	v := objects.RepToRaw(rep)
	switch {
	case math.IsNaN(v):
		return 0
	case v >= math.MaxInt64:
		return math.MaxInt64
	case v <= math.MinInt64:
		return math.MinInt64
	default:
		return int64(v)
	}
}
