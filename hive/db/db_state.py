"""Hive db state manager. Check if schema loaded, init synced, etc."""

import time

from hive.db.schema import setup #, teardown
from hive.db.adapter import Db

class DbState:

    _db = None

    # prop is true until initial sync complete
    _is_initial_sync = True

    # db schema version
    _ver = None

    @classmethod
    def initialize(cls):
        """Perform startup database checks.

        1) Load schema if needed
        2) Run migrations if needed
        3) Check if initial sync has completed
        """

        print("[INIT] Welcome! Initializing hive.")

        # create db schema if needed
        if not cls._is_schema_loaded():
            print("[INIT] Create db schema...")
            setup()

        # perform db migrations
        cls._check_migrations()

        # check if initial sync complete
        cls._is_initial_sync = cls._is_feed_cache_empty()
        if cls._is_initial_sync:
            print("[INIT] Continue with initial sync...")

    @classmethod
    def db(cls):
        """Get a db adapter instance."""
        if not cls._db:
            cls._db = Db.instance()
        return cls._db

    @classmethod
    def finish_initial_sync(cls):
        """Set status to initial sync complete."""
        print("[INIT] Initial sync complete!")
        cls._is_initial_sync = False

    @classmethod
    def is_initial_sync(cls):
        """Check if we're still in the process of initial sync."""
        return cls._is_initial_sync

    @staticmethod
    def status():
        """Basic health status: head block/time, current age (secs)."""
        sql = ("SELECT num, created_at, extract(epoch from created_at) ts "
               "FROM hive_blocks ORDER BY num DESC LIMIT 1")
        row = DbState.db().query_row(sql)
        return dict(db_head_block=row['num'],
                    db_head_time=str(row['created_at']),
                    db_head_age=int(time.time() - row['ts']))

    @classmethod
    def _is_schema_loaded(cls):
        """Check if the schema has been loaded into db yet."""
        # check if database has been initialized (i.e. schema loaded)
        engine = cls.db().engine_name()
        if engine == 'postgresql':
            return bool(cls.db().query_one("""
                SELECT 1 FROM pg_catalog.pg_tables WHERE schemaname = 'public'
            """))
        elif engine == 'mysql':
            return bool(cls.db().query_one('SHOW TABLES'))
        raise Exception("unknown db engine %s" % engine)

    @classmethod
    def _is_feed_cache_empty(cls):
        """Check if the hive_feed_cache table is empty.

        If empty, it indicates that the initial sync has not finished.
        """
        return not cls.db().query_one("SELECT 1 FROM hive_feed_cache LIMIT 1")

    @classmethod
    def _check_migrations(cls):
        """Check current migration version and perform updates as needed."""
        cls._ver = cls.db().query_one("SELECT db_version FROM hive_state LIMIT 1")
        assert cls._ver is not None, 'could not load state record'

        if cls._ver == 0:
            # first run!
            cls._set_ver(1)

        # Example migration:
        #if cls._ver == 1:
        #    cls.db().query("ALTER TABLE hive_posts ALTER COLUMN author SET DEFAULT ''")
        #    cls._set_ver(2)


    @classmethod
    def _set_ver(cls, ver):
        """Sets the db/schema version number. Enforce sequential."""
        assert cls._ver is not None, 'version needs to be read before updating'
        assert ver == cls._ver + 1, 'version must follow previous'
        cls.db().query("UPDATE hive_state SET db_version = %d" % ver)
        cls._ver = ver
        print("[HIVE] db migrated to version: %d" % ver)
