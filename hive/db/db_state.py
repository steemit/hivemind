from hive.db.schema import setup, teardown
from hive.db.methods import db_needs_setup, query_one, query

class DbState:

    # prop is true until initial sync complete
    _is_initial_sync = True

    # db schema version
    _ver = None

    @classmethod
    def initialize(cls):
        # create db schema if needed
        if db_needs_setup():
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
    def is_initial_sync(cls):
        return cls._is_initial_sync

    @classmethod
    def _is_feed_cache_empty(cls):
        return not query_one("SELECT 1 FROM hive_feed_cache LIMIT 1")


    @classmethod
    def _check_migrations(cls):
        cls._ver = query_one("SELECT db_version FROM hive_state LIMIT 1")

        #assert cls._ver, 'could not load state record'
        if cls._ver == None:
            query("""
              INSERT INTO hive_state (block_num, db_version, steem_per_mvest,
              usd_per_steem, sbd_per_steem, dgpo) VALUES (0, 1, 0, 0, 0, '')
            """)
            cls._ver = 1

        if cls._ver == 0:
            cls._set_ver(1)

        if cls._ver == 1:
            query("ALTER TABLE hive_posts ALTER COLUMN category SET DEFAULT ''")
            cls._set_ver(2)

    @classmethod
    def _set_ver(cls, ver):
        assert cls._ver, 'version needs to be read before updating'
        assert ver == cls._ver + 1, 'version must follow previous'
        query("UPDATE hive_state SET db_version = %d" % ver)
        print("[HIVE] db migrated to version: %d" % ver)
        cls._ver = ver
