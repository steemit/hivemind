package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/health"
	"github.com/steemit/hivemind/internal/indexer"
	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
	"github.com/steemit/hivemind/pkg/telemetry"
)

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
	logger.Info("Starting Hivemind Indexer")

	// Initialize telemetry
	telemetryShutdown, err := telemetry.Init(&cfg.Telemetry)
	if err != nil {
		logger.Fatal("Failed to initialize telemetry", zap.Error(err))
	}
	defer telemetryShutdown()

	// Initialize database
	database, err := db.New(&cfg.Database, cfg.Logging.Level)
	if err != nil {
		logger.Fatal("Failed to initialize database", zap.Error(err))
	}
	defer database.Close()

	// Expose the connection pool gauges on the shared metrics registry
	// (planned PR#430; the indexer's write path is what saturates the pool,
	// so its water level is the primary signal).
	if sqlDB, err := database.DB.DB(); err == nil {
		if err := telemetry.RegisterDBPoolMetrics(sqlDB); err != nil {
			logger.Error("Failed to register DB pool metrics", zap.Error(err))
		}
	} else {
		logger.Error("Failed to get underlying sql.DB for pool metrics", zap.Error(err))
	}

	// Install the shared mutes list (irredeemables) used by CachedPost
	// notifications (PR#6d). Empty URL = disabled list.
	steem.SetSharedMutes(steem.NewMutes(cfg.Steem.MutedAccountsURL))

	// Run schema migrations before indexing.
	//
	// For a NEW database, forceVersion=0 runs the baseline migration (0001)
	// from scratch, building the full schema (DB_VERSION 29 shape).
	//
	// For an EXISTING database already provisioned by the Python legacy,
	// set HIVE_DB_MIGRATE_FORCE=1 to stamp version 1 without running DDL,
	// after which subsequent versions (if any) apply normally.
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

	// Initialize Steem client
	steemClient, err := steem.New(&cfg.Steem)
	if err != nil {
		logger.Fatal("Failed to initialize Steem client", zap.Error(err))
	}

	// Minimal observability HTTP listener (planned PR#430, 05-deploy.md
	// sections 1.1-1.3): /metrics + /debug/pprof + /health + /head_age on
	// the dedicated HIVE_PROMETHEUS_PORT (9091 in compose). The legacy
	// indexer served no HTTP at all; this listener keeps the EB health
	// check and the sync-lag sampling working for the writer process.
	//
	// The checker is flagged as a sync service: like the legacy is_syncer
	// branch, a writer lagging during a multi-hour catch-up is healthy, so
	// /health only fails when the database is unreachable (head lag is
	// watched through /head_age and the block-rate metrics instead).
	headQuerier, err := health.NewDBHeadQuerier(cfg.Database.URL)
	if err != nil {
		logger.Fatal("Failed to open health check pool", zap.Error(err))
	}
	defer headQuerier.Close()
	healthChecker := health.NewChecker(headQuerier, cfg.Telemetry.HealthMaxHeadAge, true)

	metricsServer, err := telemetry.NewMetricsServer(telemetry.MetricsServerConfig{
		Enabled:      cfg.Telemetry.PrometheusEnabled,
		Port:         cfg.Telemetry.PrometheusPort,
		PprofEnabled: cfg.Telemetry.PprofEnabled,
	}, map[string]http.Handler{
		"/health":   http.HandlerFunc(healthChecker.HandleHealth),
		"/head_age": http.HandlerFunc(healthChecker.HandleHeadAge),
	})
	if err != nil {
		logger.Fatal("Failed to create metrics server", zap.Error(err))
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

	// Create sync manager
	syncManager, err := indexer.NewSync(cfg, database, steemClient)
	if err != nil {
		logger.Fatal("Failed to create sync manager", zap.Error(err))
	}

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run returns when done (clean exit, TEST_MAX_BLOCK stop) or on error.
	// syncDone closes in ALL cases so the shutdown path below can wait for
	// the in-flight work to settle.
	syncDone := make(chan struct{})
	syncErr := make(chan error, 1)
	go func() {
		defer close(syncDone)
		logger.Info("Starting indexer sync...")
		if err := syncManager.Run(ctx); err != nil {
			syncErr <- err
		}
	}()

	logger.Info("Indexer initialized, waiting for interrupt...")

	// Wait for interrupt signal, sync error, or clean completion.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-quit:
		// Graceful shutdown (§2.9): cancel the context, then WAIT for Run
		// to return before the deferred teardown (telemetry, DB pool) runs.
		// Run's cancellation boundary sits between block batches: the
		// in-flight batch completes -- its block transactions commit and
		// the dirty queues flush (post cache, follow deltas, accounts) --
		// so no queued work is lost and block/delta state stays consistent.
		// Closing the DB pool before Run returns would abort exactly those
		// final transactions.
		logger.Info("Shutdown signal received; waiting for in-flight block batch to complete...")
		// A SECOND signal force-exits (128+SIGINT convention): graceful
		// shutdown legitimately waits minutes for the in-flight batch or a
		// maintenance transaction (Rebuild/ForceRecount run unbounded by
		// design); an operator who repeats the signal means "exit now",
		// and this way they never have to reach for SIGKILL, which would
		// skip even this log line. What committed stays committed -- the
		// persisted markers make the next boot resume cleanly.
		go func() {
			<-quit
			logger.Warn("Second shutdown signal received; forcing immediate exit")
			os.Exit(130)
		}()
		cancel()
		<-syncDone
		select {
		case err := <-syncErr:
			// A real error during the final batch (context cancellation is
			// mapped to a clean nil inside Run). Not Fatal: Fatal would
			// skip the deferred DB/telemetry teardown, and the committed
			// prefix is resumable -- the error is recorded for the operator.
			logger.Error("Indexer sync returned an error during shutdown", zap.Error(err))
		default:
		}
	case err := <-syncErr:
		logger.Fatal("Indexer sync failed", zap.Error(err))
	case <-syncDone:
		// Run returned on its own without reporting an error on syncErr --
		// the TEST_MAX_BLOCK stop point (or, after a shutdown-signal race,
		// a cancellation mapped to nil inside Run). Without this branch the
		// process kept waiting for a signal forever: nothing writes to
		// syncErr when Run succeeds, so the original select spun idle
		// (PR#407 review). Check syncErr anyway (Run may have returned an
		// error, in which case both channels are ready) and exit non-zero
		// on it, zero otherwise.
		select {
		case err := <-syncErr:
			logger.Error("Indexer sync exited with an error", zap.Error(err))
			os.Exit(1)
		default:
		}
	}

	logger.Info("Indexer exited")
}
