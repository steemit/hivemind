"""Redis cache utilities for indexer and server."""

import logging
import hashlib
from urllib.parse import urlparse

from aiocache import Cache
from hive.server.db import CACHE_NAMESPACE

log = logging.getLogger(__name__)


class RedisCacheManager:
    """Shared Redis cache manager for indexer and server.

    Provides both async methods (for server) and sync methods (for indexer).
    """

    _cache = None  # async aiocache instance
    _sync_client = None  # sync redis client for indexer

    @classmethod
    def init(cls, redis_url):
        """Initialize Redis connection.

        Args:
            redis_url: Redis connection URL (e.g., redis://localhost:6379)

        Returns:
            Cache instance or None if redis_url is empty
        """
        if redis_url:
            # Async cache for server
            cls._cache = Cache.from_url(redis_url)
            log.info("RedisCacheManager: initialized async cache with url=%s",
                     redis_url[:50] + "...")

            # Sync client for indexer
            try:
                import redis
                cls._sync_client = redis.from_url(redis_url)
                log.info("RedisCacheManager: initialized sync client for indexer")
            except ImportError:
                log.warning("RedisCacheManager: redis package not installed, sync methods unavailable")
            except Exception as e:
                log.warning("RedisCacheManager: failed to init sync client: %s", e)
        else:
            log.info("RedisCacheManager: no redis_url provided, cache disabled")
        return cls._cache

    @classmethod
    def get_cache(cls):
        """Get async cache instance.

        Returns:
            Cache instance or None
        """
        return cls._cache

    @classmethod
    def get_sync_client(cls):
        """Get sync redis client instance.

        Returns:
            Redis client or None
        """
        return cls._sync_client

    @classmethod
    def _build_cache_key(cls, key, namespace=CACHE_NAMESPACE):
        """Build full cache key with namespace.

        Args:
            key: Cache key
            namespace: Cache namespace

        Returns:
            Full cache key string
        """
        return f"{namespace}:{key}"

    # ============== SYNC METHODS (for indexer) ==============

    @classmethod
    def sync_delete_post_id_cache(cls, author, permlink):
        """Synchronously invalidate post_id cache.

        This should be called from indexer (sync code) when a post is
        created, deleted, or undeleted.

        Args:
            author: Post author
            permlink: Post permlink
        """
        if cls._sync_client is None:
            return

        cache_key = cls._build_cache_key(f'post_id_{author}_{permlink}')
        try:
            cls._sync_client.delete(cache_key)
            log.debug("RedisCacheManager: [sync] invalidated cache key=%s", cache_key)
        except Exception as e:
            log.warning("RedisCacheManager: [sync] failed to invalidate cache key=%s, error=%s",
                        cache_key, e)

    @classmethod
    def sync_delete_post_content_cache(cls, author, permlink):
        """Synchronously invalidate post content cache (bridge_get_post_*).

        Args:
            author: Post author
            permlink: Post permlink
        """
        if cls._sync_client is None:
            return

        # The cache key in get_post uses MD5 hash
        cache_key_str = f'get_post_{author}_{permlink}'
        cache_key = cls._build_cache_key(
            'bridge_get_post_' + hashlib.md5(cache_key_str.encode()).hexdigest()
        )

        try:
            cls._sync_client.delete(cache_key)
            log.debug("RedisCacheManager: [sync] invalidated post content cache key=%s", cache_key)
        except Exception as e:
            log.warning("RedisCacheManager: [sync] failed to invalidate post content cache, error=%s", e)

    @classmethod
    def sync_delete_all_post_caches(cls, author, permlink):
        """Synchronously invalidate all caches related to a post.

        Args:
            author: Post author
            permlink: Post permlink
        """
        cls.sync_delete_post_id_cache(author, permlink)
        cls.sync_delete_post_content_cache(author, permlink)

    # ============== ASYNC METHODS (for server) ==============

    @classmethod
    async def delete_post_id_cache(cls, author, permlink):
        """Asynchronously invalidate post_id cache.

        This should be called when a post is created, deleted, or undeleted
        to ensure the cache doesn't return stale "not found" results.

        Args:
            author: Post author
            permlink: Post permlink
        """
        if cls._cache is None:
            return

        cache_key = f'post_id_{author}_{permlink}'
        try:
            await cls._cache.delete(cache_key, namespace=CACHE_NAMESPACE)
            log.debug("RedisCacheManager: invalidated cache key=%s", cache_key)
        except Exception as e:
            log.warning("RedisCacheManager: failed to invalidate cache key=%s, error=%s",
                        cache_key, e)

    @classmethod
    async def delete_post_content_cache(cls, author, permlink):
        """Asynchronously invalidate post content cache (bridge_get_post_*).

        Args:
            author: Post author
            permlink: Post permlink
        """
        if cls._cache is None:
            return

        # The cache key in get_post uses MD5 hash
        cache_key_str = f'get_post_{author}_{permlink}'
        cache_key = 'bridge_get_post_' + hashlib.md5(cache_key_str.encode()).hexdigest()

        try:
            await cls._cache.delete(cache_key, namespace=CACHE_NAMESPACE)
            log.debug("RedisCacheManager: invalidated post content cache key=%s", cache_key)
        except Exception as e:
            log.warning("RedisCacheManager: failed to invalidate post content cache, error=%s", e)

    @classmethod
    async def delete_all_post_caches(cls, author, permlink):
        """Asynchronously invalidate all caches related to a post.

        Args:
            author: Post author
            permlink: Post permlink
        """
        await cls.delete_post_id_cache(author, permlink)
        await cls.delete_post_content_cache(author, permlink)
