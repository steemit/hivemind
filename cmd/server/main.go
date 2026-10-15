package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/api"
	"github.com/steemit/hivemind/internal/cache"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/health"
	"github.com/steemit/hivemind/internal/middleware"
	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// defaultShutdownGrace is the fallback for a zero-value
// ServerConfig.ShutdownGracePeriod (hand-built configs; Load always fills
// the HIVE_HTTP_SHUTDOWN_GRACE default of 45s).
const defaultShutdownGrace = 45 * time.Second

// requestAbortDrain is the bounded wait after in-flight request contexts
// are canceled at the end of the grace period: handlers observing the
// cancellation (DB calls abort through the context) unwind in
// milliseconds; anything past this window is stuck in non-cancellable
// work and only a process exit can reclaim it. A var so tests can
// shorten it.
var requestAbortDrain = 5 * time.Second

func main() {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	if err := logging.InitLogger(&cfg.Logging); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	// Sync() returns an error (e.g. EINVAL on non-tty stderr) that has no
	// recovery path at process exit; discard it explicitly for errcheck.
	defer func() { _ = logging.GetLogger().Sync() }()

	logger := logging.GetLogger()
	logger.Info("Starting Hivemind API Server")

	// Initialize telemetry
	telemetryShutdown, err := telemetry.Init(&cfg.Telemetry)
	if err != nil {
		logger.Fatal("Failed to initialize telemetry", zap.Error(err))
	}
	defer telemetryShutdown()

	// Observability HTTP listener: /metrics + /debug/pprof on the
	// dedicated HIVE_PROMETHEUS_PORT (planned PR#430, 05-deploy.md
	// sections 1.1/1.3). /health stays on the main API port (legacy
	// serve.py placement, EB health check path).
	metricsServer, err := telemetry.NewMetricsServer(telemetry.MetricsServerConfig{
		Enabled:      cfg.Telemetry.PrometheusEnabled,
		Port:         cfg.Telemetry.PrometheusPort,
		PprofEnabled: cfg.Telemetry.PprofEnabled,
	}, nil)
	if err != nil {
		logger.Fatal("Failed to start metrics server", zap.Error(err))
	}
	if metricsServer != nil {
		go func() {
			if err := metricsServer.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("Metrics server failed", zap.Error(err))
			}
		}()
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := metricsServer.Shutdown(ctx); err != nil {
				logger.Error("Metrics server shutdown failed", zap.Error(err))
			}
		}()
	}

	// Install the shared mutes list (irredeemables) used by API object
	// builders (stats.hide/blacklists). Empty URL = disabled list.
	steem.SetSharedMutes(steem.NewMutes(cfg.Steem.MutedAccountsURL))

	// Steemd client: backs condenser_api.get_transaction (block lookups).
	// Client creation is lazy (no connection is made until called), so the
	// server still starts when steemd is unreachable.
	steemd, err := steem.New(&cfg.Steem)
	if err != nil {
		logger.Fatal("Failed to create steemd client", zap.Error(err))
	}

	// Initialize database
	database, err := db.New(&cfg.Database, cfg.Logging.Level)
	if err != nil {
		logger.Fatal("Failed to initialize database", zap.Error(err))
	}
	defer database.Close()

	// Expose the connection pool gauges (hivemind_db_pool_in_use/idle/...)
	// on the shared metrics registry (05-deploy.md section 3 observation).
	if sqlDB, err := database.DB.DB(); err == nil {
		if err := telemetry.RegisterDBPoolMetrics(sqlDB); err != nil {
			logger.Error("Failed to register DB pool metrics", zap.Error(err))
		}
	} else {
		logger.Error("Failed to get underlying sql.DB for pool metrics", zap.Error(err))
	}

	// Run schema migrations (skippable via HIVE_DB_MIGRATE=false).
	// The server only needs read access; migrations also run in the indexer,
	// but running here too keeps dev/test environments self-bootstrapping.
	if cfg.Database.Migrate {
		force := 0
		if cfg.Database.MigrateForceBaseline {
			force = 1
		}
		if err := db.RunMigrations(cfg.Database.URL, force); err != nil {
			logger.Fatal("Failed to run database migrations", zap.Error(err))
		}
	} else {
		logger.Info("Database migrations skipped (HIVE_DB_MIGRATE=false)")
	}

	// Initialize Redis cache
	redisCache, err := cache.New(&cfg.Redis)
	if err != nil {
		logger.Fatal("Failed to initialize Redis cache", zap.Error(err))
	}
	if redisCache != nil {
		defer redisCache.Close()
	}

	// Real health checks (planned PR#430, 05-deploy.md section 1.2): the
	// checker probes the head block through a dedicated single-connection
	// pool (the legacy isolated health engine) so a saturated main pool
	// cannot starve /health.
	headQuerier, err := health.NewDBHeadQuerier(cfg.Database.URL)
	if err != nil {
		logger.Fatal("Failed to open health check pool", zap.Error(err))
	}
	defer headQuerier.Close()
	healthChecker := health.NewChecker(headQuerier, cfg.Telemetry.HealthMaxHeadAge, false)

	// Create Gin router
	if cfg.Logging.Level == "DEBUG" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()

	// Add middlewares
	router.Use(gin.Recovery())
	router.Use(middleware.TracingMiddleware())
	router.Use(middleware.MetricsMiddleware())

	// Health endpoints on the main API port (legacy serve.py placement):
	// /health 500s when the DB is unreachable or (for this reader process)
	// the head block lags past HIVE_HEALTH_MAX_HEAD_AGE; /head_age answers
	// the lag in seconds for external monitors.
	router.GET("/health", gin.WrapH(http.HandlerFunc(healthChecker.HandleHealth)))
	router.GET("/.well-known/healthcheck.json", gin.WrapH(http.HandlerFunc(healthChecker.HandleHealth)))
	router.GET("/head_age", gin.WrapH(http.HandlerFunc(healthChecker.HandleHeadAge)))

	// Setup API routes
	apiRouter := api.NewRouter(database, redisCache, steemd, cfg.Indexer.RecommendCommunities, cfg.Server.MaxBodyBytes)
	apiRouter.SetupRoutes(router)

	// rootCtx is the base context of every request (via BaseContext);
	// canceling it aborts in-flight requests when the shutdown grace
	// expires (section 1.4).
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Create HTTP server with timeouts to prevent slow-client goroutine leaks
	// (see connection-pool-exhaustion-go-rewrite.md Issue 4).
	srv := newHTTPServer(&cfg.Server, router, rootCtx)

	// Start server in goroutine
	go func() {
		logger.Info("Server starting", zap.String("address", srv.Addr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("Server failed to start", zap.Error(err))
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	// Graceful shutdown (section 1.4): the grace window defaults to 45s,
	// comfortably above the 30s statement timeout, so every DB-bounded
	// in-flight request completes naturally; only stragglers past the
	// window get their contexts canceled.
	if err := shutdownServer(srv, cfg.Server.ShutdownGracePeriod, rootCancel, logger); err != nil {
		logger.Error("Server shutdown failed", zap.Error(err))
	}

	logger.Info("Server exited")
}

// newHTTPServer builds the API HTTP server. BaseContext ties every request
// context to rootCtx so shutdown can abort in-flight requests.
func newHTTPServer(cfg *config.ServerConfig, handler http.Handler, rootCtx context.Context) *http.Server {
	return &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:           handler,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		BaseContext:       func(net.Listener) context.Context { return rootCtx },
		ReadHeaderTimeout: 5 * time.Second,
	}
}

// shutdownServer drains the server for up to grace (<= 0 selects
// defaultShutdownGrace); if requests are still in flight then, it cancels
// the request root context (aborting ctx-aware handlers) and waits a
// bounded drain before returning.
func shutdownServer(srv *http.Server, grace time.Duration, cancelRequests context.CancelFunc, logger *zap.Logger) error {
	if grace <= 0 {
		grace = defaultShutdownGrace
	}
	ctx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()

	err := srv.Shutdown(ctx)
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}

	// Grace expired with requests in flight: cancel their contexts so the
	// deferred pool/telemetry teardown does not race live handlers.
	logger.Warn("Shutdown grace expired with requests in flight; canceling in-flight request contexts",
		zap.Duration("grace", grace))
	cancelRequests()

	// Bounded unwind wait: give ctx-observing handlers time to return
	// after the cancellation before the process tears down shared state.
	time.Sleep(requestAbortDrain)
	return err
}
