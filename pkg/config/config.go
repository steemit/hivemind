package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the application
type Config struct {
	Database  DatabaseConfig
	Steem     SteemConfig
	Redis     RedisConfig
	Server    ServerConfig
	Indexer   IndexerConfig
	Logging   LoggingConfig
	Telemetry TelemetryConfig
}

// DatabaseConfig holds database configuration.
// Pool sizing is configurable to avoid connection pool exhaustion (see
// connection-pool-exhaustion-go-rewrite.md). Defaults mirror the Python
// legacy pool_size (25) rather than the database/sql defaults.
type DatabaseConfig struct {
	URL string

	// Migrate controls whether schema migrations run on startup.
	Migrate bool

	// MigrateForceBaseline, when true, stamps the schema migration version to 1
	// without executing DDL on startup. Use this the FIRST time you point the
	// binary (server or indexer) at an existing database already provisioned by
	// the Python legacy, then unset it.
	MigrateForceBaseline bool

	// MaxOpenConns caps total connections. Default 25 (matches Python legacy).
	MaxOpenConns int

	// MaxIdleConns caps idle connections in the pool. Default 10.
	MaxIdleConns int

	// ConnMaxLifetime caps how long a connection may be reused. Default 1h.
	ConnMaxLifetime time.Duration

	// ConnMaxIdleTime caps how long an idle connection stays in the pool. Default 10m.
	ConnMaxIdleTime time.Duration

	// StatementTimeout is applied per session (SET statement_timeout) to
	// prevent a single slow query from holding a connection indefinitely.
	// Default 30s.
	StatementTimeout time.Duration
}

// SteemConfig holds Steem node configuration
type SteemConfig struct {
	URL        string
	MaxBatch   int
	MaxWorkers int

	// MaxRetries bounds transport-level retries per RPC after the first
	// attempt (steemgosdk api/v2 WithMaxRetry; exponential backoff between
	// attempts, doubled on 429). Retries cannot be fully disabled: 0 falls
	// back to the SDK default (3) rather than disabling retrying.
	MaxRetries int

	// RPCTimeout bounds each HTTP attempt against steemd (steemgosdk api/v2
	// WithTimeout). 0 selects the SDK default (15s). The worst case for one
	// logical call is (MaxRetries+1) x RPCTimeout plus backoff.
	RPCTimeout time.Duration

	// MutedAccountsURL is the irredeemables list endpoint (whitespace-
	// separated account names), refreshed hourly. Empty disables the list.
	// Mirrors legacy config key muted_accounts_url.
	MutedAccountsURL string
}

// RedisConfig holds Redis configuration
type RedisConfig struct {
	URL     string
	Enabled bool
}

// ServerConfig holds HTTP server configuration.
// Timeouts prevent slow-client goroutine leaks (see
// connection-pool-exhaustion-go-rewrite.md Issue 4).
type ServerConfig struct {
	Port int
	Host string

	// ReadTimeout caps the time to read the request. Default 30s.
	ReadTimeout time.Duration

	// WriteTimeout caps the time to write the response. Default 30s.
	WriteTimeout time.Duration

	// IdleTimeout caps the time a keep-alive connection sits idle. Default 120s.
	IdleTimeout time.Duration

	// MaxBodyBytes caps the JSON-RPC request body (http.MaxBytesReader);
	// larger bodies are refused with HTTP 413 before any parsing. Default
	// 10MB; 0 selects the default. The legacy aiohttp default is 1MB
	// (client_max_size).
	MaxBodyBytes int64

	// ShutdownGracePeriod bounds the graceful-shutdown window on SIGTERM
	// (planned PR#430, 05-deploy.md section 1.4): the server waits this
	// long for in-flight requests to finish, then cancels their contexts.
	// It must be >= db_statement_timeout so every request bounded by the
	// statement timeout can complete naturally (default 45s vs the 30s
	// statement timeout); 0 selects the default.
	ShutdownGracePeriod time.Duration
}

