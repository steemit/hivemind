package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/steemit/hivemind/pkg/telemetry"
)

// TracingMiddleware adds OpenTelemetry tracing to requests
func TracingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		tracer := otel.Tracer("hivemind")

		// Create span for request
		ctx, span := tracer.Start(ctx, "hivemind.request",
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()

		// Add request attributes
		span.SetAttributes(
			attribute.String("http.method", c.Request.Method),
			attribute.String("http.path", c.Request.URL.Path),
			attribute.String("http.route", c.FullPath()),
			attribute.String("http.user_agent", c.Request.UserAgent()),
			attribute.String("http.remote_addr", c.ClientIP()),
		)

		// Store span in context
		c.Request = c.Request.WithContext(ctx)

		// Record start time
		startTime := time.Now()

		// Process request
		c.Next()

		// Calculate duration
		duration := time.Since(startTime).Seconds()

		// Add response attributes
		span.SetAttributes(
			attribute.Int("http.status_code", c.Writer.Status()),
			attribute.Float64("http.duration_seconds", duration),
		)

		// Mark span as error if status >= 400
		if c.Writer.Status() >= 400 {
			span.SetStatus(codes.Error, "HTTP error")
		} else {
			span.SetStatus(codes.Ok, "Success")
		}
	}
}

// MetricsMiddleware records Prometheus metrics for requests
func MetricsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		startTime := time.Now()

		// Process request
		c.Next()

		// Record metrics
		duration := time.Since(startTime).Seconds()
		status := "success"
		if c.Writer.Status() >= 400 {
			status = "error"
		}

		// Extract namespace and method from request path
		// For JSON-RPC, this will be updated in the handler
		telemetry.RequestDuration.WithLabelValues("http", c.FullPath()).Observe(duration)
		telemetry.RequestsTotal.WithLabelValues("http", c.FullPath(), status).Inc()
	}
}
