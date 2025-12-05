package main

import (
	"context"
	"fmt"
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
	logger.Info("Starting Hivemind API Server")

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

	// Initialize Redis cache
	redisCache, err := cache.New(&cfg.Redis)
	if err != nil {
		logger.Fatal("Failed to initialize Redis cache", zap.Error(err))
	}
	if redisCache != nil {
		defer redisCache.Close()
	}

	// Create Gin router
	if cfg.Logging.Level == "DEBUG" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(gin.Recovery())

	// Setup API routes
	apiRouter := api.NewRouter(database, redisCache)
	apiRouter.SetupRoutes(router)

	// Create HTTP server
	srv := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler: router,
	}

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

	// Graceful shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Fatal("Server forced to shutdown", zap.Error(err))
	}

	logger.Info("Server exited")
}


