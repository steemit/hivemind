import logging
import time
import re
import atexit

from hive.db import conn
from funcy.seqs import first
from sqlalchemy import text

class QueryStats:
    stats = {}
    ttl_time = 0.0

    def __init__(self):
        atexit.register(QueryStats.print)

    def __call__(self, fn):
        def wrap(*args, **kwargs):
            time_start = time.perf_counter()
            result = fn(*args, **kwargs)
            time_end = time.perf_counter()
            QueryStats.log(args[0], (time_end - time_start) * 1000)
            return result
        return wrap

    @classmethod
    def log(cls, sql, ms):
        nsql = cls.normalize_sql(sql)
        cls.add_nsql_ms(nsql, ms)
        cls.check_timing(nsql, ms)
        if cls.ttl_time > 30 * 60 * 1000:
            cls.print()

    @classmethod
    def add_nsql_ms(cls, nsql, ms):
        if nsql not in cls.stats:
            cls.stats[nsql] = [ms, 1]
        else:
            cls.stats[nsql][0] += ms
            cls.stats[nsql][1] += 1
        cls.ttl_time += ms

    @classmethod
    def normalize_sql(cls, sql):
        nsql = re.sub('\s+', ' ', sql).strip()[0:256]
        nsql = re.sub('VALUES (\s*\([^\)]+\),?)+', 'VALUES (...)', nsql)
        return nsql

    @classmethod
    def check_timing(cls, nsql, ms):
        if ms > 100:
            print("\033[93m[SQL][%dms] %s\033[0m" % (ms, nsql[:250]))

    @classmethod
    def print(cls):
        if not cls.stats:
            return
        ttl = cls.ttl_time
        print("[DEBUG] total SQL time: {}s".format(int(ttl / 1000)))
        for arr in sorted(cls.stats.items(), key=lambda x: -x[1][0])[0:40]:
            sql, vals = arr
            ms, calls = vals
            print("% 5.1f%% % 7dms % 9.2favg % 8dx -- %s"
                  % (100 * ms/ttl, ms, ms/calls, calls, sql[0:180]))
        cls.clear()

    @classmethod
    def clear(cls):
        cls.stats = {}
        cls.ttl_time = 0

logger = logging.getLogger(__name__)

_trx_active = False

@QueryStats()
def __query(sql, **kwargs):
    global _trx_active
    if sql == 'START TRANSACTION':
        assert not _trx_active
        _trx_active = True
    elif sql == 'COMMIT':
        assert _trx_active
        _trx_active = False

    _query = text(sql).execution_options(autocommit=False)
    try:
        return conn.execute(_query, **kwargs)
    except Exception as e:
        print("[SQL] Error in query {} ({})".format(sql, kwargs))
        #conn.close() # TODO: check if needed
        logger.exception(e)
        raise e

def query(sql, **kwargs):
    action = sql.strip()[0:6].strip()
    if action not in ['DELETE', 'UPDATE', 'INSERT', 'COMMIT', 'START', 'ALTER']:
        raise Exception("query() only for writes. {}".format(sql))
    return __query(sql, **kwargs)

# n*m
def query_all(sql, **kwargs):
    res = __query(sql, **kwargs)
    return res.fetchall()

# 1*m
def query_row(sql, **kwargs):
    res = __query(sql, **kwargs)
    return first(res)

# n*1
def query_col(sql, **kwargs):
    res = __query(sql, **kwargs).fetchall()
    return [r[0] for r in res]

# 1*1
def query_one(sql, **kwargs):
    row = query_row(sql, **kwargs)
    if row:
        return first(row)

def db_engine():
    engine = conn.dialect.name
    if engine not in ['postgresql', 'mysql']:
        raise Exception("db engine %s not supported" % engine)
    return engine
