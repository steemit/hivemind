package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	// Save original env
	originalDB := os.Getenv("HIVE_DATABASE_URL")
	defer func() {
		if originalDB != "" {
			os.Setenv("HIVE_DATABASE_URL", originalDB)
		} else {
			os.Unsetenv("HIVE_DATABASE_URL")
		}
	}()

	// Test with environment variable
	os.Setenv("HIVE_DATABASE_URL", "postgresql://test:test@localhost:5432/testdb")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Database.URL != "postgresql://test:test@localhost:5432/testdb" {
		t.Errorf("Expected database URL from env, got: %s", cfg.Database.URL)
	}
}

func TestValidate(t *testing.T) {
	cfg := &Config{
		Database: DatabaseConfig{
			URL:              "postgresql://test@localhost/test",
			MaxOpenConns:     25,
			MaxIdleConns:     10,
			StatementTimeout: 30 * time.Second,
		},
		Steem: SteemConfig{
			URL:        "https://api.steemit.com",
			MaxBatch:   50,
			MaxWorkers: 4,
		},
		Indexer: IndexerConfig{
			TrailBlocks: 2,
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Errorf("Valid config should not error: %v", err)
	}

	// Test invalid max_batch
	cfg.Steem.MaxBatch = 10000
	if err := cfg.Validate(); err == nil {
		t.Error("Expected error for invalid max_batch")
	}
}

func TestValidateDatabasePool(t *testing.T) {
	base := func() *Config {
		return &Config{
			Database: DatabaseConfig{
				URL: "postgresql://test@localhost/test", MaxOpenConns: 25,
				MaxIdleConns: 10, StatementTimeout: 30 * time.Second,
			},
			Steem: SteemConfig{URL: "u", MaxBatch: 50, MaxWorkers: 4},
		}
	}

	// MaxOpenConns must be >= 1 (0 means unlimited in database/sql).
	c := base()
	c.Database.MaxOpenConns = 0
	if err := c.Validate(); err == nil {
		t.Error("Expected error for db_max_open_conns=0")
	}

	// MaxIdleConns must be >= 0.
	c = base()
	c.Database.MaxIdleConns = -1
	if err := c.Validate(); err == nil {
		t.Error("Expected error for negative db_max_idle_conns")
	}

	// Idle must not exceed open.
	c = base()
	c.Database.MaxOpenConns = 5
	c.Database.MaxIdleConns = 10
	if err := c.Validate(); err == nil {
		t.Error("Expected error when idle exceeds open")
	}

	// StatementTimeout must be positive.
	c = base()
	c.Database.StatementTimeout = 0
	if err := c.Validate(); err == nil {
		t.Error("Expected error for zero statement_timeout")
	}
}

func TestLoadDatabaseDefaults(t *testing.T) {
	// Ensure pool defaults are applied even when env vars are absent.
	t.Setenv("HIVE_DATABASE_URL", "postgresql://test:test@localhost:5432/testdb")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Database.MaxOpenConns != 25 {
		t.Errorf("default MaxOpenConns = %d, want 25", cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns != 10 {
		t.Errorf("default MaxIdleConns = %d, want 10", cfg.Database.MaxIdleConns)
	}
	if cfg.Database.Migrate != true {
		t.Error("default Migrate should be true")
	}
	if cfg.Database.StatementTimeout == 0 {
		t.Error("default StatementTimeout should be non-zero")
	}
}
