package cache

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
)

const (
	// CACHE_NAMESPACE is the namespace prefix for cache keys
	CACHE_NAMESPACE = "hivemind"
)

// Cache wraps Redis client
type Cache struct {
	client *redis.Client
	ctx    context.Context
}

// New creates a new Redis cache client
func New(cfg *config.RedisConfig) (*Cache, error) {
	if !cfg.Enabled {
		logging.GetLogger().Info("Redis cache disabled")
		return nil, nil
	}

	opt, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URL: %w", err)
	}

	client := redis.NewClient(opt)

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	logging.GetLogger().Info("Redis connection established")

	return &Cache{
		client: client,
		ctx:    context.Background(),
	}, nil
}

// Get retrieves a value from cache
func (c *Cache) Get(key string) (string, error) {
	if c == nil || c.client == nil {
		return "", ErrCacheDisabled
	}
	fullKey := c.namespaceKey(key)
	return c.client.Get(c.ctx, fullKey).Result()
}

// GetJSON retrieves a JSON value from cache and unmarshals it
func (c *Cache) GetJSON(key string, dest interface{}) error {
	if c == nil || c.client == nil {
		return ErrCacheDisabled
	}
	val, err := c.Get(key)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(val), dest)
}

// Set sets a value in cache with TTL
func (c *Cache) Set(key string, value interface{}, ttl time.Duration) error {
	if c == nil || c.client == nil {
		return ErrCacheDisabled
	}
	fullKey := c.namespaceKey(key)
	
	// Convert value to string if needed
	var val string
	switch v := value.(type) {
	case string:
		val = v
	default:
		jsonBytes, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("failed to marshal cache value: %w", err)
		}
		val = string(jsonBytes)
	}
	
	return c.client.Set(c.ctx, fullKey, val, ttl).Err()
}

// SetJSON marshals a value to JSON and stores it in cache
func (c *Cache) SetJSON(key string, value interface{}, ttl time.Duration) error {
	return c.Set(key, value, ttl)
}

// namespaceKey adds namespace prefix to key
func (c *Cache) namespaceKey(key string) string {
	return fmt.Sprintf("%s:%s", CACHE_NAMESPACE, key)
}

// HashKey creates a shortened cache key using MD5 hash
func HashKey(parts ...string) string {
	keyStr := ""
	for i, part := range parts {
		if i > 0 {
			keyStr += "_"
		}
		keyStr += part
	}
	
	hash := md5.Sum([]byte(keyStr))
	return hex.EncodeToString(hash[:])
}

// Delete removes a key from cache
func (c *Cache) Delete(key string) error {
	if c == nil || c.client == nil {
		return ErrCacheDisabled
	}
	return c.client.Del(c.ctx, key).Err()
}

// Exists checks if a key exists
func (c *Cache) Exists(key string) (bool, error) {
	if c == nil || c.client == nil {
		return false, ErrCacheDisabled
	}
	count, err := c.client.Exists(c.ctx, key).Result()
	return count > 0, err
}

// Close closes the Redis connection
func (c *Cache) Close() error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Close()
}

// Health checks Redis health
func (c *Cache) Health(ctx context.Context) error {
	if c == nil || c.client == nil {
		return ErrCacheDisabled
	}
	return c.client.Ping(ctx).Err()
}

var (
	// ErrCacheDisabled is returned when cache operations are attempted but cache is disabled
	ErrCacheDisabled = fmt.Errorf("cache is disabled")
)

