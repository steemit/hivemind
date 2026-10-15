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

	// Tx batch blocks: 0 (unset -> legacy default), 1 (per-block) and the
	// legacy 1000 are valid; negative and absurdly large values are not.
	cfg.Steem.MaxBatch = 50
	for _, v := range []int{0, 1, 1000, 50000} {
		cfg.Indexer.TxBatchBlocks = v
		if err := cfg.Validate(); err != nil {
			t.Errorf("TxBatchBlocks=%d should be valid: %v", v, err)
		}
	}
	for _, v := range []int{-1, 50001} {
		cfg.Indexer.TxBatchBlocks = v
		if err := cfg.Validate(); err == nil {
			t.Errorf("TxBatchBlocks=%d should be rejected", v)
		}
	}
	cfg.Indexer.TxBatchBlocks = 0
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

	// StatementTimeout: 0 means disabled (valid), negative is invalid.
	c = base()
	c.Database.StatementTimeout = 0
	if err := c.Validate(); err != nil {
		t.Errorf("StatementTimeout=0 (disabled) should be valid, got: %v", err)
	}
	c = base()
	c.Database.StatementTimeout = -1 * time.Second
	if err := c.Validate(); err == nil {
		t.Error("Expected error for negative statement_timeout")
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

func TestValidateSteemRPC(t *testing.T) {
	base := func() *Config {
		return &Config{
			Database: DatabaseConfig{
				URL: "postgresql://test@localhost/test", MaxOpenConns: 25,
				MaxIdleConns: 10, StatementTimeout: 30 * time.Second,
			},
			Steem: SteemConfig{URL: "u", MaxBatch: 50, MaxWorkers: 4},
		}
	}

	// Zero values are valid: they select the SDK defaults (retry 3, 15s).
	if err := base().Validate(); err != nil {
		t.Errorf("zero-value steemd RPC knobs should be valid, got: %v", err)
	}

	// steemd_max_retries: within bounds valid, out of bounds invalid.
	c := base()
	c.Steem.MaxRetries = 10
	if err := c.Validate(); err != nil {
		t.Errorf("steemd_max_retries=10 should be valid, got: %v", err)
	}
	c = base()
	c.Steem.MaxRetries = 11
	if err := c.Validate(); err == nil {
		t.Error("expected error for steemd_max_retries=11")
	}
	c = base()
	c.Steem.MaxRetries = -1
	if err := c.Validate(); err == nil {
		t.Error("expected error for negative steemd_max_retries")
	}

	// steemd_rpc_timeout: negative invalid.
	c = base()
	c.Steem.RPCTimeout = -time.Second
	if err := c.Validate(); err == nil {
		t.Error("expected error for negative steemd_rpc_timeout")
	}
}

func TestLoadSteemRPCDefaults(t *testing.T) {
	t.Setenv("HIVE_DATABASE_URL", "postgresql://test:test@localhost:5432/testdb")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Steem.MaxRetries != 3 {
		t.Errorf("default Steem.MaxRetries = %d, want 3", cfg.Steem.MaxRetries)
	}
	if cfg.Steem.RPCTimeout != 15*time.Second {
		t.Errorf("default Steem.RPCTimeout = %v, want 15s", cfg.Steem.RPCTimeout)
	}

	t.Setenv("HIVE_STEEMD_MAX_RETRIES", "5")
	t.Setenv("HIVE_STEEMD_RPC_TIMEOUT", "2m")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Failed to load config with overrides: %v", err)
	}
	if cfg.Steem.MaxRetries != 5 {
		t.Errorf("Steem.MaxRetries = %d, want 5 (from HIVE_STEEMD_MAX_RETRIES)", cfg.Steem.MaxRetries)
	}
	if cfg.Steem.RPCTimeout != 2*time.Minute {
		t.Errorf("Steem.RPCTimeout = %v, want 2m (from HIVE_STEEMD_RPC_TIMEOUT)", cfg.Steem.RPCTimeout)
	}
}

func TestValidateMaxBodyBytes(t *testing.T) {
	base := func() *Config {
		return &Config{
			Database: DatabaseConfig{
				URL: "postgresql://test@localhost/test", MaxOpenConns: 25,
				MaxIdleConns: 10, StatementTimeout: 30 * time.Second,
			},
			Steem: SteemConfig{URL: "u", MaxBatch: 50, MaxWorkers: 4},
		}
	}
	c := base()
	// 0 (unset) selects the default cap and must validate.
	if err := c.Validate(); err != nil {
		t.Errorf("zero MaxBodyBytes (default) should be valid: %v", err)
	}
	c.Server.MaxBodyBytes = 1
	if err := c.Validate(); err != nil {
		t.Errorf("explicit cap should be valid: %v", err)
	}
	c.Server.MaxBodyBytes = -1
	if err := c.Validate(); err == nil {
		t.Error("expected error for negative http_max_body_bytes")
	}
	c.Server.MaxBodyBytes = 1<<30 + 1
	if err := c.Validate(); err == nil {
		t.Error("expected error for http_max_body_bytes above 1GiB")
	}
}

