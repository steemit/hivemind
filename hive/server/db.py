"""Async DB adapter for hivemind API."""

import logging
from time import perf_counter as perf

import sqlalchemy
from sqlalchemy.engine.url import make_url
from aiopg.sa import create_engine

from hive.utils.stats import Stats
import aioredis, pickle

logging.getLogger('sqlalchemy.engine').setLevel(logging.WARNING)
log = logging.getLogger(__name__)

async def redis_set(cls, k, v, timeout):
    if cls:
        async with cls.pipeline(transaction=True) as pipe:
            try:
                ok1, ok2 = await (pipe.set(k, pickle.dumps(v).encode('utf-8')).expire(k, timeout).execute())
            except Exception as e:
                log.warning("[REDIS-SET_ERR] k:%s, v:%s, err: %s",k, v, e.__class__.__name__)
                raise e
            assert ok1
            assert ok2

async def redis_get(cls, k):
    if cls:
        try:
            v = await cls.get(k)
        except Exception as e:
            log.warning("[REDIS-GET_ERR] k:%s, err: %s",k, str(e))
            return None
        return pickle.loads(v)

def sqltimer(function):
    """Decorator for DB query methods which tracks timing."""
    async def _wrapper(*args, **kwargs):
        start = perf()
        result = await function(*args, **kwargs)
        Stats.log_db(args[1], perf() - start)
        return result
    return _wrapper

class Db:
    """Wrapper for aiopg.sa db driver."""

    @classmethod
    async def create(cls, url, redis_url):
        """Factory method."""
        instance = Db()
        await instance.init(url, redis_url)
        return instance

    def __init__(self):
        self.db = None
        self.redis = None
        self._prep_sql = {}

    async def init(self, url, redis_url):
        """Initialize the aiopg.sa engine."""
        conf = make_url(url)
        self.db = await create_engine(user=conf.username,
                                      database=conf.database,
                                      password=conf.password,
                                      host=conf.host,
                                      port=conf.port,
                                      maxsize=20,
                                      **conf.query)
        if redis_url:
            self.redis = await aioredis.from_url(redis_url,
                                db=1,
                                decode_responses=True)

    def close(self):
        """Close pool."""
        self.db.close()
        if self.redis:
            self.redis.close()

    async def wait_closed(self):
        """Wait for releasing and closing all acquired connections."""
        await self.db.wait_closed()

    @sqltimer
    async def query_all(self, sql, **kwargs):
        """Perform a `SELECT n*m`"""
        async with self.db.acquire() as conn:
            cur = await self._query(conn, sql, **kwargs)
            res = await cur.fetchall()
        return res
    
    async def query_all_cache(self, sql, cache_key, **kwargs):
        if self.redis:
            res = await redis_get(self.redis, cache_key)
            if res == None:
                res = await self.query_all(sql, **kwargs)
                await redis_set(self.redis, cache_key, res, 300)
            return res
        else:
            return await self.query_all(sql, **kwargs)

    @sqltimer
    async def query_row(self, sql, **kwargs):
        """Perform a `SELECT 1*m`"""
        async with self.db.acquire() as conn:
            cur = await self._query(conn, sql, **kwargs)
            res = await cur.first()
        return res
    
    async def query_row_cache(self, sql, cache_key, **kwargs):
        if self.redis:
            res = await redis_get(self.redis, cache_key)
            if res == None:
                res = await self.query_row(sql, **kwargs)
                await redis_set(self.redis, cache_key, res, 300)
            return res
        else:
            return await self.query_row(sql, **kwargs)

    @sqltimer
    async def query_col(self, sql, **kwargs):
        """Perform a `SELECT n*1`"""
        async with self.db.acquire() as conn:
            cur = await self._query(conn, sql, **kwargs)
            res = await cur.fetchall()
        return [r[0] for r in res]

    async def query_col_cache(self, sql, cache_key, **kwargs):
        if self.redis:
            res = await redis_get(self.redis, cache_key)
            if res == None:
                res = await self.query_col(sql, **kwargs)
                await redis_set(self.redis, cache_key, res, 300)
            return res
        else:
            return await self.query_col(sql, **kwargs)

    @sqltimer
    async def query_one(self, sql, **kwargs):
        """Perform a `SELECT 1*1`"""
        async with self.db.acquire() as conn:
            cur = await self._query(conn, sql, **kwargs)
            row = await cur.first()
        return row[0] if row else None

    async def query_one_cache(self, sql, cache_key, **kwargs):
        if self.redis:
            res = await redis_get(self.redis, cache_key)
            if res == None:
                res = await self.query_one(sql, **kwargs)
                await redis_set(self.redis, cache_key, res, 300)
            return res
        else:
            return await self.query_one(sql, **kwargs)

    @sqltimer
    async def query(self, sql, **kwargs):
        """Perform a write query"""
        async with self.db.acquire() as conn:
            await self._query(conn, sql, **kwargs)

    async def query_cache(self, sql, cache_key, **kwargs):
        if self.redis:
            res = await redis_get(self.redis, cache_key)
            if res == None:
                res = await self.query(sql, **kwargs)
                await redis_set(self.redis, cache_key, res, 300)
            return res
        else:
            return await self.query(sql, **kwargs)

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
