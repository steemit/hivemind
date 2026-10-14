package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/db"
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

	// Wait for interrupt signal or sync error
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
		cancel()
		<-syncDone
		select {
		case err := <-syncErr:
			// A real error during the final batch (context cancellation is
			// mapped to nil inside Run). Not Fatal: Fatal would skip the
			// deferred DB/telemetry teardown, and the committed prefix is
			// resumable -- the error is recorded for the operator.
			logger.Error("Indexer sync returned an error during shutdown", zap.Error(err))
		default:
		}
	case err := <-syncErr:
		logger.Fatal("Indexer sync failed", zap.Error(err))
	}

	logger.Info("Indexer exited")
}
