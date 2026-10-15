package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/steemit/hivemind/pkg/telemetry"
)

// Tracing/Metrics middleware tests (planned PR#440, KR6; 06-quality section
// 4 "middleware/telemetry: zero tests"). The Tracing middleware must:
//
//   - start a SERVER span named hivemind.request per request and attach it
//     to the request context, so downstream handlers (the JSON-RPC layer)
//     can retrieve and parent to it (span propagation);
//   - record http.method/path/route/user_agent/remote_addr request
//     attributes and http.status_code/duration response attributes;
//   - mark the span Error for HTTP >= 400 and Ok otherwise.
//
// The middleware performs no W3C traceparent extraction (it parents to
// whatever ctx the server hander gives it); that evaluation is registered in
// the PR#440 report rather than implemented here.

// installTestTracerProvider swaps the global tracer provider for one backed
// by an in-memory span recorder, returning the recorder and a restore func.
func installTestTracerProvider(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return recorder
}

// spanAttr returns the span's attribute value for key ("" when absent).
func spanAttr(span sdktrace.ReadOnlySpan, key string) string {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.Emit()
		}
	}
	return ""
}

func TestTracingMiddlewareSpanPropagation(t *testing.T) {
	recorder := installTestTracerProvider(t)
	gin.SetMode(gin.TestMode)

	// The handler observes the propagated span: the middleware must have
	// stored a live span whose context the handler can parent to.
	var handlerSC trace.SpanContext
	r := gin.New()
	r.Use(TracingMiddleware())
	r.POST("/rpc", func(c *gin.Context) {
		handlerSC = trace.SpanContextFromContext(c.Request.Context())
		c.Writer.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/rpc?x=1", nil)
	req.Header.Set("User-Agent", "tracing-mw-test/1.0")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (test harness sanity)", w.Code)
	}
	if !handlerSC.IsValid() {
		t.Fatal("handler saw no valid span context: TracingMiddleware did not propagate the span through the request context")
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want exactly 1 per request", len(spans))
	}
	span := spans[0]
	if span.Name() != "hivemind.request" {
		t.Errorf("span name = %q, want hivemind.request", span.Name())
	}
	if span.SpanKind() != trace.SpanKindServer {
		t.Errorf("span kind = %v, want SpanKindServer", span.SpanKind())
	}
	// Propagation equivalence: the context the handler saw must carry the
	// recorded span's trace and span ids (TraceState is not comparable, so
	// the identity is asserted on the ids).
	if handlerSC.TraceID() != span.SpanContext().TraceID() || handlerSC.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("handler span ids (trace=%s span=%s) != recorded span ids (trace=%s span=%s): the span ended is not the one propagated to handlers",
			handlerSC.TraceID(), handlerSC.SpanID(), span.SpanContext().TraceID(), span.SpanContext().SpanID())
	}

	for key, want := range map[string]string{
		"http.method":      http.MethodPost,
		"http.path":        "/rpc",
		"http.route":       "/rpc",
		"http.user_agent":  "tracing-mw-test/1.0",
		"http.remote_addr": "192.0.2.1", // httptest.DefaultRemoteAddr host (ClientIP strips the port)
		"http.status_code": "200",
	} {
		if got := spanAttr(span, key); got != want {
			t.Errorf("span attribute %s = %q, want %q", key, got, want)
		}
	}
	if got := spanAttr(span, "http.duration_seconds"); got == "" {
		t.Error("span attribute http.duration_seconds missing")
	}
	if span.Status().Code != codes.Ok {
		t.Errorf("span status = %v, want Ok for a 2xx response", span.Status().Code)
	}
}

func TestTracingMiddlewareErrorStatus(t *testing.T) {
	recorder := installTestTracerProvider(t)
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(TracingMiddleware())
	r.GET("/boom", func(c *gin.Context) {
		c.Writer.WriteHeader(http.StatusInternalServerError)
	})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (test harness sanity)", w.Code)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want 1", len(spans))
	}
	if code := spans[0].Status().Code; code != codes.Error {
		t.Errorf("span status = %v, want Error for a 5xx response", code)
	}
	if got := spanAttr(spans[0], "http.status_code"); got != "500" {
		t.Errorf("span attribute http.status_code = %q, want 500", got)
	}
}

