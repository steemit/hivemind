package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"

	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
)

var (
	tracer trace.Tracer
)

// meterProviderOnce guards the otel Prometheus exporter registration. The
// exporter registers a collector on the shared Registry; a second
// registration attempt would fail with "duplicate metrics collector
// registration attempted", so repeated Init calls (tests, embedded use)
// keep the first provider. The resource (service name) of the first call
// is the one that labels the metrics.
var meterProviderOnce sync.Once

// Init initializes OpenTelemetry with OTLP and Prometheus exporters.
//
// cfg.Enabled (HIVE_TELEMETRY_ENABLED) is the kill-switch for OTLP trace
// export ONLY: when false, no trace exporter, tracer provider or propagator
// is installed (Tracer() serves a no-op tracer) and nothing is shipped to
// the OTLP collector. The Prometheus meter provider is orthogonal and
// answers solely to cfg.PrometheusEnabled (HIVE_PROMETHEUS_ENABLED): traces
// can be off while metrics stay on, and vice versa.
func Init(cfg *config.TelemetryConfig) (func(), error) {
	ctx := context.Background()

	// Create resource. It labels both the trace exporter and the meter
	// provider, so it is built regardless of which exporters are enabled.
	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion("0.1.0"),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	var shutdownFuncs []func(context.Context) error

	if !cfg.Enabled {
		logging.GetLogger().Info("OTLP trace export disabled (HIVE_TELEMETRY_ENABLED=false); Prometheus metrics unaffected")
	} else {
		// Parse endpoint URL to extract host:port
		// WithEndpoint expects host:port format, not full URL
		endpoint := cfg.TracesEndpoint
		if strings.HasPrefix(cfg.TracesEndpoint, "http://") || strings.HasPrefix(cfg.TracesEndpoint, "https://") {
			parsedURL, err := url.Parse(cfg.TracesEndpoint)
			if err != nil {
				return nil, fmt.Errorf("failed to parse endpoint URL: %w", err)
			}
			endpoint = parsedURL.Host
			if parsedURL.Port() == "" {
				// Default port based on scheme
				if parsedURL.Scheme == "https" {
					endpoint += ":4318"
				} else {
					endpoint += ":4318"
				}
			}
		}

		// Setup trace exporter using OTLP. TLS is configurable (HIVE_TRACES_TLS,
		// planned PR#432, 05-deploy.md section 1.6): the default (false) keeps
		// insecure OTLP/HTTP for the local compose stack (in-network jaeger
		// collector); production TLS endpoints set it to true.
		otlpOpts := []otlptracehttp.Option{otlptracehttp.WithEndpoint(endpoint)}
		if !cfg.TracesTLS {
			otlpOpts = append(otlpOpts, otlptracehttp.WithInsecure())
		}
		traceExporter, err := otlptrace.New(ctx, otlptracehttp.NewClient(otlpOpts...))
		if err != nil {
			return nil, fmt.Errorf("failed to create OTLP trace exporter: %w", err)
		}

		// Setup trace provider
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(traceExporter),
			sdktrace.WithResource(res),
		)

		otel.SetTracerProvider(tp)
		shutdownFuncs = append(shutdownFuncs, tp.Shutdown)

		logging.GetLogger().Info("OTLP trace exporter initialized",
			zap.String("endpoint", endpoint),
			zap.Bool("tls", cfg.TracesTLS))

		// Set global propagator
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
			propagation.TraceContext{},
			propagation.Baggage{},
		))

		// Create tracer
		tracer = otel.Tracer(cfg.ServiceName)
	}

	// Initialize metric provider with Prometheus exporter, bridged onto the
	// shared Registry (registry.go) so otel SDK metrics and the promauto
	// hivemind_* metrics are scraped from the same /metrics endpoint.
	// Gated only by HIVE_PROMETHEUS_ENABLED -- deliberately NOT by the
	// traces kill-switch above (the two switches are orthogonal).
	if cfg.PrometheusEnabled {
		if err := initMeterProvider(res); err != nil {
			return nil, fmt.Errorf("failed to initialize Prometheus exporter: %w", err)
		}
		logging.GetLogger().Info("Prometheus exporter initialized", zap.Int("port", cfg.PrometheusPort))
	}

	// Return shutdown function (a no-op when nothing was initialized)
	shutdown := func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		for _, fn := range shutdownFuncs {
			// Create a wrapper that uses the shutdown context
			if err := func() error {
				ctx, cancel := context.WithTimeout(shutdownCtx, 3*time.Second)
				defer cancel()
				return fn(ctx)
			}(); err != nil {
				logging.GetLogger().Error("Error shutting down telemetry", zap.Error(err))
			}
		}
	}

	return shutdown, nil
}

// initMeterProvider creates the otel Prometheus exporter bound to the
// shared Registry (registry.go) and installs the global meter provider.
// See meterProviderOnce for the single-registration guard. The provider
// needs no shutdown hook (unlike the trace batcher): the Prometheus
// exporter is pull-based, so nothing is buffered to flush.
func initMeterProvider(res *resource.Resource) error {
	var err error
	meterProviderOnce.Do(func() {
		var exporter *prometheus.Exporter
		exporter, err = prometheus.New(prometheus.WithRegisterer(Registry))
		if err != nil {
			return
		}

		opts := []metric.Option{metric.WithReader(exporter)}
		if res != nil {
			opts = append(opts, metric.WithResource(res))
		}
		mp := metric.NewMeterProvider(opts...)

		otel.SetMeterProvider(mp)
	})
	return err
}

// Tracer returns the global tracer
func Tracer() trace.Tracer {
	if tracer == nil {
		// Fallback to no-op tracer
		return noop.NewTracerProvider().Tracer("hivemind")
	}
	return tracer
}

// StartSpan starts a new span
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}
