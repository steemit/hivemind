import time
from hive.db.methods import query
from hive.db.db_state import DbState

#TODO: ignore insert/delete on initial sync
class FeedCache:

    @classmethod
    def insert(cls, post_id, account_id, created_at):
        assert not DbState.is_initial_sync(), 'writing to feed cache in sync'
        sql = """INSERT INTO hive_feed_cache (account_id, post_id, created_at)
                      VALUES (:account_id, :id, :created_at)
                 ON CONFLICT (account_id, post_id) DO NOTHING"""
        query(sql, account_id=account_id, id=post_id, created_at=created_at)

    @classmethod
    def delete(cls, post_id, account_id=None):
        assert not DbState.is_initial_sync(), 'writing to feed cache in sync'
        sql = "DELETE FROM hive_feed_cache WHERE post_id = :id"
        if account_id:
            sql = sql + " AND account_id = :account_id"
        query(sql, account_id=account_id, id=post_id)

    # the feed cache allows for efficient querying of blogs+reblogs. this method
    # efficiently builds the feed cache after the initial sync.
    @classmethod
    def rebuild(cls, truncate=True):
        print("[HIVE] Rebuilding feed cache, this will take a few minutes.")
        query("START TRANSACTION")
        if truncate:
            query("TRUNCATE TABLE hive_feed_cache")

        lap_0 = time.perf_counter()
        query("""
            INSERT INTO hive_feed_cache (account_id, post_id, created_at)
                 SELECT hive_accounts.id, hive_posts.id, hive_posts.created_at
                   FROM hive_posts
                   JOIN hive_accounts ON hive_posts.author = hive_accounts.name
                  WHERE depth = 0 AND is_deleted = '0'
            ON CONFLICT DO NOTHING
        """)
        lap_1 = time.perf_counter()
        query("""
            INSERT INTO hive_feed_cache (account_id, post_id, created_at)
                 SELECT hive_accounts.id, post_id, hive_reblogs.created_at
                   FROM hive_reblogs
                   JOIN hive_accounts ON hive_reblogs.account = hive_accounts.name
            ON CONFLICT DO NOTHING
        """)
        lap_2 = time.perf_counter()
        query("COMMIT")

        print("[HIVE] Rebuilt hive feed cache in {}s ({}+{})".format(
            int(lap_2-lap_0), int(lap_1-lap_0), int(lap_2-lap_1)))
