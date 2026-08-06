package db

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
)

// zapWriter adapts zap.Logger to logger.Writer interface
type zapWriter struct {
	logger *zap.Logger
}

func (w *zapWriter) Printf(format string, args ...interface{}) {
	w.logger.Sugar().Infof(format, args...)
}

// DB wraps GORM database connection
type DB struct {
	*gorm.DB
}

// New creates a new database connection
func New(cfg *config.DatabaseConfig, logLevel string) (*DB, error) {
	// Parse log level
	var gormLogLevel logger.LogLevel
	switch logLevel {
	case "DEBUG", "debug":
		gormLogLevel = logger.Info
	case "INFO", "info":
		gormLogLevel = logger.Warn
	case "WARN", "warn", "WARNING", "warning":
		gormLogLevel = logger.Error
	case "ERROR", "error":
		gormLogLevel = logger.Silent
	default:
		gormLogLevel = logger.Warn
	}

	// Configure GORM logger
	// Create a writer adapter for zap logger
	zapLogger := logging.GetLogger()
	writer := &zapWriter{logger: zapLogger}

	gormLogger := logger.New(
		writer,
		logger.Config{
			SlowThreshold:             time.Second,
			LogLevel:                  gormLogLevel,
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	// Open database connection.
	// statement_timeout is injected into the DSN so it applies to EVERY pooled
	// connection (database/sql reuses connections; a post-connect SET would
	// only affect one). pgx parses this as a per-connection runtime param.
	dsn := buildDSN(cfg)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormLogger,
		NowFunc: func() time.Time {
			return time.Now().UTC()
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Get underlying sql.DB for connection pool configuration
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}

	// Configure connection pool from config (avoids the connection pool
	// exhaustion seen in the Python legacy; see
	// connection-pool-exhaustion-go-rewrite.md Issue 1).
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	logging.GetLogger().Info("Database connection established",
		zap.Int("max_open_conns", cfg.MaxOpenConns),
		zap.Int("max_idle_conns", cfg.MaxIdleConns),
		zap.Duration("statement_timeout", cfg.StatementTimeout),
	)

	return &DB{DB: db}, nil
}

// buildDSN appends per-connection runtime params to the database URL.
// pgx (used by the GORM postgres driver) parses `statement_timeout` as a
// session default that applies to every pooled connection, which prevents a
// single slow query from holding a connection indefinitely.
func buildDSN(cfg *config.DatabaseConfig) string {
	if cfg.StatementTimeout <= 0 {
		return cfg.URL
	}
	st := cfg.StatementTimeout.Milliseconds()
	sep := "&"
	if !contains(cfg.URL, "?") {
		sep = "?"
	}
	return fmt.Sprintf("%s%soptions=-c%%20statement_timeout%%3D%d", cfg.URL, sep, st)
}

// contains is a tiny helper to avoid pulling strings just for one call.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Close closes the database connection
func (d *DB) Close() error {
	sqlDB, err := d.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Health checks database health
func (d *DB) Health(ctx context.Context) error {
	sqlDB, err := d.DB.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}
