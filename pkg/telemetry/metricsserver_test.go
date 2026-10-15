package telemetry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
)

// TestMetricsServerSmoke is the PR#430 acceptance check for section 1.1:
// scraping the metrics port must expose both the promauto hivemind_*
// metrics and the Go runtime (go_*) family, and (section 1.3) the pprof
// index must be reachable on the same port.
func TestMetricsServerSmoke(t *testing.T) {
	// Give the CounterVec a series so it is present in the exposition.
	RecordRequest("condenser_api", "get_content", "success", 0.01)

	srv, err := NewMetricsServer(MetricsServerConfig{
		Enabled:      true,
		Port:         0, // ephemeral
		PprofEnabled: true,
	}, map[string]http.Handler{
		"/stub": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("stub-ok"))
		}),
	})
	if err != nil {
		t.Fatalf("NewMetricsServer: %v", err)
	}
	go func() {
		if err := srv.Serve(); err != nil && err != http.ErrServerClosed {
			t.Errorf("Serve: %v", err)
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	base := fmt.Sprintf("http://%s", srv.Addr().String())

	// /metrics smoke: custom metric series + go_* runtime family.
	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read /metrics body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", resp.StatusCode)
	}
	text := string(body)
	for _, want := range []string{
		`hivemind_requests_total{method="get_content",namespace="condenser_api",status="success"}`,
		"go_goroutines",
		"go_gc_duration_seconds",
		"go_memstats_alloc_bytes",
		"process_resident_memory_bytes",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("/metrics body missing %q (registry unification or runtime collectors broken)", want)
		}
	}

	// pprof index reachable (section 1.3).
	resp, err = http.Get(base + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/debug/pprof/ status = %d, want 200", resp.StatusCode)
	}

	// Extra routes (the indexer's /health + /head_age ride this listener).
	resp, err = http.Get(base + "/stub")
	if err != nil {
		t.Fatalf("GET /stub: %v", err)
	}
	defer resp.Body.Close()
	got, _ := io.ReadAll(resp.Body)
	if string(got) != "stub-ok" {
		t.Errorf("extra route body = %q, want stub-ok", got)
	}
}

// TestMetricsServerPprofDisabled verifies HIVE_PPROF_ENABLED=false removes
// the /debug/pprof routes while /metrics keeps working.
func TestMetricsServerPprofDisabled(t *testing.T) {
	srv, err := NewMetricsServer(MetricsServerConfig{
		Enabled:      true,
		Port:         0,
		PprofEnabled: false,
	}, nil)
	if err != nil {
		t.Fatalf("NewMetricsServer: %v", err)
	}
	go func() {
		if err := srv.Serve(); err != nil && err != http.ErrServerClosed {
			t.Errorf("Serve: %v", err)
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	base := fmt.Sprintf("http://%s", srv.Addr().String())

	resp, err := http.Get(base + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/metrics status = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Get(base + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/debug/pprof/ status = %d, want 404 when pprof is disabled", resp.StatusCode)
	}
}

// TestMetricsServerDisabled verifies the disabled configuration returns a
// nil server whose methods are safe no-ops (the mains rely on this).
func TestMetricsServerDisabled(t *testing.T) {
	srv, err := NewMetricsServer(MetricsServerConfig{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("NewMetricsServer: %v", err)
	}
	if srv != nil {
		t.Fatalf("disabled config must return a nil server, got %v", srv)
	}
	if err := srv.Serve(); err != nil {
		t.Errorf("nil server Serve: %v", err)
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Errorf("nil server Shutdown: %v", err)
	}
	if srv.Addr() != nil {
		t.Errorf("nil server Addr = %v, want nil", srv.Addr())
	}
}

// TestOtelBridgeOnSharedRegistry proves the registry unification: metrics
// recorded through the otel API (which the OTLP/trace side and any future
// otel instrumentation use) land on the SAME registry as the promauto
// hivemind_* metrics, so one /metrics scrape carries both.
func TestOtelBridgeOnSharedRegistry(t *testing.T) {
	res, err := sdkresource.New(context.Background(),
		sdkresource.WithSchemaURL("https://opentelemetry.io/schemas/1.21.0"),
	)
	if err != nil {
		t.Fatalf("resource: %v", err)
	}
	if err := initMeterProvider(res); err != nil {
		t.Fatalf("initMeterProvider: %v", err)
	}

	meter := otel.Meter("hivemind-test")
	counter, err := meter.Int64Counter("hivemind_otel_bridge_probe_total")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 7)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var haveBridge, havePromauto bool
	for _, mf := range families {
		if mf.GetName() == "hivemind_otel_bridge_probe_total" {
			haveBridge = true
		}
		if mf.GetName() == "hivemind_requests_total" {
			havePromauto = true
		}
	}
	if !haveBridge {
		t.Error("otel-recorded metric hivemind_otel_bridge_probe_total missing from shared Registry (exporter not bridged)")
	}
	if !havePromauto {
		t.Error("promauto metric hivemind_requests_total missing from shared Registry")
	}

	// Repeated init (the once-guard) must not error with a duplicate
	// registration on the shared registry.
	if err := initMeterProvider(res); err != nil {
		t.Errorf("second initMeterProvider should be a no-op, got: %v", err)
	}
}