func TestLoadMaxBodyBytes(t *testing.T) {
	t.Setenv("HIVE_DATABASE_URL", "postgresql://test:test@localhost:5432/testdb")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Server.MaxBodyBytes != 10<<20 {
		t.Errorf("default Server.MaxBodyBytes = %d, want %d", cfg.Server.MaxBodyBytes, 10<<20)
	}

	t.Setenv("HIVE_HTTP_MAX_BODY_BYTES", "12345")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Failed to load config with override: %v", err)
	}
	if cfg.Server.MaxBodyBytes != 12345 {
		t.Errorf("Server.MaxBodyBytes = %d, want 12345 (from HIVE_HTTP_MAX_BODY_BYTES)", cfg.Server.MaxBodyBytes)
	}
}

// TestValidateShutdownGrace pins the PR#430 graceful-shutdown invariant
// (05-deploy.md section 1.4): the grace window must cover the statement
// timeout so DB-bounded requests can complete; 0 selects the default and
// negative values are rejected.
func TestValidateShutdownGrace(t *testing.T) {
	base := func() *Config {
		return &Config{
			Database: DatabaseConfig{
				URL: "postgresql://test@localhost/test", MaxOpenConns: 25,
				MaxIdleConns: 10, StatementTimeout: 30 * time.Second,
			},
			Steem: SteemConfig{URL: "u", MaxBatch: 50, MaxWorkers: 4},
		}
	}

	// Zero value (unset -> default 45s) must validate, including against a
	// longer statement timeout (hand-built configs).
	if err := base().Validate(); err != nil {
		t.Errorf("zero ShutdownGracePeriod should be valid, got: %v", err)
	}

	// Default 45s >= statement timeout 30s.
	c := base()
	c.Server.ShutdownGracePeriod = 45 * time.Second
	if err := c.Validate(); err != nil {
		t.Errorf("45s grace vs 30s statement timeout should be valid, got: %v", err)
	}

	// Explicit grace below the statement timeout is rejected.
	c = base()
	c.Server.ShutdownGracePeriod = 20 * time.Second
	if err := c.Validate(); err == nil {
		t.Error("expected error for http_shutdown_grace < db_statement_timeout")
	}

	// Negative grace is rejected.
	c = base()
	c.Server.ShutdownGracePeriod = -time.Second
	if err := c.Validate(); err == nil {
		t.Error("expected error for negative http_shutdown_grace")
	}

	// With the statement timeout disabled (0) any non-negative grace is OK.
	c = base()
	c.Database.StatementTimeout = 0
	c.Server.ShutdownGracePeriod = time.Second
	if err := c.Validate(); err != nil {
		t.Errorf("grace with disabled statement timeout should be valid, got: %v", err)
	}
}

// TestLoadHealthObservabilityDefaults pins the PR#430 defaults and env
// overrides for the new observability knobs.
func TestLoadHealthObservabilityDefaults(t *testing.T) {
	t.Setenv("HIVE_DATABASE_URL", "postgresql://test:test@localhost:5432/testdb")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Telemetry.HealthMaxHeadAge != time.Hour {
		t.Errorf("default HealthMaxHeadAge = %v, want 1h (legacy max_head_age)", cfg.Telemetry.HealthMaxHeadAge)
	}
	if !cfg.Telemetry.PprofEnabled {
		t.Error("default PprofEnabled should be true")
	}
	if cfg.Server.ShutdownGracePeriod != 45*time.Second {
		t.Errorf("default ShutdownGracePeriod = %v, want 45s", cfg.Server.ShutdownGracePeriod)
	}

	t.Setenv("HIVE_HEALTH_MAX_HEAD_AGE", "5m")
	t.Setenv("HIVE_PPROF_ENABLED", "false")
	t.Setenv("HIVE_HTTP_SHUTDOWN_GRACE", "2m")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Failed to load config with overrides: %v", err)
	}
	if cfg.Telemetry.HealthMaxHeadAge != 5*time.Minute {
		t.Errorf("HealthMaxHeadAge = %v, want 5m (from HIVE_HEALTH_MAX_HEAD_AGE)", cfg.Telemetry.HealthMaxHeadAge)
	}
	if cfg.Telemetry.PprofEnabled {
		t.Error("PprofEnabled should be false (from HIVE_PPROF_ENABLED)")
	}
	if cfg.Server.ShutdownGracePeriod != 2*time.Minute {
		t.Errorf("ShutdownGracePeriod = %v, want 2m (from HIVE_HTTP_SHUTDOWN_GRACE)", cfg.Server.ShutdownGracePeriod)
	}

	// Negative head age threshold is rejected.
	t.Setenv("HIVE_HEALTH_MAX_HEAD_AGE", "-1s")
	if _, err := Load(); err == nil {
		t.Error("expected error for negative health_max_head_age")
	}
}
