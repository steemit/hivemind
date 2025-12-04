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
	Database DatabaseConfig
	Steem    SteemConfig
	Redis    RedisConfig
	Server   ServerConfig
	Indexer  IndexerConfig
	Logging  LoggingConfig
	Telemetry TelemetryConfig
}

// DatabaseConfig holds database configuration
type DatabaseConfig struct {
	URL string
}

// SteemConfig holds Steem node configuration
type SteemConfig struct {
	URL       string
	MaxBatch  int
	MaxWorkers int
}

// RedisConfig holds Redis configuration
type RedisConfig struct {
	URL      string
	Enabled  bool
}

// ServerConfig holds HTTP server configuration
type ServerConfig struct {
	Port int
	Host string
}

// IndexerConfig holds indexer configuration
type IndexerConfig struct {
	TrailBlocks          int
	MaxBatch            int
	MaxWorkers          int
	SyncToS3            bool
	ForceFollowRecount  bool
	TestMaxBlock        int
	TestDisableSync     bool
	RecommendCommunities string
}

// LoggingConfig holds logging configuration
type LoggingConfig struct {
	Level      string
	Format     string // "json" or "text"
	ScalyrFormat bool // Enable Scalyr-compatible JSON format
}

// TelemetryConfig holds observability configuration
type TelemetryConfig struct {
	Enabled     bool
	JaegerURL   string
	PrometheusEnabled bool
	PrometheusPort    int
	ServiceName string
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
			URL: getString("database_url", "postgresql://user:pass@localhost:5432/hive"),
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
			Port: getInt("http_server_port", 8080),
			Host: getString("http_server_host", "0.0.0.0"),
		},
		Indexer: IndexerConfig{
			TrailBlocks:          getInt("trail_blocks", 2),
			MaxBatch:            getInt("max_batch", 50),
			MaxWorkers:          getInt("max_workers", 4),
			SyncToS3:            getBool("sync_to_s3", false),
			ForceFollowRecount:  getBool("force_follow_recount", false),
			TestMaxBlock:        getInt("test_max_block", 0),
			TestDisableSync:     getBool("test_disable_sync", false),
			RecommendCommunities: getString("recommend_communities", "hive-108451,hive-172186,hive-187187"),
		},
		Logging: LoggingConfig{
			Level:        getString("log_level", "INFO"),
			Format:       getString("log_format", "json"),
			ScalyrFormat: getBool("log_scalyr_format", true),
		},
		Telemetry: TelemetryConfig{
			Enabled:            getBool("telemetry_enabled", true),
			JaegerURL:          getString("jaeger_url", "http://localhost:14268/api/traces"),
			PrometheusEnabled:  getBool("prometheus_enabled", true),
			PrometheusPort:     getInt("prometheus_port", 9090),
			ServiceName:        getString("service_name", "hivemind"),
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
	viper.SetDefault("steemd_url", "https://api.steemit.com")
	viper.SetDefault("http_server_port", 8080)
	viper.SetDefault("http_server_host", "0.0.0.0")
	viper.SetDefault("log_level", "INFO")
	viper.SetDefault("log_format", "json")
	viper.SetDefault("log_scalyr_format", true)
	viper.SetDefault("trail_blocks", 2)
	viper.SetDefault("max_batch", 50)
	viper.SetDefault("max_workers", 4)
	viper.SetDefault("telemetry_enabled", true)
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
	return nil
}

// GetDuration returns a duration from config key, with default
func GetDuration(key string, defaultValue time.Duration) time.Duration {
	if viper.IsSet(key) {
		return viper.GetDuration(key)
	}
	return defaultValue
}

