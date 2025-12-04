package config

import (
	"os"
	"testing"
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
		Database: DatabaseConfig{URL: "postgresql://test@localhost/test"},
		Steem: SteemConfig{
			URL:       "https://api.steemit.com",
			MaxBatch:  50,
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