// counterVecValue reads one series of a CounterVec (same dto technique as the
// indexer tests; the testutil package would add a module for one assertion).
func counterVecValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("write counter metric: %v", err)
	}
	return m.GetCounter().GetValue()
}

// histogramVecCount reads one series' sample count of a HistogramVec.
func histogramVecCount(t *testing.T, o prometheus.Observer) float64 {
	t.Helper()
	h, ok := o.(prometheus.Histogram)
	if !ok {
		t.Fatal("observer is not a histogram")
	}
	m := &dto.Metric{}
	if err := h.Write(m); err != nil {
		t.Fatalf("write histogram metric: %v", err)
	}
	return float64(m.GetHistogram().GetSampleCount())
}

// TestMetricsMiddlewareRecordsRequestMetrics pins the Metrics middleware: one
// request increments hivemind_requests_total{namespace=http,method=<route>,
// status=success|error} and observes hivemind_request_duration_seconds. The
// metrics are process-global prometheus collectors, so the assertions read
// deltas around a single request.
func TestMetricsMiddlewareRecordsRequestMetrics(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(MetricsMiddleware())
	r.GET("/ok", func(c *gin.Context) { c.Writer.WriteHeader(http.StatusOK) })
	r.GET("/teapot", func(c *gin.Context) { c.Writer.WriteHeader(http.StatusTeapot) })

	successBefore := counterVecValue(t, telemetry.RequestsTotal.WithLabelValues("http", "/ok", "success"))
	errorBefore := counterVecValue(t, telemetry.RequestsTotal.WithLabelValues("http", "/teapot", "error"))
	durationBefore := histogramVecCount(t, telemetry.RequestDuration.WithLabelValues("http", "/ok"))

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ok", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/teapot", nil))

	if got := counterVecValue(t, telemetry.RequestsTotal.WithLabelValues("http", "/ok", "success")) - successBefore; got != 1 {
		t.Errorf("hivemind_requests_total{http,/ok,success} delta = %v, want 1", got)
	}
	if got := counterVecValue(t, telemetry.RequestsTotal.WithLabelValues("http", "/teapot", "error")) - errorBefore; got != 1 {
		t.Errorf("hivemind_requests_total{http,/teapot,error} delta = %v, want 1 (status >= 400 classified as error)", got)
	}
	if got := histogramVecCount(t, telemetry.RequestDuration.WithLabelValues("http", "/ok")) - durationBefore; got != 1 {
		t.Errorf("hivemind_request_duration_seconds{http,/ok} sample-count delta = %v, want 1", got)
	}

	// 4xx/5xx must NOT land in the success series.
	if got := counterVecValue(t, telemetry.RequestsTotal.WithLabelValues("http", "/teapot", "success")); got != 0 {
		t.Errorf("hivemind_requests_total{http,/teapot,success} = %v, want 0 (errors never counted as success)", got)
	}
}

// TestTracingAndMetricsMiddlewareChain smoke-tests the real mounting order
// (tracing outermost, metrics innermost) used by the API server: the span
// covers the metrics recording and both middlewares coexist on one request.
func TestTracingAndMetricsMiddlewareChain(t *testing.T) {
	recorder := installTestTracerProvider(t)
	gin.SetMode(gin.TestMode)

	r := gin.New()
	r.Use(TracingMiddleware(), MetricsMiddleware())
	var handlerSC trace.SpanContext
	r.POST("/", func(c *gin.Context) {
		handlerSC = trace.SpanContextFromContext(c.Request.Context())
		_, _ = c.Writer.Write([]byte("ok"))
	})

	before := counterVecValue(t, telemetry.RequestsTotal.WithLabelValues("http", "/", "success"))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !handlerSC.IsValid() {
		t.Fatal("handler saw no valid span context under the mounted middleware chain")
	}
	if len(recorder.Ended()) != 1 {
		t.Errorf("recorded %d spans, want 1", len(recorder.Ended()))
	}
	if got := counterVecValue(t, telemetry.RequestsTotal.WithLabelValues("http", "/", "success")) - before; got != 1 {
		t.Errorf("hivemind_requests_total{http,/,success} delta = %v, want 1", got)
	}
}
