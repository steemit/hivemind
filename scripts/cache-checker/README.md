# Redis Cache Validity Check Guide

## Problem Description

The `_get_post_id` function has a high call frequency (71.98 calls/sec), even though caching is enabled (TTL 3600 seconds). Need to check if Redis cache is working properly.

## Check Methods

### Method 1: Using Check Scripts (Recommended)

#### Basic Check Script (check_cache.py)

```bash
# Check Redis connection and basic functionality
python check_cache.py --redis-url redis://localhost:6379

# Check a specific cache key
python check_cache.py --redis-url redis://localhost:6379 --test-key "post_id_testuser_testpost"
```

#### Detailed Cache Key Check Script (check_cache_keys.py) - Recommended

```bash
# Scan and analyze post_id related cache keys
python3 check_cache_keys.py --redis-url redis://localhost:6379

# Check a specific cache key
python3 check_cache_keys.py --redis-url redis://localhost:6379 --key "post_id_testuser_testpost"

# Show detailed information (including key values and TTL)
python3 check_cache_keys.py --redis-url redis://localhost:6379 --details

# Show Redis statistics
python3 check_cache_keys.py --redis-url redis://localhost:6379 --stats

# Combined usage: scan, show details and statistics
python3 check_cache_keys.py --redis-url redis://localhost:6379 --details --stats --limit 1000
```

### Method 2: Direct Redis Client Usage

```bash
# Connect to Redis
redis-cli

# View all cache keys (using namespace hivemind)
KEYS hivemind:post_id_*

# Check if a specific key exists
EXISTS hivemind:post_id_testuser_testpost

# Get key value
GET hivemind:post_id_testuser_testpost

# Get key TTL (remaining expiration time, -1 means never expires, -2 means does not exist)
TTL hivemind:post_id_testuser_testpost

# View Redis statistics
INFO stats
INFO memory
DBSIZE

# Monitor Redis commands (real-time view of all commands)
MONITOR
```

### Method 3: Enable Debug Logging

Enable debug logging when starting the server:

```bash
# Method 1: Use environment variables
export DEBUG_SQL=true
export LOG_LEVEL=DEBUG
python -m hive.server.serve

# Method 2: Use command line arguments
python -m hive.server.serve --debug-sql true --log-level DEBUG
```

After enabling, logs will show:
- `[CACHE-DEBUG] cache_key: <key>, value: <value>` - Cache hit
- `[CACHE-DEBUG] Not fit cache, cache_key: <key>, Get from DB, value: <value>` - Cache miss

### Method 4: Add Monitoring in Code

You can add statistics in the `cacher` decorator in `hive/server/db.py`:

```python
# Add in cacher decorator
cache_hits = 0
cache_misses = 0

# On cache hit
cache_hits += 1

# On cache miss
cache_misses += 1
```

## Common Issues Troubleshooting

### 1. Redis Not Connected

**Symptoms**: `db.redis_cache` is `None`

**Check**:
```python
# Check in code
if db.redis_cache is None:
    print("Redis cache not initialized")
```

**Solution**:
- Check if `REDIS_URL` environment variable or configuration is correct
- Check if Redis service is running
- Check network connection

### 2. Cache Key Format Issue

**Check**: `_get_post_id` uses cache key format `post_id_{author}_{permlink}`

**Verify**:
```bash
# Find example keys in Redis
redis-cli KEYS "hivemind:post_id_*" | head -10
```

### 3. Cache TTL Setting Issue

**Check**: `_get_post_id` sets TTL to 3600 seconds (1 hour)

**Verify**:
```bash
# Check key TTL
redis-cli TTL hivemind:post_id_testuser_testpost
```

### 4. Cache Namespace Issue

**Check**: Cache uses namespace `hivemind`

**Verify**:
```bash
# All cache keys should have hivemind: prefix
redis-cli KEYS "hivemind:*" | wc -l
```

### 5. High Call Frequency but Cache Miss

**Possible causes**:
1. Cache keys are different each time (too many author/permlink combinations)
2. Cache is frequently cleared
3. TTL setting is too short
4. Redis memory insufficient causing key eviction

**Check**:
```bash
# Check Redis memory usage
redis-cli INFO memory

# Check key eviction policy
redis-cli CONFIG GET maxmemory-policy

# Check maximum memory setting
redis-cli CONFIG GET maxmemory
```

## Performance Optimization Suggestions

### 1. Increase Cache TTL

If data doesn't change frequently, you can increase TTL:
```python
# Current: cache_ttl=3600 (1 hour)
# Suggested: cache_ttl=86400 (24 hours) or longer
```

### 2. Monitor Cache Hit Rate

Add cache hit rate monitoring:
```python
# In cacher decorator
cache_stats = {
    'hits': 0,
    'misses': 0
}

# Periodically output statistics
def get_cache_hit_rate():
    total = cache_stats['hits'] + cache_stats['misses']
    if total == 0:
        return 0
    return cache_stats['hits'] / total * 100
```

### 3. Use Redis Persistence

Ensure Redis is configured with persistence to avoid cache loss after restart:
```bash
# Check persistence configuration
redis-cli CONFIG GET save
redis-cli CONFIG GET appendonly
```

## Quick Check Checklist

- [ ] Redis service running normally
- [ ] `REDIS_URL` configured correctly
- [ ] `db.redis_cache` is not `None`
- [ ] Cache key format is correct
- [ ] TTL setting is reasonable
- [ ] Redis memory is sufficient
- [ ] Enable debug logging to view cache hits/misses
- [ ] Monitor cache hit rate

## Related Files

- `hive/server/db.py` - Cache implementation
- `hive/server/bridge_api/methods.py` - `_get_post_id` function
- `check_cache.py` - Cache check script
