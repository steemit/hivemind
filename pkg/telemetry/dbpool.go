package telemetry

import (
	"database/sql"

	"github.com/prometheus/client_golang/prometheus"
)

// DBStatsSource is implemented by *sql.DB; the interface keeps the
// collector testable with a fake.
type DBStatsSource interface {
	Stats() sql.DBStats
}

// dbPoolCollector exposes database/sql connection pool statistics
// (planned PR#430, 05-deploy.md section 1.1: the 7-day stability
// observation table requires in_use/idle pool metrics with
// "no exhaustion" as the pass line; section 4 adds a pool-watermark
// alarm on top, the Q3 incident lesson).
type dbPoolCollector struct {
	src DBStatsSource
}

// Pool metric descriptors. WaitCount/WaitDuration are cumulative since
// pool creation, hence counter type; the rest are point-in-time gauges.
var (
	dbPoolMaxOpenDesc = prometheus.NewDesc(
		"hivemind_db_pool_max_open_connections",
		"Maximum number of open connections the pool may hold (SetMaxOpenConns).",
		nil, nil,
	)
	dbPoolOpenDesc = prometheus.NewDesc(
		"hivemind_db_pool_open_connections",
		"Total connections currently held by the pool (in use + idle).",
		nil, nil,
	)
	dbPoolInUseDesc = prometheus.NewDesc(
		"hivemind_db_pool_in_use_connections",
		"Connections currently checked out by queries (pool saturation signal).",
		nil, nil,
	)
	dbPoolIdleDesc = prometheus.NewDesc(
		"hivemind_db_pool_idle_connections",
		"Idle connections currently held by the pool.",
		nil, nil,
	)
	dbPoolWaitCountDesc = prometheus.NewDesc(
		"hivemind_db_pool_wait_count_total",
		"Total number of connections waited for because the pool was at MaxOpenConns.",
		nil, nil,
	)
	dbPoolWaitDurationDesc = prometheus.NewDesc(
		"hivemind_db_pool_wait_duration_seconds_total",
		"Total time callers waited for a free connection.",
		nil, nil,
	)
)

// Describe implements prometheus.Collector.
func (c *dbPoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- dbPoolMaxOpenDesc
	ch <- dbPoolOpenDesc
	ch <- dbPoolInUseDesc
	ch <- dbPoolIdleDesc
	ch <- dbPoolWaitCountDesc
	ch <- dbPoolWaitDurationDesc
}

// Collect implements prometheus.Collector.
func (c *dbPoolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.src.Stats()
	ch <- prometheus.MustNewConstMetric(dbPoolMaxOpenDesc, prometheus.GaugeValue, float64(s.MaxOpenConnections))
	ch <- prometheus.MustNewConstMetric(dbPoolOpenDesc, prometheus.GaugeValue, float64(s.OpenConnections))
	ch <- prometheus.MustNewConstMetric(dbPoolInUseDesc, prometheus.GaugeValue, float64(s.InUse))
	ch <- prometheus.MustNewConstMetric(dbPoolIdleDesc, prometheus.GaugeValue, float64(s.Idle))
	ch <- prometheus.MustNewConstMetric(dbPoolWaitCountDesc, prometheus.CounterValue, float64(s.WaitCount))
	ch <- prometheus.MustNewConstMetric(dbPoolWaitDurationDesc, prometheus.CounterValue, s.WaitDuration.Seconds())
}

// dbPoolRegistered guards the one allowed registration: each process owns
// exactly one main pool, and a second Register would fail with a duplicate
// collector error.
var dbPoolRegistered bool

// RegisterDBPoolMetrics registers the connection pool collector for src on
// the shared Registry. Call once per process, after the database pool is
// created. Repeated calls are no-ops.
func RegisterDBPoolMetrics(src DBStatsSource) error {
	if dbPoolRegistered {
		return nil
	}
	if err := Registry.Register(&dbPoolCollector{src: src}); err != nil {
		return err
	}
	dbPoolRegistered = true
	return nil
}
