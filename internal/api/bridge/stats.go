package bridge

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// StatsAPI provides stats-related Bridge API methods
type StatsAPI struct {
	repo *db.Repository
	db   *db.DB
}

// NewStatsAPI creates a new stats API
func NewStatsAPI(repo *db.Repository, database *db.DB) *StatsAPI {
	return &StatsAPI{repo: repo, db: database}
}

// PayoutStat represents a payout statistics entry
type PayoutStat struct {
	Name   string  `json:"name"`
	Amount float64 `json:"amount"`
	Count  int64   `json:"count"`
}

// GetPayoutStats handles bridge.get_payout_stats
// Returns payout statistics for communities or accounts
func (s *StatsAPI) GetPayoutStats(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_payout_stats")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	// Extract parameters
	community, _ := pMap["community"].(string)
	limit := 25
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
	}
	if limit > 100 {
		limit = 100
	}
	if limit < 1 {
		limit = 25
	}

	telemetry.AddSpanAttributes(span, map[string]string{
		"community": community,
	})

	// Get payout stats from database
	var stats []PayoutStat

	query := s.db.DB.Table("hive_posts").
		Select("category as name, SUM(payout) as amount, COUNT(*) as count").
		Where("is_deleted = ?", false).
		Where("depth = ?", 0).
		Where("payout > ?", 0).
		Where("cashout_time > ?", time.Now())

	if community != "" {
		query = query.Where("community = ?", community)
	}

	err := query.
		Group("category").
		Order("amount DESC").
		Limit(limit).
		Scan(&stats).Error

	if err != nil && err != gorm.ErrRecordNotFound {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	telemetry.SetSpanSuccess(span)

	// Format response
	result := make([]interface{}, 0, len(stats))
	for _, stat := range stats {
		result = append(result, []interface{}{
			stat.Name,
			stat.Amount,
			stat.Count,
		})
	}

	return result, nil
}
