package telemetry

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"

	"github.com/steemit/hivemind/pkg/logging"
)

// MetricsServerConfig configures the observability HTTP listener (planned
// PR#430, 05-deploy.md sections 1.1/1.3). Both binaries expose it on the
// dedicated HIVE_PROMETHEUS_PORT (server 9090 / indexer 9091), separate
// from the JSON-RPC port, so metrics and pprof scrapes never contend with
// API traffic.
type MetricsServerConfig struct {
	// Enabled gates the whole listener (HIVE_PROMETHEUS_ENABLED).
	Enabled bool

	// Port is the listen port; 0 binds a random free port (tests).
	Port int

	// PprofEnabled mounts /debug/pprof/* on the same listener
	// (HIVE_PPROF_ENABLED, default on). The listener is expected to be
	// internal-only (EB security groups / compose network); it must not be
	// exposed on a public load balancer.
	PprofEnabled bool
}

// MetricsServer is the standalone observability HTTP server serving, on one
// internal port:
//
//	/metrics        Prometheus scrape endpoint (the shared Registry)
//	/debug/pprof/*  Go pprof indices and profiles (optional)
//	<any extra>     e.g. /health and /head_age for the indexer (section 1.2)
//
// The API server keeps /health on its main JSON-RPC port (legacy serve.py
// placement, EB health check path) and uses this server for /metrics and
// pprof only.
type MetricsServer struct {
	srv *http.Server
	ln  net.Listener
}

// NewMetricsServer builds the handler and binds the listener immediately
// (a taken port fails here, before Serve is called). extraRoutes mounts
// additional handlers, e.g. the indexer's /health and /head_age; paths
// colliding with /metrics or /debug/pprof/* are rejected.
func NewMetricsServer(cfg MetricsServerConfig, extraRoutes map[string]http.Handler) (*MetricsServer, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(Registry, promhttp.HandlerOpts{}))

	if cfg.PprofEnabled {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}

	for path, h := range extraRoutes {
		if path == "/metrics" || strings.HasPrefix(path, "/debug/pprof/") {
			return nil, fmt.Errorf("metrics server extra route %q collides with a built-in route", path)
		}
		mux.Handle(path, h)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		return nil, fmt.Errorf("metrics server failed to bind port %d: %w", cfg.Port, err)
	}

	srv := &http.Server{
		Handler:     mux,
		ReadTimeout: 10 * time.Second,
		// 75s (not the 30s of an API server): /debug/pprof/profile streams
		// a CPU profile for the requested window (default 30s, commonly
		// requested up to 60s) and only finishes writing when the window
		// closes. A 30s WriteTimeout cut every longer profile off mid-body
		// (PR#430 audit item 5); 75s covers a 60s window plus scrape
		// overhead. This listener is internal-only, so the generous bound
		// is not externally reachable.
		WriteTimeout:      75 * time.Second,
		IdleTimeout:       60 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
	}

	logging.GetLogger().Info("Metrics server listening",
		zap.String("addr", ln.Addr().String()),
		zap.Bool("pprof", cfg.PprofEnabled),
	)

	return &MetricsServer{srv: srv, ln: ln}, nil
}

// Serve runs the accept loop on the already-bound listener. It returns
// http.ErrServerClosed after Shutdown.
func (s *MetricsServer) Serve() error {
	if s == nil {
		return nil
	}
	return s.srv.Serve(s.ln)
}

// Addr returns the bound address (useful when Port=0 picked a random port).
func (s *MetricsServer) Addr() net.Addr {
	if s == nil {
		return nil
	}
	return s.ln.Addr()
}

// Shutdown gracefully stops the metrics server.
func (s *MetricsServer) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}
