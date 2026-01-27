"""Async DB adapter for hivemind API."""

import logging
from time import perf_counter as perf

import sqlalchemy
from sqlalchemy.engine.url import make_url
from aiopg.sa import create_engine
from aiocache import Cache
from hive.utils.safe_serializer import SafeUniversalSerializer

from hive.utils.stats import Stats

logging.getLogger('sqlalchemy.engine').setLevel(logging.WARNING)
log = logging.getLogger(__name__)

CACHE_NAMESPACE = "hivemind"

# Sentinel value to represent 'record not found' in cache.
# Using a string marker that can be easily serialized/deserialized.
# This allows us to distinguish between:
# - Cache miss (None from cache.get) → query DB
# - Record doesn't exist (sentinel in cache) → return None (cached)
# - Record exists (value in cache) → return value (cached)
_CACHE_NOT_FOUND = "__CACHE_NOT_FOUND__"

def sqltimer(function):
    """Decorator for DB query methods which tracks timing."""
    async def _wrapper(*args, **kwargs):
        start = perf()
        result = await function(*args, **kwargs)
        Stats.log_db(args[1], perf() - start)
        return result
    return _wrapper

"""
How to use cacher
db.query(sql, cache_key="", cache_ttl=3600)
"""
def cacher(func):
    """Decorator for DB query result cache."""
    async def _wrapper(*args, **kwargs):
        if 'cache_key' in kwargs and args[0].redis_cache is not None:
            v = await args[0].redis_cache.get(kwargs["cache_key"], namespace=CACHE_NAMESPACE)
            if Stats._db.DEBUG_SQL:
                log.debug("[CACHE-DEBUG] cache_key: %s, value: %s", kwargs["cache_key"], v)
            if v is None:
                # Cache miss: Get from DB and set to cache
                v = await func(*args, **kwargs)
                if Stats._db.DEBUG_SQL:
                    log.debug("[CACHE-DEBUG] Not fit cache, cache_key: %s, Get from DB, value: %s", kwargs["cache_key"], v)
                if "cache_ttl" in kwargs:
                    ttl = kwargs['cache_ttl']
                else:
                    ttl = 5*60
                # Use sentinel value to cache "not found" results only for query_one
                # For query_col and query_all, empty list [] is a valid result and should be cached as-is
                # For query_one, None means "record doesn't exist" and needs sentinel to distinguish from cache miss
                func_name = func.__name__
                if func_name == 'query_one' and v is None:
                    # Only use sentinel for query_one when result is None
                    cache_value = _CACHE_NOT_FOUND
                else:
                    # For query_col, query_all, query_row: cache the actual result (including empty lists)
                    cache_value = v
                await args[0].redis_cache.set(kwargs['cache_key'], cache_value, ttl=ttl, namespace=CACHE_NAMESPACE)
                return v
            elif v == _CACHE_NOT_FOUND:
                # Cache hit with sentinel: record doesn't exist (cached) - only for query_one
                return None
            else:
                # Cache hit with value: record exists (cached) or empty list for query_col/query_all
                return v
        else:
            return await func(*args, **kwargs)
    return _wrapper

class Db:
    """Wrapper for aiopg.sa db driver."""

    @classmethod
    async def create(cls, url, redis_url=None, pool_size=20):
        """Factory method."""
        instance = Db()
        await instance.init(url, redis_url, pool_size)
        return instance

    def __init__(self):
        self.db = None
        self.redis_cache = None
        self._prep_sql = {}

    async def init(self, url, redis_url, pool_size=20):
        """Initialize the aiopg.sa engine."""
        conf = make_url(url)
        self.db = await create_engine(user=conf.username,
                                      database=conf.database,
                                      password=conf.password,
                                      host=conf.host,
                                      port=conf.port,
                                      maxsize=pool_size,
                                      **conf.query)
        if redis_url is not None:
            self.redis_cache = Cache.from_url(redis_url)
            self.redis_cache.serializer = SafeUniversalSerializer()

    def close(self):
        """Close pool."""
        self.db.close()
        if self.redis_cache is not None:
            self.redis_cache.close()

    async def wait_closed(self):
        """Wait for releasing and closing all acquired connections."""
        await self.db.wait_closed()

    @sqltimer
    @cacher
    async def query_all(self, sql, **kwargs):
        """Perform a `SELECT n*m`"""
        async with self.db.acquire() as conn:
            cur = await self._query(conn, sql, **kwargs)
            res = await cur.fetchall()
        return res

    @sqltimer
    @cacher
    async def query_row(self, sql, **kwargs):
        """Perform a `SELECT 1*m`"""
        async with self.db.acquire() as conn:
            cur = await self._query(conn, sql, **kwargs)
            res = await cur.first()
        return res

    @sqltimer
    @cacher
    async def query_col(self, sql, **kwargs):
        """Perform a `SELECT n*1`"""
        async with self.db.acquire() as conn:
            cur = await self._query(conn, sql, **kwargs)
            res = await cur.fetchall()
        return [r[0] for r in res]

    @sqltimer
    @cacher
    async def query_one(self, sql, **kwargs):
        """Perform a `SELECT 1*1`"""
        async with self.db.acquire() as conn:
            cur = await self._query(conn, sql, **kwargs)
            row = await cur.first()
        return row[0] if row else None

    @sqltimer
    @cacher
    async def query(self, sql, **kwargs):
        """Perform a write query"""
        async with self.db.acquire() as conn:
            await self._query(conn, sql, **kwargs)

    async def _query(self, conn, sql, **kwargs):
        """Send a query off to SQLAlchemy."""
        try:
            return await conn.execute(self._sql_text(sql), **kwargs)
        except Exception as e:
            log.warning("[SQL-ERR] %s in query %s (%s)",
                        e.__class__.__name__, sql, kwargs)
            raise e

    def _sql_text(self, sql):
        if sql in self._prep_sql:
            query = self._prep_sql[sql]
        else:
            query = sqlalchemy.text(sql).execution_options(autocommit=False)
            self._prep_sql[sql] = query
        return query
