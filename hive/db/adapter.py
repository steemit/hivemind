"""Wrapper for sqlalchemy, providing a simple interface."""

import logging
from time import perf_counter as perf
from collections import OrderedDict
from funcy.seqs import first
import sqlalchemy

from hive.conf import Conf
from hive.utils.stats import Stats

logging.getLogger('sqlalchemy.engine').setLevel(logging.WARNING)

log = logging.getLogger(__name__)

class Db:
    """RDBMS adapter for hive. Handles connecting and querying."""

    _instance = None
    @classmethod
    def instance(cls):
        """Get a lazily-initialized singleton."""
        if not cls._instance:
            cls._instance = Db()
        return cls._instance

    def __init__(self):
        """Initialize an instance.

        No work is performed here. Some modues might initialize an
        instance before config is loaded.
        """
        self._conn = None
        self._trx_active = False
        self._prep_sql = {}

    def conn(self):
        """Get the lazily-initialized db connection."""
        if not self._conn:
            self._conn = Db.create_engine(echo=False).connect()
            # It seems as though sqlalchemy tries to take over transactions
            # and handle them itself; seems to issue a START TRANSACTION on
            # connect, which makes postgres complain when we start our own:
            #
            # > WARNING:  there is already a transaction in progress
            #
            # TODO: handle this behavior properly. In the meantime,
            self._conn.execute(sqlalchemy.text("COMMIT"))
        return self._conn

    @staticmethod
    def create_engine(echo=False):
        """Create a new SA db engine. Use echo=True for ultra verbose."""
        db_url = Conf.get('database_url')
        assert db_url, ('--database-url (or DATABASE_URL env) not specified; '
                        'e.g. postgresql://user:pass@localhost:5432/hive')
        engine = sqlalchemy.create_engine(
            db_url,
            isolation_level="READ UNCOMMITTED", # only supported in mysql
            pool_recycle=3600,
            echo=echo)
        return engine

    def is_trx_active(self):
        """Check if a transaction is in progress."""
        return self._trx_active

    def query(self, sql, **kwargs):
        """Perform a (*non-`SELECT`*) write query."""

        # if prepared tuple, unpack
        if isinstance(sql, tuple):
            assert not kwargs
            assert isinstance(sql[0], str)
            assert isinstance(sql[1], dict)
            sql, kwargs = sql

        # this method is reserved for anything but SELECT
        assert self._is_write_query(sql), sql
        return self._query(sql, **kwargs)

    def query_all(self, sql, **kwargs):
        """Perform a `SELECT n*m`"""
        res = self._query(sql, **kwargs)
        return res.fetchall()

    def query_row(self, sql, **kwargs):
        """Perform a `SELECT 1*m`"""
        res = self._query(sql, **kwargs)
        return first(res)

    def query_col(self, sql, **kwargs):
        """Perform a `SELECT n*1`"""
        res = self._query(sql, **kwargs).fetchall()
        return [r[0] for r in res]

    def query_one(self, sql, **kwargs):
        """Perform a `SELECT 1*1`"""
        row = first(self._query(sql, **kwargs))
        return first(row) if row else None

    def engine_name(self):
        """Get the name of the engine (e.g. `postgresql`, `mysql`)."""
        engine = self.conn().dialect.name
        if engine not in ['postgresql', 'mysql']:
            raise Exception("db engine %s not supported" % engine)
        return engine

    def batch_queries(self, queries, trx):
        """Process batches of prepared SQL tuples.

        If `trx` is true, the queries will be wrapped in a transaction.
        The format of queries is `[(sql, {params*}), ...]`
        """
        if trx:
            self.query("START TRANSACTION")
        for (sql, params) in queries:
            self.query(sql, **params)
        if trx:
            self.query("COMMIT")

    @staticmethod
    def build_insert(table, values, pk=None):
        """Generates an INSERT statement w/ bindings."""
        values = OrderedDict(values)

        # Delete PK field if blank
        if pk:
            pks = [pk] if isinstance(pk, str) else pk
            for key in pks:
                if not values[key]:
                    del values[key]

        fields = list(values.keys())
        cols = ', '.join([k for k in fields])
        params = ', '.join([':'+k for k in fields])
        sql = "INSERT INTO %s (%s) VALUES (%s)"
        sql = sql % (table, cols, params)

        return (sql, values)

    @staticmethod
    def build_update(table, values, pk):
        """Generates an UPDATE statement w/ bindings."""
        assert pk and isinstance(pk, (str, list))
        pks = [pk] if isinstance(pk, str) else pk
        values = OrderedDict(values)
        fields = list(values.keys())

        update = ', '.join([k+" = :"+k for k in fields if k not in pks])
        where = ' AND '.join([k+" = :"+k for k in fields if k in pks])
        sql = "UPDATE %s SET %s WHERE %s"
        sql = sql % (table, update, where)

        return (sql, values)

    def _sql_text(self, sql):
        if sql in self._prep_sql:
            query = self._prep_sql[sql]
        else:
            query = sqlalchemy.text(sql).execution_options(autocommit=False)
            self._prep_sql[sql] = query
        return query

    def _query(self, sql, **kwargs):
        """Send a query off to SQLAlchemy."""
        if sql == 'START TRANSACTION':
            assert not self._trx_active
            self._trx_active = True
        elif sql == 'COMMIT':
            assert self._trx_active
            self._trx_active = False

        query = self._sql_text(sql)

        try:
            start = perf()
            result = self.conn().execute(query, **kwargs)
            Stats.log_db(sql, perf() - start)
            return result
        except Exception as e:
            log.error("[SQL-ERR] %s in query %s (%s)",
                      e.__class__.__name__, sql, kwargs)
            raise e

    @staticmethod
    def _is_write_query(sql):
        """Check if `sql` is a DELETE, UPDATE, COMMIT, ALTER, etc."""
        action = sql.strip()[0:6].strip()
        if action == 'SELECT':
            return False
        if action in ['DELETE', 'UPDATE', 'INSERT', 'COMMIT', 'START',
                      'ALTER', 'TRUNCA', 'CREATE']:
            return True
        raise Exception("unknown action: {}".format(sql))
