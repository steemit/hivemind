import time

from hive.db.schema import setup #, teardown
from hive.db.methods import db_engine, query_one, query, query_row

class DbState:

    # prop is true until initial sync complete
    _is_initial_sync = True

    # prop is true when following head block
    _is_live = False

    # db schema version
    _ver = None

    @classmethod
    def initialize(cls):
        # create db schema if needed
        if not cls._is_schema_loaded():
            print("[INIT] Initializing db...")
            setup()

        # perform db migrations
        cls._check_migrations()

        # check if initial sync complete
        cls._is_initial_sync = cls._is_feed_cache_empty()
        if cls._is_initial_sync:
            print("[INIT] Continue with initial sync...")

    @classmethod
    def finish_initial_sync(cls):
        print("[INIT] Initial sync complete!")
        cls._is_initial_sync = False

    @classmethod
    def start_listen(cls):
        cls._is_live = True

    @classmethod
    def stop_listen(cls):
        cls._is_live = False

    @classmethod
    def is_listen_mode(cls):
        return cls._is_live

    @classmethod
    def is_initial_sync(cls):
        return cls._is_initial_sync

    @staticmethod
    def status():
        sql = ("SELECT num, created_at, extract(epoch from created_at) ts "
               "FROM hive_blocks ORDER BY num DESC LIMIT 1")
        row = query_row(sql)
        return dict(db_head_block=row['num'],
                    db_head_time=str(row['created_at']),
                    db_head_age=int(time.time() - row['ts']))

    @classmethod
    def _is_schema_loaded(cls):
        # check if database has been initialized (i.e. schema loaded)
        engine = db_engine()
        if engine == 'postgresql':
            return bool(query_one("""
                SELECT 1 FROM pg_catalog.pg_tables WHERE schemaname = 'public'
            """))
        elif engine == 'mysql':
            return bool(query_one('SHOW TABLES'))
        raise Exception("unknown db engine %s" % engine)

    @classmethod
    def _is_feed_cache_empty(cls):
        return not query_one("SELECT 1 FROM hive_feed_cache LIMIT 1")

    @classmethod
    def _check_migrations(cls):
        cls._ver = query_one("SELECT db_version FROM hive_state LIMIT 1")

        #assert cls._ver, 'could not load state record'
        if cls._ver is None:
            query("""
              INSERT INTO hive_state (block_num, db_version, steem_per_mvest,
              usd_per_steem, sbd_per_steem, dgpo) VALUES (0, 1, 0, 0, 0, '')
            """)
            cls._ver = 1

        if cls._ver == 0:
            cls._set_schema_ver(1)

        if cls._ver == 1:
            query("ALTER TABLE hive_posts ALTER COLUMN category SET DEFAULT ''")
            cls._set_schema_ver(2)

        if cls._ver == 2:
            cols = ['steem_per_mvest', 'usd_per_steem', 'sbd_per_steem']
            for col in cols:
                query("ALTER TABLE hive_state ALTER COLUMN %s TYPE numeric(8,3)"
                      % col)
            cls._set_schema_ver(3)

    @classmethod
    def _set_schema_ver(cls, ver):
        assert cls._ver, 'version needs to be read before updating'
        assert ver == cls._ver + 1, 'version must follow previous'
        query("UPDATE hive_state SET db_version = %d" % ver)
        print("[HIVE] db migrated to version: %d" % ver)
        cls._ver = ver
