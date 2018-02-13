import logging

from funcy.seqs import first
from sqlalchemy import text

from hive.db.schema import connect
from hive.utils.query_stats import QueryStats

_conn = None
def conn():
    global _conn
    if not _conn:
        _conn = connect(echo=False)
    return _conn


logger = logging.getLogger(__name__)

_trx_active = False

def is_trx_active():
    global _trx_active
    return _trx_active

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
        return conn().execute(_query, **kwargs)
    except Exception as e:
        print("[SQL] Error in query {} ({})".format(sql, kwargs))
        #conn().close() # TODO: check if needed
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
    engine = conn().dialect.name
    if engine not in ['postgresql', 'mysql']:
        raise Exception("db engine %s not supported" % engine)
    return engine
