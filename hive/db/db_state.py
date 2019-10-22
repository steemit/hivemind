"""Hive db state manager. Check if schema loaded, init synced, etc."""

#pylint: disable=too-many-lines

import time
import logging

from hive.db.schema import (setup, reset_autovac, build_metadata,
                            build_metadata_community, teardown, DB_VERSION)
from hive.db.adapter import Db

log = logging.getLogger(__name__)

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

        log.info("[INIT] Welcome to hive!")

        # create db schema if needed
        if not cls._is_schema_loaded():
            log.info("[INIT] Create db schema...")
            setup(cls.db())
            cls._before_initial_sync()

        # perform db migrations
        cls._check_migrations()

        # check if initial sync complete
        cls._is_initial_sync = cls._is_feed_cache_empty()
        if cls._is_initial_sync:
            log.info("[INIT] Continue with initial sync...")
        else:
            log.info("[INIT] Hive initialized.")

    @classmethod
    def teardown(cls):
        """Drop all tables in db."""
        teardown(cls.db())

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
        log.info("[INIT] Initial sync complete!")

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
            'hive_posts_ix3', # (author, depth, id)
            'hive_posts_ix4', # (parent_id, id, is_deleted=0)
            'hive_posts_ix5', # (community_id>0, is_pinned=1)
            'hive_follows_ix5a', # (following, state, created_at, follower)
            'hive_follows_ix5b', # (follower, state, created_at, following)
            'hive_reblogs_ix1', # (post_id, account, created_at)
            'hive_posts_cache_ix6a', # (sc_trend, post_id, paidout=0)
            'hive_posts_cache_ix6b', # (post_id, sc_trend, paidout=0)
            'hive_posts_cache_ix7a', # (sc_hot, post_id, paidout=0)
            'hive_posts_cache_ix7b', # (post_id, sc_hot, paidout=0)
            'hive_posts_cache_ix8', # (category, payout, depth, paidout=0)
            'hive_posts_cache_ix9a', # (depth, payout, post_id, paidout=0)
            'hive_posts_cache_ix9b', # (category, depth, payout, post_id, paidout=0)
            'hive_posts_cache_ix10', # (post_id, payout, gray=1, payout>0)
            'hive_posts_cache_ix30', # API: community trend
            'hive_posts_cache_ix31', # API: community hot
            'hive_posts_cache_ix32', # API: community created
            'hive_posts_cache_ix33', # API: community payout
            'hive_posts_cache_ix34', # API: community muted
            'hive_accounts_ix3', # (vote_weight, name VPO)
            'hive_accounts_ix4', # (id, name)
            'hive_accounts_ix5', # (cached_at, name)
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

        engine = cls.db().engine()
        log.info("[INIT] Begin pre-initial sync hooks")

        for index in cls._disableable_indexes():
            log.info("Drop index %s.%s", index.table, index.name)
            index.drop(engine)

        # TODO: #111
        #for key in cls._all_foreign_keys():
        #    log.info("Drop fk %s", key.name)
        #    key.drop(engine)

        log.info("[INIT] Finish pre-initial sync hooks")

    @classmethod
    def _after_initial_sync(cls):
        """Routine which runs *once* after initial sync.

        Re-creates non-core indexes for serving APIs after init sync,
        as well as all foreign keys."""

        engine = cls.db().engine()
        log.info("[INIT] Begin post-initial sync hooks")

        for index in cls._disableable_indexes():
            log.info("Create index %s.%s", index.table, index.name)
            index.create(engine)

        # TODO: #111
        #for key in cls._all_foreign_keys():
        #    log.info("Create fk %s", key.name)
        #    key.create(engine)

        log.info("[INIT] Finish post-initial sync hooks")

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
        if engine == 'mysql':
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
        #pylint: disable=line-too-long,too-many-branches,too-many-statements
        cls._ver = cls.db().query_one("SELECT db_version FROM hive_state LIMIT 1")
        assert cls._ver is not None, 'could not load state record'

        if cls._ver == 0:
            raise Exception("dbv cannot be 0; reindex required")

        if cls._ver == 1:
            cls._set_ver(2)

        if cls._ver == 2:
            cls._set_ver(3)

        if cls._ver == 3:
            cls.db().query("CREATE INDEX hive_accounts_ix3 ON hive_accounts (vote_weight, name varchar_pattern_ops)")
            cls._set_ver(4)

        if cls._ver == 4:
            cls.db().query("CREATE INDEX hive_follows_ix4 ON hive_follows (follower, following) WHERE state = 2")
            cls._set_ver(5)

        if cls._ver == 5:
            # recover acct names lost to issue #151
            from hive.steem.client import SteemClient
            from hive.indexer.accounts import Accounts
            names = SteemClient().get_all_account_names()
            Accounts.load_ids()
            Accounts.register(names, '1970-01-01T00:00:00')
            Accounts.clear_ids()
            cls._set_ver(6)

        if cls._ver == 6:
            cls.db().query("DROP INDEX hive_posts_cache_ix6")
            cls.db().query("CREATE INDEX hive_posts_cache_ix6a ON hive_posts_cache (sc_trend, post_id) WHERE is_paidout = '0'")
            cls.db().query("CREATE INDEX hive_posts_cache_ix6b ON hive_posts_cache (post_id, sc_trend) WHERE is_paidout = '0'")
            cls.db().query("DROP INDEX hive_posts_cache_ix7")
            cls.db().query("CREATE INDEX hive_posts_cache_ix7a ON hive_posts_cache (sc_hot, post_id) WHERE is_paidout = '0'")
            cls.db().query("CREATE INDEX hive_posts_cache_ix7b ON hive_posts_cache (post_id, sc_hot) WHERE is_paidout = '0'")
            cls._set_ver(7)

        if cls._ver == 7:
            cls.db().query("CREATE INDEX hive_accounts_ix4 ON hive_accounts (id, name)")
            cls.db().query("CREATE INDEX hive_accounts_ix5 ON hive_accounts (cached_at, name)")
            cls._set_ver(8)

        if cls._ver == 8:
            cls.db().query("DROP INDEX hive_follows_ix2")
            cls.db().query("DROP INDEX hive_follows_ix3")
            cls.db().query("DROP INDEX hive_follows_ix4")
            cls.db().query("CREATE INDEX hive_follows_5a ON hive_follows (following, state, created_at, follower)")
            cls.db().query("CREATE INDEX hive_follows_5b ON hive_follows (follower, state, created_at, following)")
            cls._set_ver(9)

        if cls._ver == 9:
            from hive.indexer.follow import Follow
            Follow.force_recount()
            cls._set_ver(10)

        if cls._ver == 10:
            cls.db().query("CREATE INDEX hive_posts_cache_ix8 ON hive_posts_cache (category, payout, depth) WHERE is_paidout = '0'")
            cls.db().query("CREATE INDEX hive_posts_cache_ix9a ON hive_posts_cache (depth, payout, post_id) WHERE is_paidout = '0'")
            cls.db().query("CREATE INDEX hive_posts_cache_ix9b ON hive_posts_cache (category, depth, payout, post_id) WHERE is_paidout = '0'")
            cls._set_ver(11)

        if cls._ver == 11:
            cls.db().query("DROP INDEX hive_posts_ix1")
            cls.db().query("DROP INDEX hive_posts_ix2")
            cls.db().query("CREATE INDEX hive_posts_ix3 ON hive_posts (author, depth, id) WHERE is_deleted = '0'")
            cls.db().query("CREATE INDEX hive_posts_ix4 ON hive_posts (parent_id, id) WHERE is_deleted = '0'")
            cls._set_ver(12)

        if cls._ver == 12: # community schema

            for table in ['hive_members', 'hive_flags', 'hive_modlog',
                          'hive_communities', 'hive_subscriptions',
                          'hive_roles', 'hive_notifs']:
                cls.db().query("DROP TABLE IF EXISTS %s" % table)
            build_metadata_community().create_all(cls.db().engine())

            cls.db().query("ALTER TABLE hive_accounts ADD COLUMN lr_notif_id integer")
            cls.db().query("ALTER TABLE hive_posts DROP CONSTRAINT hive_posts_fk2")
            cls.db().query("ALTER TABLE hive_posts DROP COLUMN community")
            cls.db().query("ALTER TABLE hive_posts ADD COLUMN community_id integer")
            cls.db().query("ALTER TABLE hive_posts_cache ADD COLUMN community_id integer")
            cls._set_ver(13)

        if cls._ver == 13:
            sqls = ("CREATE INDEX hive_posts_ix5 ON hive_posts (id) WHERE is_pinned = '1' AND is_deleted = '0'",
                    "CREATE INDEX hive_posts_ix6 ON hive_posts (community_id, id) WHERE community_id IS NOT NULL AND is_pinned = '1' AND is_deleted = '0'",
                    "CREATE INDEX hive_posts_cache_ix10 ON hive_posts_cache (post_id, payout) WHERE is_grayed = '1' AND payout > 0",
                    "CREATE INDEX hive_posts_cache_ix30 ON hive_posts_cache (community_id, sc_trend,   post_id) WHERE community_id IS NOT NULL AND is_grayed = '0' AND depth = 0",
                    "CREATE INDEX hive_posts_cache_ix31 ON hive_posts_cache (community_id, sc_hot,     post_id) WHERE community_id IS NOT NULL AND is_grayed = '0' AND depth = 0",
                    "CREATE INDEX hive_posts_cache_ix32 ON hive_posts_cache (community_id, created_at, post_id) WHERE community_id IS NOT NULL AND is_grayed = '0' AND depth = 0",
                    "CREATE INDEX hive_posts_cache_ix33 ON hive_posts_cache (community_id, payout,     post_id) WHERE community_id IS NOT NULL AND is_grayed = '0' AND is_paidout = '0'",
                    "CREATE INDEX hive_posts_cache_ix34 ON hive_posts_cache (community_id, payout,     post_id) WHERE community_id IS NOT NULL AND is_grayed = '1' AND is_paidout = '0'")
            for sql in sqls:
                cls.db().query(sql)
            cls._set_ver(14)

        if cls._ver == 14:
            for table in ['hive_members', 'hive_flags', 'hive_modlog',
                          'hive_communities', 'hive_subscriptions',
                          'hive_roles', 'hive_notifs']:
                cls.db().query("DROP TABLE IF EXISTS %s" % table)
            build_metadata_community().create_all(cls.db().engine())


        reset_autovac(cls.db())

        log.info("[HIVE] db version: %d", cls._ver)
        assert cls._ver == DB_VERSION, "migration missing or invalid DB_VERSION"
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
        log.info("[HIVE] db migrated to version: %d", ver)