// IndexerConfig holds indexer configuration
type IndexerConfig struct {
	TrailBlocks          int
	MaxBatch             int
	MaxWorkers           int
	SyncInterval         int // Seconds between sync checks
	SyncToS3             bool
	ForceFollowRecount   bool
	TestMaxBlock         int
	TestDisableSync      bool
	RecommendCommunities string

	// JobsInterval is the cache-audit jobs period in seconds
	// (audit_cache_missing/deleted/undelete). 0 disables the scheduler;
	// the jobs remain available for manual invocation.
	JobsInterval int

	// CacheSyncCatchupBlocks is the CacheSync gate (§2.7): while the
	// indexer trails the chain's last irreversible block by more than this
	// many blocks (historical catch-up), the hive_posts_cache_temp 90-day
	// prune pauses; at or below it (live), pruning resumes. Legacy only
	// runs CacheSync from live mode; 100 mirrors the legacy stream max_gap.
	CacheSyncCatchupBlocks int

	// TxBatchBlocks is the number of blocks sharing ONE database
	// transaction (§4.1): the batch is fetched first, then processed,
	// follow-count-flushed and committed atomically -- the Go analogue of
	// legacy sync.py's chunk_size=1000 (Blocks.process_multi wraps 1000
	// blocks per START TRANSACTION ... COMMIT). A failure of any block in
	// the batch rolls back the whole batch and the sync layer retries it
	// from its first block. 1 restores per-block transactions.
	TxBatchBlocks int
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level        string
	Format       string // "json" or "text"
	ScalyrFormat bool   // Enable Scalyr-compatible JSON format
}

// TelemetryConfig holds observability configuration
type TelemetryConfig struct {
	Enabled           bool
	TracesEndpoint    string // OTLP endpoint (e.g., "localhost:4318")
	JaegerURL         string // Deprecated: use TracesEndpoint instead
	PrometheusEnabled bool
	PrometheusPort    int
	ServiceName       string

	// HealthMaxHeadAge is the /health head-lag threshold (planned PR#430,
	// 05-deploy.md section 1.2). Readers answer 500 when the indexed head
	// block is older than this (legacy serve.py max_head_age, 1h). 0
	// disables the lag criterion (catch-up deployments). Sync-service
	// processes (the indexer) are always exempt.
	HealthMaxHeadAge time.Duration

	// PprofEnabled mounts /debug/pprof/* on the metrics port of both
	// binaries (05-deploy.md section 1.3). The metrics port is expected to
	// be internal-only; do not expose it publicly while this is on.
	PprofEnabled bool
}

