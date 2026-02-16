package telemetry

import (
	"context"
	"encoding/json"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// StartSpan starts a new span with the given name
func StartSpanWithName(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	tracer := otel.Tracer("hivemind")
	return tracer.Start(ctx, name, opts...)
}

// AddSpanAttributes adds attributes to a span
func AddSpanAttributes(span trace.Span, attrs map[string]string) {
	attributes := make([]attribute.KeyValue, 0, len(attrs))
	for k, v := range attrs {
		attributes = append(attributes, attribute.String(k, v))
	}
	span.SetAttributes(attributes...)
}

// AddSpanIntAttributes adds integer attributes to a span
func AddSpanIntAttributes(span trace.Span, attrs map[string]int) {
	attributes := make([]attribute.KeyValue, 0, len(attrs))
	for k, v := range attrs {
		attributes = append(attributes, attribute.Int(k, v))
	}
	span.SetAttributes(attributes...)
}

// RecordSpanError records an error in a span
func RecordSpanError(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}

// SetSpanSuccess marks a span as successful
func SetSpanSuccess(span trace.Span) {
	span.SetStatus(codes.Ok, "Success")
}

// RecordSpanParams records request parameters as a span event
// This allows viewing params in Jaeger UI under the Logs section
func RecordSpanParams(span trace.Span, params interface{}) {
	if params == nil {
		return
	}

	// Serialize params to JSON for logging
	var paramsJSON string
	if paramsBytes, err := json.Marshal(params); err == nil {
		paramsJSON = string(paramsBytes)
	} else {
		paramsJSON = "failed to serialize params"
	}

	// Add as event (shows in Logs section in Jaeger UI)
	span.AddEvent("request.params",
		trace.WithAttributes(
			attribute.String("params", paramsJSON),
		),
	)
}

// SpanFromContext returns the current span from context
func SpanFromContext(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}

// ContextWithSpan creates a new context with the given span
func ContextWithSpan(ctx context.Context, span trace.Span) context.Context {
	return trace.ContextWithSpan(ctx, span)
}
