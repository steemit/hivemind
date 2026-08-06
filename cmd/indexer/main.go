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
	defer logging.GetLogger().Sync()

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
		if cfg.Indexer.MigrateForceBaseline {
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

	// Start sync in goroutine
	syncErr := make(chan error, 1)
	go func() {
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
		logger.Info("Shutting down indexer...")
		cancel()
	case err := <-syncErr:
		logger.Fatal("Indexer sync failed", zap.Error(err))
	}

	logger.Info("Indexer exited")
}
