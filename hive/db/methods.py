import logging
from funcy.seqs import first
from hive.db import conn
from sqlalchemy import text, select, func
from decimal import Decimal

import time
import re
import atexit


class QueryStats:
    stats = {}
    ttltime = 0.0

    @classmethod
    def log(cls, sql, ms):
        nsql = re.sub('\s+', ' ', sql).strip()[0:256] #normalize
        nsql = re.sub('VALUES (\s*\([^\)]+\),?)+', 'VALUES (...)', nsql)
        if nsql not in cls.stats:
            cls.stats[nsql] = [0, 0]
        cls.stats[nsql][0] += ms
        cls.stats[nsql][1] += 1
        cls.ttltime += ms
        if cls.ttltime > 30 * 60 * 1000:
            cls.print()

    @classmethod
    def print(cls):
        ttl = cls.ttltime
        print("[DEBUG] total SQL time: {}s".format(int(ttl / 1000)))
        for arr in sorted(cls.stats.items(), key=lambda x:-x[1][0])[0:40]:
            sql, vals = arr
            ms, calls = vals
            print("% 5.1f%% % 10.2fms % 7.2favg % 8dx -- %s" % (100 * ms/ttl, ms, ms/calls, calls, sql[0:180]))
        cls.stats = {}
        cls.ttltime = 0

atexit.register(QueryStats.print)

logger = logging.getLogger(__name__)

# generic
# -------
def query(sql, **kwargs):
    ti = time.perf_counter()
    query = text(sql).execution_options(autocommit=False)
    try:
        res = conn.execute(query, **kwargs)
        ms = int((time.perf_counter() - ti) * 1000)
        QueryStats.log(sql, ms)
        if ms > 100:
            disp = re.sub('\s+', ' ', sql).strip()[:250]
            print("\033[93m[SQL][{}ms] {}\033[0m".format(ms, disp))
        logger.debug(res)
        return res
    except Exception as e:
        print("[SQL] Error in query {} ({})".format(sql, kwargs))
        conn.close()
        logger.exception(e)
        raise e

# n*m
def query_all(sql, **kwargs):
    res = query(sql, **kwargs)
    return res.fetchall()

# 1*m
def query_row(sql, **kwargs):
    res = query(sql, **kwargs)
    return first(res)

# n*1
def query_col(sql, **kwargs):
    res = query(sql, **kwargs).fetchall()
    return [r[0] for r in res]

# 1*1
def query_one(sql, **kwargs):
    row = query_row(sql, **kwargs)
    if row:
        return first(row)

def db_needs_setup():
    db = conn.dialect.name
    if db == 'postgresql':
        return not query_row("SELECT * FROM pg_catalog.pg_tables WHERE schemaname != 'pg_catalog' AND schemaname != 'information_schema'")
    elif db == 'mysql':
        return not query_row('SHOW TABLES')
    raise Exception("db engine %s not supported" % db)


async def db_head_state():
    sql = "SELECT num,created_at,extract(epoch from created_at) ts FROM hive_blocks ORDER BY num DESC LIMIT 1"
    row = query_row(sql)
    return dict(db_head_block = row['num'],
                db_head_time = str(row['created_at']),
                db_head_age = int(time.time() - row['ts']))


async def db_last_block():
    return query_one("SELECT MAX(num) FROM hive_blocks") or 0


# api specific
# ------------
async def get_followers(account: str, skip: int, limit: int):
    sql = """
    SELECT follower, created_at FROM hive_follows WHERE following = :account
    AND state = 1 ORDER BY created_at DESC LIMIT :limit OFFSET :skip
    """
    res = query(sql, account=account, skip=int(skip), limit=int(limit))
    return [[r[0],str(r[1])] for r in res.fetchall()]


async def get_following(account: str, skip: int, limit: int):
    sql = """
    SELECT following, created_at FROM hive_follows WHERE follower = :account
    AND state = 1 ORDER BY created_at DESC LIMIT :limit OFFSET :skip
    """
    res = query(sql, account=account, skip=int(skip), limit=int(limit))
    return [[r[0],str(r[1])] for r in res.fetchall()]


async def following_count(account: str):
    sql = "SELECT following FROM hive_accounts WHERE name = :a"
    return query_one(sql, a=account)


async def follower_count(account: str):
    sql = "SELECT followers FROM hive_accounts WHERE name = :a"
    return query_one(sql, a=account)


# evaluate replacing two above methods with this
async def follow_stats(account: str):
    sql = "SELECT following, followers FROM hive_accounts WHERE name = :account"
    return first(query(sql))

# all completed payouts
async def payouts_total():
    # memoized historical sum. To update:
    #  SELECT SUM(payout) FROM hive_posts_cache
    #  WHERE is_paidout = 1 AND payout_at <= precalc_date
    precalc_date = '2017-08-30 00:00:00'
    precalc_sum = Decimal('19358777.541')

    # sum all payouts since `precalc_date`
    sql = """
      SELECT SUM(payout) FROM hive_posts_cache
      WHERE is_paidout = 1 AND payout_at > '%s'
    """ % (precalc_date)

    return float(precalc_sum + query_one(sql)) #TODO: decimal

# sum of completed payouts last 24 hrs
async def payouts_last_24h():
    sql = """
      SELECT SUM(payout) FROM hive_posts_cache
      WHERE is_paidout = 1 AND payout_at > DATE_SUB(NOW(), INTERVAL 24 HOUR)
    """
    return float(query_one(sql)) # TODO: decimal


