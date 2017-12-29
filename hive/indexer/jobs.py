import logging
import time

from hive.db.methods import query, query_all, query_col

logger = logging.getLogger(__name__)

# the feed cache allows for efficient querying of blogs+reblogs. this method
# efficiently builds the feed cache after the initial sync.
def rebuild_feed_cache(truncate=True):
    print("[INIT] Rebuilding hive feed cache, this will take a few minutes.")
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

    print("[INIT] Rebuilt hive feed cache in {}s ({}+{})".format(
        int(lap_2-lap_0), int(lap_1-lap_0), int(lap_2-lap_1)))


# identify and insert missing cache rows
def select_missing_posts(limit=None, fast_mode=True):
    if fast_mode:
        where = "id > (SELECT COALESCE(MAX(post_id), 0) FROM hive_posts_cache)"
    else:
        all_ids = query_col("SELECT id FROM hive_posts WHERE is_deleted = '0'")
        cached_ids = query_col("SELECT post_id FROM hive_posts_cache")
        missing_ids = set(all_ids) - set(cached_ids)
        if not missing_ids:
            return []
        where = "id IN (%s)" % ','.join(map(str, missing_ids))

    if limit:
        limit = "LIMIT %d" % limit
    else:
        limit = ""

    sql = ("SELECT id, author, permlink FROM hive_posts "
           "WHERE is_deleted = '0' AND %s ORDER BY id %s" % (where, limit))
    return list(query(sql))


# when a post gets paidout ensure we update its final state
def select_paidout_posts(block_date):
    sql = """
    SELECT post_id, author, permlink FROM hive_posts_cache
    WHERE is_paidout = '0' AND payout_at <= :date
    """
    return list(query(sql, date=block_date))

# (debug) thorough scan for missing posts_cache records
def audit_missing_posts():
    start = 20400000
    id1 = query_col("SELECT id FROM hive_posts WHERE is_deleted = '0' AND id >= %d" % start)
    id2 = query_col("SELECT post_id FROM hive_posts_cache WHERE post_id >= %d" % start)
    missing = set(id1) - set(id2)

    print("missing count: %d -- %s" % (len(missing), missing))
    if not missing:
        return

    sql = "SELECT id, author, permlink, to_char(created_at, 'YYYY-MM-DD HH24:MI') created FROM hive_posts WHERE id IN :ids"
    rows = query_all(sql, ids=tuple(missing))
    for row in rows:
        print(row)


if __name__ == '__main__':
    audit_missing_posts()
    pass
