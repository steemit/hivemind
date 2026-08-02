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

	// MigrateForceBaseline, when true, stamps the schema migration version to 1
	// without executing DDL on startup. Use this the FIRST time you point the
	// Go indexer at an existing database already provisioned by the Python
	// legacy. It is ignored for fresh databases. After the first run, unset it.
	MigrateForceBaseline bool
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
			URL:              getString("database_url", "postgresql://user:pass@localhost:5432/hive"),
			Migrate:          getBool("db_migrate", true),
			MaxOpenConns:     getInt("db_max_open_conns", 25),
			MaxIdleConns:     getInt("db_max_idle_conns", 10),
			ConnMaxLifetime:  getDuration("db_conn_max_lifetime", time.Hour),
			ConnMaxIdleTime:  getDuration("db_conn_max_idle_time", 10*time.Minute),
			StatementTimeout: getDuration("db_statement_timeout", 30*time.Second),
		},
		Steem: SteemConfig{
			URL:        getString("steemd_url", "https://api.steemit.com"),
			MaxBatch:   getInt("max_batch", 50),
			MaxWorkers: getInt("max_workers", 4),
		},
		Redis: RedisConfig{
			URL:     getString("redis_url", ""),
			Enabled: getString("redis_url", "") != "",
		},
		Server: ServerConfig{
			Port:         getInt("http_server_port", 8080),
			Host:         getString("http_server_host", "0.0.0.0"),
			ReadTimeout:  getDuration("http_read_timeout", 30*time.Second),
			WriteTimeout: getDuration("http_write_timeout", 30*time.Second),
			IdleTimeout:  getDuration("http_idle_timeout", 120*time.Second),
		},
		Indexer: IndexerConfig{
			TrailBlocks:          getInt("trail_blocks", 2),
			MaxBatch:             getInt("max_batch", 50),
			MaxWorkers:           getInt("max_workers", 4),
			SyncInterval:         getInt("sync_interval", 3),
			SyncToS3:             getBool("sync_to_s3", false),
			ForceFollowRecount:   getBool("force_follow_recount", false),
			TestMaxBlock:         getInt("test_max_block", 0),
			TestDisableSync:      getBool("test_disable_sync", false),
			RecommendCommunities: getString("recommend_communities", "hive-108451,hive-172186,hive-187187"),
			MigrateForceBaseline: getBool("db_migrate_force", false),
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
	viper.SetDefault("db_max_open_conns", 25)
	viper.SetDefault("db_max_idle_conns", 10)
	viper.SetDefault("db_conn_max_lifetime", time.Hour)
	viper.SetDefault("db_conn_max_idle_time", 10*time.Minute)
	viper.SetDefault("db_statement_timeout", 30*time.Second)
	viper.SetDefault("steemd_url", "https://api.steemit.com")
	viper.SetDefault("http_server_port", 8080)
	viper.SetDefault("http_server_host", "0.0.0.0")
	viper.SetDefault("http_read_timeout", 30*time.Second)
	viper.SetDefault("http_write_timeout", 30*time.Second)
	viper.SetDefault("http_idle_timeout", 120*time.Second)
	viper.SetDefault("log_level", "INFO")
	viper.SetDefault("log_format", "json")
	viper.SetDefault("log_scalyr_format", true)
	viper.SetDefault("trail_blocks", 2)
	viper.SetDefault("max_batch", 50)
	viper.SetDefault("max_workers", 4)
	viper.SetDefault("telemetry_enabled", true)
	viper.SetDefault("traces_endpoint", "localhost:4318")
	viper.SetDefault("prometheus_enabled", true)
	viper.SetDefault("prometheus_port", 9090)
	viper.SetDefault("service_name", "hivemind")
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
	if c.Indexer.TrailBlocks < 0 || c.Indexer.TrailBlocks > 100 {
		return fmt.Errorf("trail_blocks must be between 0 and 100")
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
	if c.Database.StatementTimeout <= 0 {
		return fmt.Errorf("db_statement_timeout must be positive")
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
