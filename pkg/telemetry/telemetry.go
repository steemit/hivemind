package telemetry

import (
	"context"
	"fmt"
	"net/url"
	"strings"
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
	"go.uber.org/zap"

	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
)

var (
	tracer trace.Tracer
)

// Init initializes OpenTelemetry with OTLP and Prometheus exporters
func Init(cfg *config.TelemetryConfig) (func(), error) {
	if !cfg.Enabled {
		logging.GetLogger().Info("Telemetry disabled")
		return func() {}, nil
	}

	ctx := context.Background()

	// Create resource
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

	// Setup trace exporter using OTLP
	traceExporter, err := otlptrace.New(ctx,
		otlptracehttp.NewClient(
			otlptracehttp.WithEndpoint(endpoint),
			otlptracehttp.WithInsecure(), // Use TLS in production
		),
	)
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

	logging.GetLogger().Info("OTLP trace exporter initialized", zap.String("endpoint", endpoint))

	// Initialize metric provider with Prometheus exporter
	if cfg.PrometheusEnabled {
		exporter, err := prometheus.New()
		if err != nil {
			return nil, fmt.Errorf("failed to create Prometheus exporter: %w", err)
		}

		mp := metric.NewMeterProvider(
			metric.WithReader(exporter),
			metric.WithResource(res),
		)

		otel.SetMeterProvider(mp)
		shutdownFuncs = append(shutdownFuncs, mp.Shutdown)

		logging.GetLogger().Info("Prometheus exporter initialized", zap.Int("port", cfg.PrometheusPort))
	}

	// Set global propagator
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Create tracer
	tracer = otel.Tracer(cfg.ServiceName)

	// Return shutdown function
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

// Tracer returns the global tracer
func Tracer() trace.Tracer {
	if tracer == nil {
		// Fallback to no-op tracer
		return trace.NewNoopTracerProvider().Tracer("hivemind")
	}
	return tracer
}

// StartSpan starts a new span
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return Tracer().Start(ctx, name, opts...)
}
