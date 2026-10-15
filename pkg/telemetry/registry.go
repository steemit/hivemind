package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Registry is THE single Prometheus registry for the process (planned
// PR#430, 05-deploy.md section 1.1). Before this unification the promauto
// metric definitions in metrics.go registered on the client_golang default
// registry while the OpenTelemetry Prometheus exporter created a private
// registry of its own, and neither was ever exposed: there was no /metrics
// endpoint at all, and wiring one to either registry would have missed the
// other's metrics.
//
// Everything now lands here:
//   - the promauto factory below (all hivemind_* metrics in metrics.go and
//     callers of the Record* helpers),
//   - the OpenTelemetry Prometheus exporter (telemetry.Init passes
//     prometheus.WithRegisterer(Registry)), which bridges otel SDK metrics
//     (otel_scope_info, target_info, exporter/reader internals) into the
//     same registry,
//   - the Go runtime and process collectors (go_goroutines, go_memstats_*,
//     go_gc_duration_seconds, process_*) -- the "promhttp default go_* set"
//     the stability observation table (05-deploy.md section 3) scrapes,
//   - the DB connection pool collector (dbpool.go, in_use/idle/wait
//     gauges the same table depends on).
//
// The /metrics endpoint (metricsserver.go) serves exactly this registry.
var Registry = prometheus.NewRegistry()

// metricFactory builds all hivemind_* metrics on Registry. Use it instead
// of the package-level promauto constructors, which would silently target
// the default registry again.
var metricFactory = promauto.With(Registry)

func init() {
	Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}
