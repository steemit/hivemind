package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// Request metrics
	RequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_requests_total",
			Help: "Total number of JSON-RPC requests",
		},
		[]string{"namespace", "method", "status"},
	)

	RequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hivemind_request_duration_seconds",
			Help:    "Request latency in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"namespace", "method"},
	)

	RequestErrors = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_request_errors_total",
			Help: "Total number of request errors",
		},
		[]string{"namespace", "method", "error_type"},
	)

	// Database metrics
	DBQueriesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_db_queries_total",
			Help: "Total number of database queries",
		},
		[]string{"operation", "table"},
	)

	DBQueryDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hivemind_db_query_duration_seconds",
			Help:    "Database query latency in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0},
		},
		[]string{"operation", "table"},
	)

	// Cache metrics
	CacheOperations = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_cache_operations_total",
			Help: "Total number of cache operations",
		},
		[]string{"operation", "result"},
	)

	CacheHitRatio = promauto.NewGauge(
		prometheus.GaugeOpts{
			Name: "hivemind_cache_hit_ratio",
			Help: "Cache hit ratio",
		},
	)

	CacheOperationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "hivemind_cache_operation_duration_seconds",
			Help:    "Cache operation latency in seconds",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
		},
		[]string{"operation"},
	)

	// Post metrics
	PostsFetched = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_posts_fetched_total",
			Help: "Total number of posts fetched",
		},
		[]string{"method"},
	)

	// Follow metrics
	FollowsFetched = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_follows_fetched_total",
			Help: "Total number of follow relationships fetched",
		},
		[]string{"direction"},
	)

	// Community metrics
	CommunitiesQueried = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "hivemind_communities_queried_total",
			Help: "Total number of community queries",
		},
		[]string{"method"},
	)

	// Notification metrics
	NotificationsFetched = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "hivemind_notifications_fetched_total",
			Help: "Total number of notifications fetched",
		},
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