// Load loads configuration from environment variables and config file
func Load() (*Config, error) {
	// Set defaults
	setDefaults()

	// Load from environment
	viper.SetEnvPrefix("HIVE")
	viper.AutomaticEnv()

	// Load from config file if exists
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("$HOME/.hivemind")
	viper.AddConfigPath("/etc/hivemind")

	if err := viper.ReadInConfig(); err != nil {
		// Config file not found; this is OK if we have env vars
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("error reading config file: %w", err)
		}
	}

	cfg := &Config{
		Database: DatabaseConfig{
			URL:                  getString("database_url", "postgresql://user:pass@localhost:5432/hive"),
			Migrate:              getBool("db_migrate", true),
			MigrateForceBaseline: getBool("db_migrate_force", false),
			MaxOpenConns:         getInt("db_max_open_conns", 25),
			MaxIdleConns:         getInt("db_max_idle_conns", 10),
			ConnMaxLifetime:      getDuration("db_conn_max_lifetime", time.Hour),
			ConnMaxIdleTime:      getDuration("db_conn_max_idle_time", 10*time.Minute),
			StatementTimeout:     getDuration("db_statement_timeout", 30*time.Second),
		},
		Steem: SteemConfig{
			URL:              getString("steemd_url", "https://api.steemit.com"),
			MaxBatch:         getInt("max_batch", 50),
			MaxWorkers:       getInt("max_workers", 4),
			MaxRetries:       getInt("steemd_max_retries", 3),
			RPCTimeout:       getDuration("steemd_rpc_timeout", 15*time.Second),
			MutedAccountsURL: getString("muted_accounts_url", ""),
		},
		Redis: RedisConfig{
			URL:     getString("redis_url", ""),
			Enabled: getString("redis_url", "") != "",
		},
		Server: ServerConfig{
			Port:                getInt("http_server_port", 8080),
			Host:                getString("http_server_host", "0.0.0.0"),
			ReadTimeout:         getDuration("http_read_timeout", 30*time.Second),
			WriteTimeout:        getDuration("http_write_timeout", 30*time.Second),
			IdleTimeout:         getDuration("http_idle_timeout", 120*time.Second),
			MaxBodyBytes:        getInt64("http_max_body_bytes", 10<<20),
			ShutdownGracePeriod: getDuration("http_shutdown_grace", 45*time.Second),
		},
		Indexer: IndexerConfig{
			TrailBlocks:            getInt("trail_blocks", 2),
			MaxBatch:               getInt("max_batch", 50),
			MaxWorkers:             getInt("max_workers", 4),
			SyncInterval:           getInt("sync_interval", 3),
			SyncToS3:               getBool("sync_to_s3", false),
			ForceFollowRecount:     getBool("force_follow_recount", false),
			TestMaxBlock:           getInt("test_max_block", 0),
			TestDisableSync:        getBool("test_disable_sync", false),
			RecommendCommunities:   getString("recommend_communities", "hive-108451,hive-172186,hive-187187"),
			JobsInterval:           getInt("jobs_interval", 0),
			CacheSyncCatchupBlocks: getInt("cache_sync_catchup_blocks", 100),
			TxBatchBlocks:          getInt("indexer_tx_batch_blocks", 1000),
		},
		Logging: LoggingConfig{
			Level:        getString("log_level", "INFO"),
			Format:       getString("log_format", "json"),
			ScalyrFormat: getBool("log_scalyr_format", true),
		},
		Telemetry: TelemetryConfig{
			Enabled:           getBool("telemetry_enabled", true),
			TracesEndpoint:    getString("traces_endpoint", "localhost:4318"),
			JaegerURL:         getString("jaeger_url", "http://localhost:14268/api/traces"),
			PrometheusEnabled: getBool("prometheus_enabled", true),
			PrometheusPort:    getInt("prometheus_port", 9090),
			ServiceName:       getString("service_name", "hivemind"),
			HealthMaxHeadAge:  getDuration("health_max_head_age", time.Hour),
			PprofEnabled:      getBool("pprof_enabled", true),
		},
	}

	// Validate required fields
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return cfg, nil
}

func setDefaults() {
	viper.SetDefault("database_url", "postgresql://user:pass@localhost:5432/hive")
	viper.SetDefault("db_migrate", true)
	viper.SetDefault("db_migrate_force", false)
	viper.SetDefault("db_max_open_conns", 25)
	viper.SetDefault("db_max_idle_conns", 10)
	viper.SetDefault("db_conn_max_lifetime", time.Hour)
	viper.SetDefault("db_conn_max_idle_time", 10*time.Minute)
	viper.SetDefault("db_statement_timeout", 30*time.Second)
	viper.SetDefault("steemd_url", "https://api.steemit.com")
	viper.SetDefault("steemd_max_retries", 3)
	viper.SetDefault("steemd_rpc_timeout", 15*time.Second)
	viper.SetDefault("muted_accounts_url", "")
	viper.SetDefault("http_server_port", 8080)
	viper.SetDefault("http_server_host", "0.0.0.0")
	viper.SetDefault("http_read_timeout", 30*time.Second)
	viper.SetDefault("http_write_timeout", 30*time.Second)
	viper.SetDefault("http_idle_timeout", 120*time.Second)
	viper.SetDefault("http_max_body_bytes", 10<<20)
	viper.SetDefault("http_shutdown_grace", 45*time.Second)
	viper.SetDefault("log_level", "INFO")
	viper.SetDefault("log_format", "json")
	viper.SetDefault("log_scalyr_format", true)
	viper.SetDefault("trail_blocks", 2)
	viper.SetDefault("jobs_interval", 0)
	viper.SetDefault("cache_sync_catchup_blocks", 100)
	viper.SetDefault("indexer_tx_batch_blocks", 1000)
	viper.SetDefault("max_batch", 50)
	viper.SetDefault("max_workers", 4)
	viper.SetDefault("telemetry_enabled", true)
	viper.SetDefault("traces_endpoint", "localhost:4318")
	viper.SetDefault("prometheus_enabled", true)
	viper.SetDefault("prometheus_port", 9090)
	viper.SetDefault("service_name", "hivemind")
	viper.SetDefault("health_max_head_age", time.Hour)
	viper.SetDefault("pprof_enabled", true)
}

