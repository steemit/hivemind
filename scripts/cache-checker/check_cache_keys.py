#!/usr/bin/env python3
"""
Detailed Redis cache key inspection script

Usage:
    # Using environment variable (recommended)
    export REDIS_URL=redis://localhost:6379
    python3 check_cache_keys.py
    
    # Using command line argument
    python3 check_cache_keys.py --redis-url redis://localhost:6379
    python3 check_cache_keys.py --redis-url redis://localhost:6379 --pattern "post_id_*" --limit 100
    python3 check_cache_keys.py --redis-url redis://localhost:6379 --stats
"""

import argparse
import os
import sys
import time
from collections import defaultdict

try:
    import redis
except ImportError:
    print("Error: redis library is required")
    print("Please run: pip install redis")
    sys.exit(1)

from hive.server.db import CACHE_NAMESPACE


def format_key(key):
    """Format key name (handle bytes and str)"""
    if isinstance(key, bytes):
        return key.decode('utf-8')
    return key


def format_value(value):
    """Format value (handle bytes and str, limit length)"""
    if isinstance(value, bytes):
        value = value.decode('utf-8')
    if isinstance(value, str) and len(value) > 100:
        return value[:100] + "..."
    return value


def check_redis_connection(redis_url):
    """Check Redis connection"""
    try:
        r = redis.Redis.from_url(redis_url, decode_responses=False)
        r.ping()
        return r
    except Exception as e:
        print(f"✗ Redis connection failed: {e}")
        return None


def scan_keys(r, pattern, limit=None):
    """Scan keys matching the pattern (using SCAN to avoid blocking)"""
    keys = []
    cursor = 0
    count = 0
    
    print(f"Scanning keys (pattern: {pattern})...")
    start_time = time.time()
    
    while True:
        cursor, batch = r.scan(cursor, match=pattern, count=1000)
        keys.extend(batch)
        count += len(batch)
        
        if count % 1000 == 0 and count > 0:
            elapsed = time.time() - start_time
            print(f"  Scanned {count} keys (elapsed {elapsed:.1f}s)...")
        
        if limit and count >= limit:
            keys = keys[:limit]
            break
            
        if cursor == 0:
            break
    
    elapsed = time.time() - start_time
    print(f"✓ Scan completed: found {len(keys)} keys (elapsed {elapsed:.1f}s)")
    return keys


def analyze_keys(r, keys, show_details=False, max_details=10):
    """Analyze detailed information about keys"""
    if not keys:
        print("No matching keys found")
        return
    
    print(f"\n{'='*60}")
    print(f"Key analysis (total: {len(keys)})")
    print(f"{'='*60}")
    
    # Statistics
    ttl_stats = defaultdict(int)
    key_types = defaultdict(int)
    sample_keys = []
    
    print("\nAnalyzing keys...")
    for i, key in enumerate(keys[:max_details if show_details else len(keys)]):
        try:
            key_str = format_key(key)
            key_type = r.type(key)
            ttl = r.ttl(key)
            
            ttl_stats[ttl] += 1
            key_types[key_type.decode() if isinstance(key_type, bytes) else key_type] += 1
            
            if show_details and i < max_details:
                value = r.get(key)
                value_str = format_value(value) if value else None
                sample_keys.append({
                    'key': key_str,
                    'type': key_type.decode() if isinstance(key_type, bytes) else key_type,
                    'ttl': ttl,
                    'value': value_str,
                    'size': len(value) if value else 0
                })
        except Exception as e:
            print(f"  Warning: Error analyzing key {format_key(key)}: {e}")
    
    # Display statistics
    print(f"\nKey type distribution:")
    for key_type, count in sorted(key_types.items(), key=lambda x: x[1], reverse=True):
        print(f"  {key_type}: {count}")
    
    print(f"\nTTL distribution:")
    ttl_categories = {
        'Never expires': [k for k in ttl_stats.keys() if k == -1],
        'Expired': [k for k in ttl_stats.keys() if k == -2],
        'Within 1 hour': [k for k in ttl_stats.keys() if 0 < k <= 3600],
        '1-24 hours': [k for k in ttl_stats.keys() if 3600 < k <= 86400],
        'Over 24 hours': [k for k in ttl_stats.keys() if k > 86400]
    }
    
    for category, ttls in ttl_categories.items():
        count = sum(ttl_stats[ttl] for ttl in ttls)
        if count > 0:
            print(f"  {category}: {count}")
    
    # Display sample keys
    if show_details and sample_keys:
        print(f"\nSample key details (first {min(max_details, len(sample_keys))}):")
        for i, info in enumerate(sample_keys, 1):
            # Format TTL (avoid nested f-string, compatible with Python 3.6)
            if info['ttl'] == -1:
                ttl_str = 'Never expires'
            elif info['ttl'] == -2:
                ttl_str = 'Expired'
            else:
                hours = info['ttl'] // 3600
                minutes = (info['ttl'] % 3600) // 60
                ttl_str = f'{hours}h {minutes}m'
            
            print(f"\n  [{i}] {info['key']}")
            print(f"      Type: {info['type']}")
            print(f"      TTL: {info['ttl']}s ({ttl_str})")
            print(f"      Size: {info['size']} bytes")
            if info['value']:
                print(f"      Value: {info['value']}")


