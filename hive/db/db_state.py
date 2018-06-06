"""Hive db state manager. Check if schema loaded, init synced, etc."""

import time

from hive.db.schema import setup, build_metadata # teardown
from hive.db.adapter import Db

class DbState:
    """Manages database state: sync status, migrations, etc."""

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

        print("[INIT] Welcome to hive!")

        # create db schema if needed
        if not cls._is_schema_loaded():
            print("[INIT] Create db schema...")
            setup()
            cls._before_initial_sync()

        # perform db migrations
        cls._check_migrations()

        # check if initial sync complete
        cls._is_initial_sync = cls._is_feed_cache_empty()
        if cls._is_initial_sync:
            print("[INIT] Continue with initial sync...")
        else:
            print("[INIT] Hive initialized.")

    @classmethod
    def db(cls):
        """Get a db adapter instance."""
        if not cls._db:
            cls._db = Db.instance()
        return cls._db

    @classmethod
    def finish_initial_sync(cls):
        """Set status to initial sync complete."""
        assert cls._is_initial_sync, "initial sync was not started."
        cls._after_initial_sync()
        cls._is_initial_sync = False
        print("[INIT] Initial sync complete!")

    @classmethod
    def is_initial_sync(cls):
        """Check if we're still in the process of initial sync."""
        return cls._is_initial_sync

    @classmethod
    def _all_foreign_keys(cls):
        md = build_metadata()
        out = []
        for table in md.tables.values():
            out.extend(table.foreign_keys)
        return out

    @classmethod
    def _disableable_indexes(cls):
        to_locate = [
            'hive_posts_ix1', # (parent_id)
            'hive_posts_ix2', # (is_deleted, depth)
            'hive_follows_ix2', # (following, follower, state=1)
            'hive_follows_ix3', # (follower, following, state=1)
            'hive_reblogs_ix1', # (post_id, account, created_at)
            'hive_posts_cache_ix6', # (sc_trend, post_id)
            'hive_posts_cache_ix7', # (sc_hot, post_id)
        ]

        to_return = []
        md = build_metadata()
        for table in md.tables.values():
            for index in table.indexes:
                if index.name not in to_locate:
                    continue
                to_locate.remove(index.name)
                to_return.append(index)

        # ensure we found all the items we expected
        assert not to_locate, "indexes not located: {}".format(to_locate)
        return to_return

    @classmethod
    def _before_initial_sync(cls):
        """Routine which runs *once* after db setup.

        Disables non-critical indexes for faster initial sync, as well
        as foreign key constraints."""

        engine = cls.db().create_engine()
        print("[INIT] Begin pre-initial sync hooks")

        for index in cls._disableable_indexes():
            print("Drop index %s.%s" % (index.table, index.name))
            index.drop(engine)

        # TODO: #111
        #for key in cls._all_foreign_keys():
        #    print("Drop fk %s" % (key.name))
        #    key.drop(engine)

        print("[INIT] Finish pre-initial sync hooks")

    @classmethod
    def _after_initial_sync(cls):
        """Routine which runs *once* after initial sync.

        Re-creates non-core indexes for serving APIs after init sync,
        as well as all foreign keys."""

        engine = cls.db().create_engine()
        print("[INIT] Begin post-initial sync hooks")

        for index in cls._disableable_indexes():
            print("Create index %s.%s" % (index.table, index.name))
            index.create(engine)

        # TODO: #111
        #for key in cls._all_foreign_keys():
        #    print("Create fk %s" % (key.name))
        #    key.create(engine)

        print("[INIT] Finish post-initial sync hooks")

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
            #cls._set_ver(1)
            cls._set_ver(2)

        if cls._ver == 1:
            from hive.indexer.follow import Follow
            Follow.force_recount()
            cls._set_ver(2)

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
