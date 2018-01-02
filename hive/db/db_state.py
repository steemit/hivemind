from hive.db.schema import setup, teardown
from hive.db.methods import db_needs_setup, query_one

class DbState:

    # prop is true until initial sync complete
    _is_initial_sync = True

    @classmethod
    def initialize(cls):
        # create db schema if needed
        if db_needs_setup():
            print("[INIT] Initializing db...")
            setup()

        # check if initial sync complete
        cls._is_initial_sync = cls._is_feed_cache_empty()
        if cls._is_initial_sync:
            print("[INIT] Continue with initial sync...")

    @classmethod
    def initial_sync_finished():
        print("[INIT] Initial sync complete!")
        cls._is_initial_sync = False

    @classmethod
    def is_initial_sync(cls):
        return cls._is_initial_sync

    @classmethod
    def _is_feed_cache_empty(cls):
        return not query_one("SELECT 1 FROM hive_feed_cache LIMIT 1")
