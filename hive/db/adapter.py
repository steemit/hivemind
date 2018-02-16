import logging
import collections
from funcy.seqs import first

import sqlalchemy
from sqlalchemy import text

from hive.conf import Conf
from hive.db.query_stats import QueryStats

logger = logging.getLogger(__name__)

class Db:
    _instance = None
    @classmethod
    def instance(cls):
        if not cls._instance:
            cls._instance = Db()
        return cls._instance

    def __init__(self):
        self._conn = None
        self._trx_active = False

    def conn(self):
        if not self._conn:
            self._conn = Db.create_engine(echo=False).connect()
            # It seems as though sqlalchemy tries to take over transactions
            # and handle them itself; seems to issue a START TRANSACTION on
            # connect, which makes postgres complain when we start our own:
            # > WARNING:  there is already a transaction in progress
            # TODO: handle this behavior properly. In the meantime:
            self._conn.execute(text("COMMIT"))
        return self._conn


    @staticmethod
    def create_engine(echo=False):
        engine = sqlalchemy.create_engine(
            Conf.get('database_url'),
            isolation_level="READ UNCOMMITTED", # only works in mysql
            pool_recycle=3600,
            echo=echo)
        return engine

    def is_trx_active(self):
        return self._trx_active

    # any non-SELECT queries
    def query(self, sql, **kwargs):
        # if prepared tuple, unpack
        if isinstance(sql, tuple):
            assert not kwargs
            kwargs = sql[1]
            sql = sql[0]
            assert isinstance(sql, str)
            assert isinstance(kwargs, dict)

        # this method is reserved for anything but SELECT
        assert self._is_write_query(sql), sql
        return self._query(sql, **kwargs)

    # SELECT n*m
    def query_all(self, sql, **kwargs):
        res = self._query(sql, **kwargs)
        return res.fetchall()

    # SELECT 1*m
    def query_row(self, sql, **kwargs):
        res = self._query(sql, **kwargs)
        return first(res)

    # SELECT n*1
    def query_col(self, sql, **kwargs):
        res = self._query(sql, **kwargs).fetchall()
        return [r[0] for r in res]

    # SELECT 1*1
    def query_one(self, sql, **kwargs):
        row = self.query_row(sql, **kwargs)
        if row:
            return first(row)

    def db_engine(self):
        engine = self.conn().dialect.name
        if engine not in ['postgresql', 'mysql']:
            raise Exception("db engine %s not supported" % engine)
        return engine

    @staticmethod
    def build_upsert(table, pk, values):
        pks = [pk] if isinstance(pk, str) else pk
        values = collections.OrderedDict(values)
        fields = list(values.keys())
        pks_blank = [values[k] is None for k in pks]

        if all(pks_blank):
            cols = ', '.join([k for k in fields if k not in pks])
            params = ', '.join([':'+k for k in fields if k not in pks])
            sql = "INSERT INTO %s (%s) VALUES (%s)"
            sql = sql % (table, cols, params)
        else:
            update = ', '.join([k+" = :"+k for k in fields if k not in pks])
            where = ' AND '.join([k+" = :"+k for k in fields if k in pks])
            sql = "UPDATE %s SET %s WHERE %s"
            sql = sql % (table, update, where)

        return (sql, values)

    @QueryStats()
    def _query(self, sql, **kwargs):
        if sql == 'START TRANSACTION':
            assert not self._trx_active
            self._trx_active = True
        elif sql == 'COMMIT':
            assert self._trx_active
            self._trx_active = False

        query = text(sql).execution_options(autocommit=False)
        try:
            return self.conn().execute(query, **kwargs)
        except Exception as e:
            print("[SQL] Error in query {} ({})".format(sql, kwargs))
            #self.conn.close() # TODO: check if needed
            logger.exception(e)
            raise e

    @staticmethod
    def _is_write_query(sql):
        action = sql.strip()[0:6].strip()
        if action == 'SELECT':
            return False
        if action in ['DELETE', 'UPDATE', 'INSERT', 'COMMIT', 'START', 'ALTER']:
            return True
        raise Exception("unknown action: {}".format(sql))
