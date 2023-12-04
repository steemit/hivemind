"""Async DB adapter for hivemind API."""

import logging
from time import perf_counter as perf

import sqlalchemy
from sqlalchemy.engine.url import make_url
from aiopg.sa import create_engine
from aiocache import Cache
from aiocache.serializers import JsonSerializer

from hive.utils.stats import Stats

logging.getLogger('sqlalchemy.engine').setLevel(logging.WARNING)
log = logging.getLogger(__name__)

CACHE_NAMESPACE = "hivemind_"

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
            v = await args[0].redis_cache.get(kwargs["cache_key"])
            if v is None:
                v = await func(*args, **kwargs)
                if v is None:
                    """
                    TODO:
                        * hit no cache => None
                        * get no record => None
                        These two conditions are conflict.
                        Need to wrap the redis_cache.get()
                    """
                    log.warning("[CACHE-LAYER-TODO] [%s] (%s)", args, kwargs)
                    return None
                if "cache_ttl" in kwargs:
                    ttl = kwargs['cache_ttl']
                else:
                    ttl = 5*60
                if isinstance(v, list):
                    d, a = {}, []
                    for row in v:
                        try:
                            for col, val in row.items():
                                # build up the dictionary
                                d = {**d, **{col: val}}
                            a.append(d)
                        except:
                            # if row is not RowProxy
                            log.warning("[CACHE-LAYER] The row is not RowProxy. row: {%s}, args: {%s}, kwargs: {%s}", row, args, kwargs)
                            a.append(row)
                    v = a
                cache_key = CACHE_NAMESPACE + kwargs['cache_key']
                await args[0].redis_cache.set(cache_key, v)
                await args[0].redis_cache.expire(cache_key, ttl)
            return v
        else:
            return await func(*args, **kwargs)
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
        self.redis_cache = None
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
        if redis_url is not None:
            self.redis_cache = Cache.from_url(redis_url)
            self.redis_cache.serializer = JsonSerializer()

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
