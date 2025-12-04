package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
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
	return c.client.Get(c.ctx, key).Result()
}

// Set sets a value in cache with TTL
func (c *Cache) Set(key string, value interface{}, ttl time.Duration) error {
	if c == nil || c.client == nil {
		return ErrCacheDisabled
	}
	return c.client.Set(c.ctx, key, value, ttl).Err()
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

