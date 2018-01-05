import logging
import time
import re
import atexit

from hive.db import conn
from funcy.seqs import first
from sqlalchemy import text

class QueryStats:
    stats = {}
    ttltime = 0.0

    @classmethod
    def log(cls, sql, ms):
        nsql = re.sub('\s+', ' ', sql).strip()[0:256] #normalize
        nsql = re.sub('VALUES (\s*\([^\)]+\),?)+', 'VALUES (...)', nsql)
        if nsql not in cls.stats:
            cls.stats[nsql] = [0, 0]
        cls.stats[nsql][0] += ms
        cls.stats[nsql][1] += 1
        cls.ttltime += ms
        if cls.ttltime > 30 * 60 * 1000:
            cls.print()

    @classmethod
    def print(cls):
        if not cls.stats:
            return
        ttl = cls.ttltime
        print("[DEBUG] total SQL time: {}s".format(int(ttl / 1000)))
        for arr in sorted(cls.stats.items(), key=lambda x: -x[1][0])[0:40]:
            sql, vals = arr
            ms, calls = vals
            print("% 5.1f%% % 10.2fms % 8.2favg % 8dx -- %s"
                  % (100 * ms/ttl, ms, ms/calls, calls, sql[0:180]))
        cls.clear()

    @classmethod
    def clear(cls):
        cls.stats = {}
        cls.ttltime = 0

atexit.register(QueryStats.print)

logger = logging.getLogger(__name__)

_trx_active = False

# generic
# -------
def query(sql, **kwargs):
    action = sql.strip()[0:6].strip()
    if action not in ['DELETE', 'UPDATE', 'INSERT', 'COMMIT', 'START']:
        raise Exception("query() only for writes. {}".format(sql))
    __query(sql, **kwargs)

def __query(sql, **kwargs):
    global _trx_active
    if sql == 'START TRANSACTION':
        assert not _trx_active
        _trx_active = True
    elif sql == 'COMMIT':
        assert _trx_active
        _trx_active = False
    ti = time.perf_counter()
    _query = text(sql).execution_options(autocommit=False)
    try:
        res = conn.execute(_query, **kwargs)
        ms = int((time.perf_counter() - ti) * 1000)
        QueryStats.log(sql, ms)
        if ms > 100:
            disp = re.sub('\s+', ' ', sql).strip()[:250]
            print("\033[93m[SQL][{}ms] {}\033[0m".format(ms, disp))
        return res
    except Exception as e:
        print("[SQL] Error in query {} ({})".format(sql, kwargs))
        conn.close()
        logger.exception(e)
        raise e

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

def db_needs_setup():
    db = conn.dialect.name
    if db == 'postgresql':
        return not query_row("""
            SELECT * FROM pg_catalog.pg_tables WHERE schemaname = 'public'
        """)
    elif db == 'mysql':
        return not query_row('SHOW TABLES')
    raise Exception("db engine %s not supported" % db)
