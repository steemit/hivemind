package bridge

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// payoutStatsTTL bounds how long a query-time payout aggregation is served
// from the in-process cache. Legacy rebuilds its payout_stats temp table at
// most once per hour (common/payout_stats.py:36); the same cadence is kept
// here without introducing a background writer (Q4 decision: query-time
// aggregation instead of a materialized table -- the indexer must not gain a
// new write responsibility for a read-only stats endpoint).
const payoutStatsTTL = 60 * time.Minute

// payoutStatsDefaultLimit and payoutStatsMaxLimit mirror legacy
// valid_limit(limit, 250) with the signature default 250
// (hive_api/stats.py:21-24). The aggregation is cached at the max bound and
// sliced per request: ORDER BY payout DESC LIMIT k selects a prefix of
// LIMIT payoutStatsMaxLimit for any k <= 250 (tie order among equal payouts
// is unspecified in legacy too, so a cached prefix is an equivalent answer).
const (
	payoutStatsDefaultLimit = 250
	payoutStatsMaxLimit     = 250
)

// StatsAPI provides stats-related Bridge API methods
type StatsAPI struct {
	repo *db.Repository
	db   *db.DB

	// In-process result cache. get_payout_stats has no observer dimension,
	// so the cached payload is pure shared data -- the Q3 invariant
	// ("observer-personalized data never enters a shared cache") holds by
	// construction.
	cacheMu     sync.Mutex
	cachedAt    time.Time
	cachedStats *payoutStats
}

// NewStatsAPI creates a new stats API
func NewStatsAPI(repo *db.Repository, database *db.DB) *StatsAPI {
	return &StatsAPI{repo: repo, db: database}
}

// payoutStats is the cached payout aggregation: the top-limit item rows plus
// the limit-independent totals.
type payoutStats struct {
	items []interface{}
	total float64
	blogs float64
}

// payoutStatsRow is one row of the legacy payout_stats read join: either a
// community aggregate (name set, author NULL) or an author-level
// non-community row (name NULL, author set).
type payoutStatsRow struct {
	Name    *string
	Title   string
	Author  *string
	Payout  float64
	Posts   int64
	Authors *int64
}

// payoutStatsCTE reproduces the legacy payout_stats materialization
// (common/payout_stats.py:39-59) as a query-time CTE over hive_posts_cache:
//
//	branch 1: per (community_id, author) sums -- unpaid posts only
//	branch 2: per community_id totals with distinct-author counts
//
// is_paidout='0' is the exact legacy filter (boolean column; PG accepts the
// '0' literal). No depth filter: comments count toward payouts, as in legacy.
const payoutStatsCTE = `
WITH payout_stats AS (
    SELECT community_id, author, SUM(payout) AS payout, COUNT(*) AS posts, NULL::bigint AS authors
      FROM hive_posts_cache
     WHERE is_paidout = '0'
  GROUP BY community_id, author
  UNION ALL
    SELECT community_id, NULL::varchar, SUM(payout) AS payout, COUNT(*) AS posts, COUNT(DISTINCT author) AS authors
      FROM hive_posts_cache
     WHERE is_paidout = '0'
  GROUP BY community_id
)`

// GetPayoutStats handles bridge.get_payout_stats
// Returns payout statistics for building a treemap, aggregated at query time
// from unpaid hive_posts_cache rows and cached in-process for an hour.
// Response shape mirrors legacy hive_api/stats.py:49 -- {items,total,blogs}
// where each item is [url, label, payout, posts, authors(null for
// author-level rows)].
func (s *StatsAPI) GetPayoutStats(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	_, span := telemetry.StartSpanWithName(ctx.Request.Context(), "bridge.get_payout_stats")
	defer span.End()

	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	// Legacy valid_limit(limit, 250); signature default 250
	// (hive_api/stats.py:24).
	limit, err := apierrors.ValidQueryLimit(pMap, payoutStatsDefaultLimit, payoutStatsMaxLimit)
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	telemetry.AddSpanAttributes(span, map[string]string{
		"limit": strconv.Itoa(limit),
	})

	stats, err := s.payoutStats(ctx.Request.Context())
	if err != nil {
		telemetry.RecordSpanError(span, err)
		return nil, err
	}

	telemetry.SetSpanSuccess(span)

	items := stats.items
	if len(items) > limit {
		items = items[:limit]
	}

	return map[string]interface{}{
		"items": items,
		"total": stats.total,
		"blogs": stats.blogs,
	}, nil
}

// payoutStats returns the cached payout aggregation, refreshing it when older
// than payoutStatsTTL. The mutex serializes refreshes: concurrent misses
// block on one aggregation instead of stampeding the database.
func (s *StatsAPI) payoutStats(ctx context.Context) (*payoutStats, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cachedStats != nil && time.Since(s.cachedAt) < payoutStatsTTL {
		return s.cachedStats, nil
	}

	stats, err := s.queryPayoutStats(ctx)
	if err != nil {
		return nil, err
	}
	s.cachedStats = stats
	s.cachedAt = time.Now()
	return stats, nil
}

// queryPayoutStats runs the two legacy reads against the CTE: the item join
// (stats.py:29-37, row shaping in _row stats.py:10-18) and the totals
// (stats.py:42-47). COALESCE covers the empty-table case where legacy would
// raise float(None); with no unpaid posts, {items:[], total:0, blogs:0} is
// the faithful degenerate answer.
func (s *StatsAPI) queryPayoutStats(ctx context.Context) (*payoutStats, error) {
	gdb := s.db.DB.WithContext(ctx)

	var rows []payoutStatsRow
	if err := gdb.Raw(payoutStatsCTE+`
	SELECT hc.name, hc.title, ps.author, ps.payout, ps.posts, ps.authors
	  FROM payout_stats ps
	  LEFT JOIN hive_communities hc ON hc.id = ps.community_id
	 WHERE (ps.community_id IS NULL AND ps.author IS NOT NULL)
	    OR (ps.community_id IS NOT NULL AND ps.author IS NULL)
	 ORDER BY ps.payout DESC
	 LIMIT ?`, payoutStatsMaxLimit).Scan(&rows).Error; err != nil {
		return nil, err
	}

	// Legacy _row (stats.py:10-18): community rows take (name, title) as
	// (url, label); author rows take '@' + author for both.
	items := make([]interface{}, 0, len(rows))
	for _, row := range rows {
		var url, label string
		if row.Name != nil && *row.Name != "" {
			url = *row.Name
			label = row.Title
		} else if row.Author != nil {
			url = "@" + *row.Author
			label = url
		} else {
			// Unreachable per the WHERE clause; both keys NULL is the
			// blogs-aggregate row which the read query excludes.
			continue
		}
		var authors interface{}
		if row.Authors != nil {
			authors = *row.Authors
		}
		items = append(items, []interface{}{url, label, row.Payout, row.Posts, authors})
	}

	var totals struct {
		Total float64
		Blogs float64
	}
	if err := gdb.Raw(payoutStatsCTE + `
	SELECT COALESCE(SUM(payout) FILTER (WHERE author IS NULL), 0) AS total,
	       COALESCE(SUM(payout) FILTER (WHERE community_id IS NULL AND author IS NULL), 0) AS blogs
	  FROM payout_stats`).Scan(&totals).Error; err != nil {
		return nil, err
	}

	return &payoutStats{items: items, total: totals.Total, blogs: totals.Blogs}, nil
}