func getString(key, defaultValue string) string {
	if viper.IsSet(key) {
		return viper.GetString(key)
	}
	// Also check environment variable directly
	if val := os.Getenv("HIVE_" + toEnvKey(key)); val != "" {
		return val
	}
	return defaultValue
}

func getInt(key string, defaultValue int) int {
	if viper.IsSet(key) {
		return viper.GetInt(key)
	}
	if val := os.Getenv("HIVE_" + toEnvKey(key)); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultValue
}

// getInt64 reads an int64 config value from viper or HIVE_ env var.
func getInt64(key string, defaultValue int64) int64 {
	if viper.IsSet(key) {
		return viper.GetInt64(key)
	}
	if val := os.Getenv("HIVE_" + toEnvKey(key)); val != "" {
		if i, err := strconv.ParseInt(val, 10, 64); err == nil {
			return i
		}
	}
	return defaultValue
}

func getBool(key string, defaultValue bool) bool {
	if viper.IsSet(key) {
		return viper.GetBool(key)
	}
	if val := os.Getenv("HIVE_" + toEnvKey(key)); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return defaultValue
}

// getDuration reads a duration config value from viper or HIVE_ env var.
// Accepts Go duration strings (e.g. "30s", "1h", "10m").
func getDuration(key string, defaultValue time.Duration) time.Duration {
	if viper.IsSet(key) {
		return viper.GetDuration(key)
	}
	if val := os.Getenv("HIVE_" + toEnvKey(key)); val != "" {
		if d, err := time.ParseDuration(val); err == nil {
			return d
		}
	}
	return defaultValue
}