# given an array of post ids, returns full metadata in the same order
def get_posts(ids, context = None):
    sql = """
    SELECT post_id, author, permlink, title, preview, img_url, payout,
           promoted, created_at, payout_at, is_nsfw, rshares, votes, json
      FROM hive_posts_cache WHERE post_id IN :ids
    """

    reblogged_ids = []
    if context:
        reblogged_ids = query_col("SELECT post_id FROM hive_reblogs WHERE account = :a AND post_id IN :ids", a=context, ids=tuple(ids))

    # key by id so we can return sorted by input order
    posts_by_id = {}
    for row in query(sql, ids=tuple(ids)).fetchall():
        obj = dict(row)

        if context:
            voters = [csa.split(",")[0] for csa in obj['votes'].split("\n")]
            obj['user_state'] = {
                'reblogged': row['post_id'] in reblogged_ids,
                'voted': context in voters
            }

        # TODO: Object of type 'Decimal' is not JSON serializable
        obj['payout'] = float(obj['payout'])
        obj['promoted'] = float(obj['promoted'])

        # TODO: Object of type 'datetime' is not JSON serializable
        obj['created_at'] = str(obj['created_at'])
        obj['payout_at'] = str(obj['payout_at'])

        obj.pop('votes') # temp
        obj.pop('json')  # temp
        posts_by_id[row['post_id']] = obj

    # in rare cases of cache inconsistency, recover and warn
    missed = set(ids) - posts_by_id.keys()
    if missed:
        print("WARNING: get_posts do not exist in cache: {}".format(missed))
        for id in missed:
            ids.remove(id)

    return [posts_by_id[id] for id in ids]


# builds SQL query to pull a list of posts for any sort order or tag
# sort can be: trending hot new promoted
async def get_discussions_by_sort_and_tag(sort, tag, skip, limit, context = None):
    if skip > 5000:
        raise Exception("cannot skip {} results".format(skip))
    if limit > 100:
        raise Exception("cannot limit {} results".format(limit))

    order = ''
    where = []

    if sort == 'trending':
        order = 'sc_trend DESC'
    elif sort == 'hot':
        order = 'sc_hot DESC'
    elif sort == 'new':
        order = 'post_id DESC'
        where.append('depth = 0')
    elif sort == 'promoted':
        order = 'promoted DESC'
        where.append('is_paidout = 0')
        where.append('promoted > 0')
    else:
        raise Exception("unknown sort order {}".format(sort))

    if tag:
        where.append('post_id IN (SELECT post_id FROM hive_post_tags WHERE tag = :tag)')

    if where:
        where = 'WHERE ' + ' AND '.join(where)
    else:
        where = ''

    sql = "SELECT post_id FROM hive_posts_cache %s ORDER BY %s LIMIT :limit OFFSET :skip" % (where, order)
    ids = [r[0] for r in query(sql, tag=tag, limit=limit, skip=skip).fetchall()]
    return get_posts(ids, context)


# returns "homepage" feed for specified account
async def get_user_feed(account: str, skip: int, limit: int, context: str = None):
    sql = """
      SELECT post_id, string_agg(name, ',') accounts
        FROM hive_feed_cache
        JOIN hive_accounts ON account_id = hive_accounts.id
       WHERE account_id IN (SELECT following FROM hive_follows
                          WHERE follower = (SELECT id FROM hive_accounts WHERE name = :account) AND state = 1)
    GROUP BY post_id
    ORDER BY MIN(hive_feed_cache.created_at) DESC LIMIT :limit OFFSET :skip
    """
    res = query_all(sql, account = account, skip = skip, limit = limit)
    posts = get_posts([r[0] for r in res], context)

    # Merge reblogged_by data into result set
    accts = dict(res)
    for post in posts:
        rby = set(accts[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts


# returns a blog feed (posts and reblogs from the specified account)
async def get_blog_feed(account: str, skip: int, limit: int, context: str = None):
    #sql = """
    #    SELECT id, created_at
    #      FROM hive_posts
    #     WHERE depth = 0 AND is_deleted = 0 AND author = :account
    # UNION ALL
    #    SELECT post_id, created_at
    #      FROM hive_reblogs
    #     WHERE account = :account AND (SELECT is_deleted FROM hive_posts
    #                                   WHERE id = post_id) = 0
    #  ORDER BY created_at DESC
    #     LIMIT :limit OFFSET :skip
    #"""
    sql = ("SELECT post_id FROM hive_feed_cache WHERE account_id = (SELECT id FROM hive_accounts WHERE name = :account) "
            "ORDER BY created_at DESC LIMIT :limit OFFSET :skip")
    post_ids = query_col(sql, account = account, skip = skip, limit = limit)
    return get_posts(post_ids, context)


async def get_related_posts(account: str, permlink: str):
    sql = """
      SELECT p2.id
        FROM hive_posts p1
        JOIN hive_posts p2 ON p1.category = p2.category
        JOIN hive_posts_cache pc ON p2.id = pc.post_id
       WHERE p1.author = :a AND p1.permlink = :p
         AND sc_trend > :t AND p1.id != p2.id
    ORDER BY sc_trend DESC LIMIT 5
    """
    thresh = time.time() / 480000
    post_ids = query_col(sql, a=account, p=permlink, t=thresh)
    return get_posts(post_ids)