def get_redis_stats(r):
    """Get Redis statistics"""
    print(f"\n{'='*60}")
    print("Redis Statistics")
    print(f"{'='*60}")
    
    try:
        info = r.info()
        
        # Memory information
        print("\nMemory usage:")
        used_memory = info.get('used_memory_human', 'N/A')
        used_memory_peak = info.get('used_memory_peak_human', 'N/A')
        maxmemory = info.get('maxmemory_human', 'N/A')
        print(f"  Used: {used_memory}")
        print(f"  Peak: {used_memory_peak}")
        print(f"  Max limit: {maxmemory if maxmemory != '0B' else 'Unlimited'}")
        
        # Key statistics
        print("\nKey statistics:")
        db_size = r.dbsize()
        print(f"  Total keys: {db_size}")
        
        # Namespace key count
        namespace_keys = len(r.keys(f"{CACHE_NAMESPACE}:*"))
        print(f"  {CACHE_NAMESPACE} namespace keys: {namespace_keys}")
        
        # Hit rate (if available)
        if 'keyspace_hits' in info and 'keyspace_misses' in info:
            hits = info['keyspace_hits']
            misses = info['keyspace_misses']
            total = hits + misses
            if total > 0:
                hit_rate = (hits / total) * 100
                print(f"\nCache hit rate:")
                print(f"  Hits: {hits:,}")
                print(f"  Misses: {misses:,}")
                print(f"  Hit rate: {hit_rate:.2f}%")
        
    except Exception as e:
        print(f"✗ Error getting statistics: {e}")


def check_specific_key(r, key):
    """Check a specific cache key"""
    print(f"\n{'='*60}")
    print(f"Checking cache key: {key}")
    print(f"{'='*60}")
    
    full_key = f"{CACHE_NAMESPACE}:{key}" if not key.startswith(CACHE_NAMESPACE) else key
    
    try:
        exists = r.exists(full_key)
        if exists:
            value = r.get(full_key)
            ttl = r.ttl(full_key)
            key_type = r.type(full_key)
            
            # Format TTL (avoid nested f-string, compatible with Python 3.6)
            if ttl == -1:
                ttl_str = 'Never expires'
            elif ttl == -2:
                ttl_str = 'Expired'
            else:
                hours = ttl // 3600
                minutes = (ttl % 3600) // 60
                ttl_str = f'{hours}h {minutes}m'
            
            print(f"✓ Key exists")
            print(f"  Full key name: {full_key}")
            print(f"  Type: {key_type.decode() if isinstance(key_type, bytes) else key_type}")
            print(f"  TTL: {ttl}s ({ttl_str})")
            print(f"  Value: {format_value(value) if value else 'None'}")
            if value:
                print(f"  Size: {len(value)} bytes")
        else:
            print(f"✗ Key does not exist")
            print(f"  Full key name: {full_key}")
    except Exception as e:
        print(f"✗ Error checking key: {e}")


def main():
    parser = argparse.ArgumentParser(description='Detailed Redis cache key inspection tool')
    # Get Redis URL from environment variable or command line argument
    default_redis_url = os.environ.get('REDIS_URL')
    parser.add_argument('--redis-url', 
                       default=default_redis_url,
                       help='Redis connection URL (default: from REDIS_URL environment variable)')
    parser.add_argument('--pattern', default=f'{CACHE_NAMESPACE}:post_id_*', 
                       help=f'Key pattern to scan (default: {CACHE_NAMESPACE}:post_id_*)')
    parser.add_argument('--key', help='Check a specific cache key (without namespace prefix)')
    parser.add_argument('--limit', type=int, help='Limit the number of keys to scan')
    parser.add_argument('--stats', action='store_true', help='Show Redis statistics')
    parser.add_argument('--details', action='store_true', help='Show detailed key information')
    parser.add_argument('--max-details', type=int, default=10, help='Maximum number of detailed keys to show (default: 10)')
    
    args = parser.parse_args()
    
    # Validate Redis URL
    if not args.redis_url:
        print("Error: Redis URL is required")
        print("Please provide --redis-url argument or set REDIS_URL environment variable")
        sys.exit(1)
    
    # Check Redis connection
    r = check_redis_connection(args.redis_url)
    if r is None:
        sys.exit(1)
    
    print(f"✓ Redis connection successful")
    print(f"  Namespace: {CACHE_NAMESPACE}")
    
    # Check specific key
    if args.key:
        check_specific_key(r, args.key)
    
    # Scan keys
    if args.pattern:
        keys = scan_keys(r, args.pattern, limit=args.limit)
        if keys:
            analyze_keys(r, keys, show_details=args.details, max_details=args.max_details)
    
    # Show statistics
    if args.stats:
        get_redis_stats(r)
    
    print(f"\n{'='*60}")
    print("Check completed!")
    print(f"{'='*60}")


if __name__ == "__main__":
    main()
