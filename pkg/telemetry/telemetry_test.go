package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/steemit/hivemind/pkg/config"
)

// TestInitDisabledIsTracesKillSwitch pins the HIVE_TELEMETRY_ENABLED
// semantics (2026-10-19 consistency audit, item A1): Init treats Enabled as
// the kill-switch for OTLP trace export only. With Enabled=false no OTLP
// exporter, tracer provider or tracer may be installed, while the Prometheus
// meter provider stays under its own switch (PrometheusEnabled) and keeps
// bridging otel-API metrics onto the shared Registry.
func TestInitDisabledIsTracesKillSwitch(t *testing.T) {
	prevTracer := tracer

	shutdown, err := Init(&config.TelemetryConfig{
		Enabled:           false,
		PrometheusEnabled: true,
		PrometheusPort:    0,
		ServiceName:       "hivemind-test",
		TracesEndpoint:    "localhost:4318",
	})
	if err != nil {
		t.Fatalf("Init with telemetry disabled: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init must return a non-nil (no-op) shutdown function")
	}
	// The no-op shutdown must be safe to call.
	shutdown()

	// The disabled path must not install any tracer.
	if tracer != prevTracer {
		t.Fatal("Init with Enabled=false must not install a tracer")
	}
	if prevTracer == nil {
		_, span := Tracer().Start(context.Background(), "probe")
		if span.IsRecording() {
			t.Error("Tracer() must serve a no-op tracer when telemetry is disabled")
		}
		span.End()
	}

	// Metrics orthogonality: with traces off, the Prometheus meter provider
	// must still be installed, so otel-API metrics keep landing on the
	// shared Registry. This holds whether the once-guarded provider was
	// installed by this Init call or an earlier test in the same process.
	counter, err := otel.Meter("hivemind-test").Int64Counter("hivemind_traces_kill_switch_probe_total")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(context.Background(), 1)

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	found := false
	for _, mf := range families {
		if mf.GetName() == "hivemind_traces_kill_switch_probe_total" {
			found = true
		}
	}
	if !found {
		t.Error("otel metric missing from shared Registry with telemetry disabled (Prometheus path must not be gated by HIVE_TELEMETRY_ENABLED)")
	}
}

// TestInitAllExportersDisabled verifies the fully-disabled configuration
// returns a no-op shutdown without touching any global otel state.
func TestInitAllExportersDisabled(t *testing.T) {
	prevTracer := tracer

	shutdown, err := Init(&config.TelemetryConfig{
		Enabled:           false,
		PrometheusEnabled: false,
		ServiceName:       "hivemind-test",
		TracesEndpoint:    "localhost:4318",
	})
	if err != nil {
		t.Fatalf("Init with all exporters disabled: %v", err)
	}
	if shutdown == nil {
		t.Fatal("Init must return a non-nil (no-op) shutdown function")
	}
	shutdown()

	if tracer != prevTracer {
		t.Fatal("Init with Enabled=false must not install a tracer")
	}
}