func toEnvKey(key string) string {
	// Convert snake_case to UPPER_SNAKE_CASE
	result := ""
	for i, r := range key {
		if i > 0 && r >= 'A' && r <= 'Z' {
			result += "_"
		}
		if r == '-' || r == '_' {
			result += "_"
		} else {
			result += string(r)
		}
	}
	return result
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.Database.URL == "" {
		return fmt.Errorf("database_url is required")
	}
	if c.Steem.URL == "" {
		return fmt.Errorf("steemd_url is required")
	}
	if c.Steem.MaxBatch <= 0 || c.Steem.MaxBatch > 5000 {
		return fmt.Errorf("max_batch must be between 1 and 5000")
	}
	if c.Steem.MaxWorkers <= 0 || c.Steem.MaxWorkers > 64 {
		return fmt.Errorf("max_workers must be between 1 and 64")
	}
	// 0 selects the SDK default for both knobs; negative values are never
	// meaningful, and absurdly large retry counts would hammer a struggling
	// node instead of surfacing the failure to the sync loop.
	if c.Steem.MaxRetries < 0 || c.Steem.MaxRetries > 10 {
		return fmt.Errorf("steemd_max_retries must be between 0 and 10 (0 = SDK default)")
	}
	if c.Steem.RPCTimeout < 0 {
		return fmt.Errorf("steemd_rpc_timeout must not be negative (0 = SDK default)")
	}
	if c.Indexer.TrailBlocks < 0 || c.Indexer.TrailBlocks > 100 {
		return fmt.Errorf("trail_blocks must be between 0 and 100")
	}
	if c.Indexer.CacheSyncCatchupBlocks < 0 {
		return fmt.Errorf("cache_sync_catchup_blocks must not be negative")
	}
	// The tx batch is the atomic unit of the sync loop: 1 restores per-block
	// transactions (the degenerate mode), and an absurdly large batch would
	// hold a transaction (and its locks) open for far too long on dense
	// blocks. 0 means "unset" (zero-value configs; the sync loop applies the
	// legacy default of 1000). 1000 is the legacy chunk_size.
	if c.Indexer.TxBatchBlocks < 0 || c.Indexer.TxBatchBlocks > 50000 {
		return fmt.Errorf("indexer_tx_batch_blocks must be between 0 (default) and 50000 (1 = per-block transactions)")
	}
	// Pool sizing sanity checks. MaxOpenConns=0 means "unlimited" in
	// database/sql, which defeats the purpose of pool protection, so reject it.
	if c.Database.MaxOpenConns < 1 {
		return fmt.Errorf("db_max_open_conns must be at least 1")
	}
	if c.Database.MaxIdleConns < 0 {
		return fmt.Errorf("db_max_idle_conns must not be negative")
	}
	if c.Database.MaxIdleConns > c.Database.MaxOpenConns {
		return fmt.Errorf("db_max_idle_conns (%d) must not exceed db_max_open_conns (%d)",
			c.Database.MaxIdleConns, c.Database.MaxOpenConns)
	}
	// StatementTimeout: negative is invalid; 0 means "disabled" (no DSN injection).
	if c.Database.StatementTimeout < 0 {
		return fmt.Errorf("db_statement_timeout must not be negative (0 = disabled)")
	}
	if c.Database.ConnMaxLifetime < 0 || c.Database.ConnMaxIdleTime < 0 {
		return fmt.Errorf("db_conn_max_lifetime and db_conn_max_idle_time must not be negative")
	}
	// HTTP timeouts: 0 means "disabled" (http.Server treats 0 as no timeout);
	// negative is invalid.
	for _, d := range []struct {
		name string
		v    time.Duration
	}{
		{"http_read_timeout", c.Server.ReadTimeout},
		{"http_write_timeout", c.Server.WriteTimeout},
		{"http_idle_timeout", c.Server.IdleTimeout},
	} {
		if d.v < 0 {
			return fmt.Errorf("%s must not be negative", d.name)
		}
	}
	// Request body cap: 0 selects the default (10MB, applied by the
	// JSON-RPC handler), negative or absurdly large values (> 1GiB) are
	// rejected — an unbounded pre-auth body is exactly what §5.5 exists
	// to prevent.
	if c.Server.MaxBodyBytes < 0 || c.Server.MaxBodyBytes > 1<<30 {
		return fmt.Errorf("http_max_body_bytes must be between 0 (default) and %d bytes", 1<<30)
	}
	// Graceful shutdown: 0 selects the default (45s); negative is invalid.
	// When set explicitly it must cover the statement timeout so every
	// DB-bounded request can complete inside the grace window (planned
	// PR#430, 05-deploy.md section 1.4).
	if c.Server.ShutdownGracePeriod < 0 {
		return fmt.Errorf("http_shutdown_grace must not be negative (0 = default)")
	}
	grace := c.Server.ShutdownGracePeriod
	if grace == 0 {
		// 0 means "use the default" (45s via setDefaults); the invariant
		// must hold for the effective value, not just explicit settings.
		grace = 45 * time.Second
	}
	if grace > 0 && c.Database.StatementTimeout > 0 &&
		grace < c.Database.StatementTimeout {
		return fmt.Errorf("http_shutdown_grace (%s) must be >= db_statement_timeout (%s) so in-flight statements can complete",
			grace, c.Database.StatementTimeout)
	}
	// /health head-lag threshold: 0 disables the criterion (catch-up
	// deployments); negative is invalid.
	if c.Telemetry.HealthMaxHeadAge < 0 {
		return fmt.Errorf("health_max_head_age must not be negative (0 = disabled)")
	}
	return nil
}

// GetDuration returns a duration from config key, with default
func GetDuration(key string, defaultValue time.Duration) time.Duration {
	if viper.IsSet(key) {
		return viper.GetDuration(key)
	}
	return defaultValue
}
