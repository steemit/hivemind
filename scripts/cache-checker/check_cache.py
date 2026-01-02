#!/usr/bin/env python3
"""
Redis cache validity check script

Usage:
    # Using environment variable (recommended)
    export REDIS_URL=redis://localhost:6379
    python check_cache.py
    
    # Using command line argument
    python check_cache.py --redis-url redis://localhost:6379
    python check_cache.py --redis-url redis://localhost:6379 --test-key "post_id_testuser_testpost"
"""

import asyncio
import argparse
import os
import sys
from aiocache import Cache
from hive.server.db import CACHE_NAMESPACE


async def check_redis_connection(redis_url):
    """Check if Redis connection is working"""
    print("=" * 60)
    print("1. Checking Redis connection...")
    print("=" * 60)
    
    try:
        cache = Cache.from_url(redis_url)
        # Test connection
        await cache.set("test_connection", "ok", ttl=10)
        result = await cache.get("test_connection")
        await cache.delete("test_connection")
        
        if result == "ok":
            print("✓ Redis connection OK")
            return cache
        else:
            print("✗ Redis connection test failed")
            return None
    except Exception as e:
        print(f"✗ Redis connection failed: {e}")
        return None


async def check_cache_key(cache, cache_key):
    """Check if a specific cache key exists"""
    print("\n" + "=" * 60)
    print(f"2. Checking cache key: {cache_key}")
    print("=" * 60)
    
    try:
        # Check key with namespace
        full_key = f"{CACHE_NAMESPACE}:{cache_key}"
        value = await cache.get(cache_key, namespace=CACHE_NAMESPACE)
        
        if value is not None:
            print(f"✓ Cache key exists")
            print(f"  Key name: {full_key}")
            print(f"  Value: {value}")
            
            # Get TTL
            try:
                # aiocache may not directly support TTL query, try to get it
                print(f"  Note: TTL information needs to be queried directly via Redis client")
            except:
                pass
        else:
            print(f"✗ Cache key does not exist or has expired")
            print(f"  Key name: {full_key}")
    except Exception as e:
        print(f"✗ Error checking cache key: {e}")


async def scan_cache_keys(cache, pattern="post_id_*"):
    """Scan cache keys matching the pattern"""
    print("\n" + "=" * 60)
    print(f"3. Scanning cache keys (pattern: {pattern})")
    print("=" * 60)
    
    # Note: aiocache does not directly support SCAN, need to use Redis client
    print("Tip: To scan all cache keys, use Redis client:")
    print(f"  redis-cli --scan --pattern '{CACHE_NAMESPACE}:{pattern}'")
    print(f"\nOr use Python redis client:")
    print(f"  import redis")
    print(f"  r = redis.Redis.from_url('redis://your-redis-url')")
    print(f"  keys = r.keys('{CACHE_NAMESPACE}:{pattern}')")
    print(f"  print(f'Found {{len(keys)}} matching keys')")


async def test_cache_operations(cache):
    """Test cache read/write operations"""
    print("\n" + "=" * 60)
    print("4. Testing cache operations")
    print("=" * 60)
    
    test_key = "test_cache_operation"
    test_value = 12345
    
    try:
        # Write
        await cache.set(test_key, test_value, ttl=60, namespace=CACHE_NAMESPACE)
        print(f"✓ Cache write successful: {test_key} = {test_value}")
        
        # Read
        result = await cache.get(test_key, namespace=CACHE_NAMESPACE)
        if result == test_value:
            print(f"✓ Cache read successful: {result}")
        else:
            print(f"✗ Cache read failed: expected {test_value}, got {result}")
        
        # Delete
        await cache.delete(test_key, namespace=CACHE_NAMESPACE)
        print(f"✓ Cache delete successful")
        
        # Verify deletion
        result = await cache.get(test_key, namespace=CACHE_NAMESPACE)
        if result is None:
            print(f"✓ Deletion verification successful")
        else:
            print(f"✗ Deletion verification failed: key still exists")
            
    except Exception as e:
        print(f"✗ Error testing cache operations: {e}")


async def get_cache_stats(cache):
    """Get cache statistics (if supported)"""
    print("\n" + "=" * 60)
    print("5. Cache statistics")
    print("=" * 60)
    
    try:
        # Get Redis information
        print("Tip: To get detailed Redis statistics, use:")
        print("  redis-cli INFO stats")
        print("  redis-cli INFO memory")
        print("  redis-cli DBSIZE")
    except Exception as e:
        print(f"✗ Error getting statistics: {e}")


def print_debug_instructions():
    """Print instructions for enabling debug logging"""
    print("\n" + "=" * 60)
    print("6. Enabling cache debug logging")
    print("=" * 60)
    print("To enable cache debug logging:")
    print("  1. Set environment variable: export DEBUG_SQL=true")
    print("  2. Or add to startup arguments: --debug-sql true")
    print("  3. Set log level to DEBUG: export LOG_LEVEL=DEBUG")
    print("\nAfter enabling, logs will show:")
    print("  [CACHE-DEBUG] cache_key: <key>, value: <value>")
    print("  [CACHE-DEBUG] Not fit cache, cache_key: <key>, Get from DB, value: <value>")


async def main():
    parser = argparse.ArgumentParser(description='Check Redis cache validity')
    # Get Redis URL from environment variable or command line argument
    default_redis_url = os.environ.get('REDIS_URL')
    parser.add_argument('--redis-url', 
                       default=default_redis_url,
                       help='Redis connection URL (default: from REDIS_URL environment variable)')
    parser.add_argument('--test-key', help='Cache key to check (e.g., post_id_testuser_testpost)')
    parser.add_argument('--pattern', default='post_id_*', help='Key pattern to scan (default: post_id_*)')
    
    args = parser.parse_args()
    
    # Validate Redis URL
    if not args.redis_url:
        print("Error: Redis URL is required")
        print("Please provide --redis-url argument or set REDIS_URL environment variable")
        sys.exit(1)
    
    # Check Redis connection
    cache = await check_redis_connection(args.redis_url)
    if cache is None:
        print("\n✗ Cannot connect to Redis, please check:")
        print("  1. Is Redis service running?")
        print("  2. Is Redis URL correct?")
        print("  3. Is network connection normal?")
        sys.exit(1)
    
    # Check specified cache key
    if args.test_key:
        await check_cache_key(cache, args.test_key)
    
    # Scan cache keys
    await scan_cache_keys(cache, args.pattern)
    
    # Test cache operations
    await test_cache_operations(cache)
    
    # Get statistics
    await get_cache_stats(cache)
    
    # Print debug instructions
    print_debug_instructions()
    
    # Close connection (aiocache's close is a coroutine)
    try:
        if hasattr(cache, 'close'):
            if asyncio.iscoroutinefunction(cache.close):
                await cache.close()
            else:
                cache.close()
    except Exception as e:
        # Ignore errors during close
        pass
    
    print("\n" + "=" * 60)
    print("Check completed!")
    print("=" * 60)


if __name__ == "__main__":
    # Compatible with Python 3.6 (asyncio.run() requires Python 3.7+)
    try:
        # Python 3.7+
        asyncio.run(main())
    except AttributeError:
        # Python 3.6
        loop = asyncio.get_event_loop()
        try:
            loop.run_until_complete(main())
        finally:
            loop.close()
