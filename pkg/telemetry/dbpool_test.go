package telemetry

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeStatsSource struct{ stats sql.DBStats }

func (f *fakeStatsSource) Stats() sql.DBStats { return f.stats }

// TestDBPoolMetrics pins the pool gauge family the 7-day observation
// table depends on (in_use / idle / wait; 05-deploy.md section 3).
func TestDBPoolMetrics(t *testing.T) {
	fake := &fakeStatsSource{stats: sql.DBStats{
		MaxOpenConnections: 25,
		OpenConnections:    12,
		InUse:              5,
		Idle:               7,
		WaitCount:          3,
		WaitDuration:       1500 * time.Millisecond,
	}}

	if err := RegisterDBPoolMetrics(fake); err != nil {
		t.Fatalf("RegisterDBPoolMetrics: %v", err)
	}

	expected := `
# HELP hivemind_db_pool_idle_connections Idle connections currently held by the pool.
# TYPE hivemind_db_pool_idle_connections gauge
hivemind_db_pool_idle_connections 7
# HELP hivemind_db_pool_in_use_connections Connections currently checked out by queries (pool saturation signal).
# TYPE hivemind_db_pool_in_use_connections gauge
hivemind_db_pool_in_use_connections 5
# HELP hivemind_db_pool_max_open_connections Maximum number of open connections the pool may hold (SetMaxOpenConns).
# TYPE hivemind_db_pool_max_open_connections gauge
hivemind_db_pool_max_open_connections 25
# HELP hivemind_db_pool_open_connections Total connections currently held by the pool (in use + idle).
# TYPE hivemind_db_pool_open_connections gauge
hivemind_db_pool_open_connections 12
# HELP hivemind_db_pool_wait_count_total Total number of connections waited for because the pool was at MaxOpenConns.
# TYPE hivemind_db_pool_wait_count_total counter
hivemind_db_pool_wait_count_total 3
# HELP hivemind_db_pool_wait_duration_seconds_total Total time callers waited for a free connection.
# TYPE hivemind_db_pool_wait_duration_seconds_total counter
hivemind_db_pool_wait_duration_seconds_total 1.5
`
	if err := testutil.GatherAndCompare(Registry, strings.NewReader(expected),
		"hivemind_db_pool_idle_connections",
		"hivemind_db_pool_in_use_connections",
		"hivemind_db_pool_max_open_connections",
		"hivemind_db_pool_open_connections",
		"hivemind_db_pool_wait_count_total",
		"hivemind_db_pool_wait_duration_seconds_total",
	); err != nil {
		t.Fatalf("GatherAndCompare: %v", err)
	}
}

// TestRegisterDBPoolMetricsIdempotent: a second registration must be a
// no-op, not a duplicate-collector error.
func TestRegisterDBPoolMetricsIdempotent(t *testing.T) {
	if err := RegisterDBPoolMetrics(&fakeStatsSource{}); err != nil {
		t.Fatalf("repeated RegisterDBPoolMetrics: %v", err)
	}
}
