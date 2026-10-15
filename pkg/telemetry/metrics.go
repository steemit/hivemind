package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// Request metrics
	RequestsTotal = metricFactory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_requests_total",
			Help: "Total number of JSON-RPC requests",
		},
		[]string{"namespace", "method", "status"},
	)

	RequestDuration = metricFactory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hivemind_request_duration_seconds",
			Help:    "Request latency in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"namespace", "method"},
	)

	RequestErrors = metricFactory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_request_errors_total",
			Help: "Total number of request errors",
		},
		[]string{"namespace", "method", "error_type"},
	)

	// Database metrics
	DBQueriesTotal = metricFactory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_db_queries_total",
			Help: "Total number of database queries",
		},
		[]string{"operation", "table"},
	)

	DBQueryDuration = metricFactory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hivemind_db_query_duration_seconds",
			Help:    "Database query latency in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
		},
		[]string{"operation", "table"},
	)

	// Cache metrics
	CacheOperations = metricFactory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_cache_operations_total",
			Help: "Total number of cache operations",
		},
		[]string{"operation", "result"},
	)

	CacheHitRatio = metricFactory.NewGauge(
		prometheus.GaugeOpts{
			Name: "hivemind_cache_hit_ratio",
			Help: "Cache hit ratio",
		},
	)

	CacheOperationDuration = metricFactory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hivemind_cache_operation_duration_seconds",
			Help:    "Cache operation latency in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
		},
		[]string{"operation"},
	)

	// Post metrics
	PostsFetched = metricFactory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_posts_fetched_total",
			Help: "Total number of posts fetched",
		},
		[]string{"method"},
	)

	// Follow metrics
	FollowsFetched = metricFactory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_follows_fetched_total",
			Help: "Total number of follow relationships fetched",
		},
		[]string{"direction"},
	)

	// Community metrics
	CommunitiesQueried = metricFactory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_communities_queried_total",
			Help: "Total number of community queries",
		},
		[]string{"method"},
	)

	// Notification metrics
	NotificationsFetched = metricFactory.NewCounter(
		prometheus.CounterOpts{
			Name: "hivemind_notifications_fetched_total",
			Help: "Total number of notifications fetched",
		},
	)

	// Indexer throughput metrics (§4.2): blocks/s comes from rate() over
	// the processed-blocks counter; the fetch histogram isolates the steemd
	// GetBlocksRange RPC latency from the transactional write path so a
	// throughput regression is attributable to one side or the other.
	IndexerBlocksProcessed = metricFactory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_indexer_blocks_processed_total",
			Help: "Total blocks committed by the indexer (per tx batch); rate() yields blocks/s",
		},
		[]string{"phase"}, // "initial" | "live"
	)

	IndexerBatchFetchDuration = metricFactory.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "hivemind_indexer_blocks_fetch_duration_seconds",
			Help:    "Wall time of the steemd GetBlocksRange fetches of one tx batch (RPC latency)",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		},
	)

	IndexerBatchWriteDuration = metricFactory.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hivemind_indexer_tx_batch_duration_seconds",
			Help:    "Wall time of a committed tx batch (transaction open through COMMIT; excludes fetch)",
			Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		},
		[]string{"phase"},
	)

	// Poison-block retry counter (PR#411 review registration): counts every
	// failed sync attempt the poison loops retry (per attempt, not per
	// block). With whole-batch rollback a deterministically failing block
	// makes every retry re-fetch and re-process the entire tx batch (see the
	// amplification math in docs/indexer-flow.md), so a sustained nonzero
	// rate() on this counter is the operational signal to open the
	// poison-block runbook.
	IndexerPoisonRetries = metricFactory.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_indexer_poison_retries_total",
			Help: "Failed sync attempts counted by the poison-block loops (per retry attempt, not per block); a sustained nonzero rate pins a deterministically failing block -- see the poison runbook in docs/indexer-flow.md",
		},
		[]string{"phase"}, // "initial" | "live"
	)
)

// RecordRequest records a request metric
func RecordRequest(namespace, method, status string, duration float64) {
	RequestsTotal.WithLabelValues(namespace, method, status).Inc()
	RequestDuration.WithLabelValues(namespace, method).Observe(duration)
}

// RecordError records an error metric
func RecordError(namespace, method, errorType string) {
	RequestErrors.WithLabelValues(namespace, method, errorType).Inc()
}

// RecordDBQuery records a database query metric
func RecordDBQuery(operation, table string, duration float64) {
	DBQueriesTotal.WithLabelValues(operation, table).Inc()
	DBQueryDuration.WithLabelValues(operation, table).Observe(duration)
}

// RecordCacheOperation records a cache operation metric
func RecordCacheOperation(operation, result string, duration float64) {
	CacheOperations.WithLabelValues(operation, result).Inc()
	CacheOperationDuration.WithLabelValues(operation).Observe(duration)
}

// RecordIndexerBatch records one committed indexer tx batch: the block count
// (the blocks/s numerator), the aggregate steemd fetch wall time and the
// transactional write wall time. phase is "initial" or "live" (whether the
// batch ran under initial-sync semantics).
func RecordIndexerBatch(phase string, blocks int, fetchSeconds, writeSeconds float64) {
	IndexerBlocksProcessed.WithLabelValues(phase).Add(float64(blocks))
	IndexerBatchFetchDuration.Observe(fetchSeconds)
	IndexerBatchWriteDuration.WithLabelValues(phase).Observe(writeSeconds)
}

// RecordIndexerPoisonRetry counts one failed sync attempt that the
// poison-block loops will retry. phase is "initial" or "live".
func RecordIndexerPoisonRetry(phase string) {
	IndexerPoisonRetries.WithLabelValues(phase).Inc()
}
