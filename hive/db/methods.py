from funcy.seqs import first
from hive.db import conn
from hive.db.schema import (
    hive_follows,
)
from sqlalchemy import text, select, func

import time

# generic
# -------
def query(sql, **kwargs):
    t1 = time.time()
    res = conn.execute(text(sql).execution_options(autocommit=False), **kwargs)
    t2 = time.time()
    if t2 - t1 > 0.1:
        print("[SQL][{}ms] -- {}".format(int((t2-t1)*1000), sql[:250]))
    return res

def query_all(sql, **kwargs):
    res = query(sql, **kwargs)
    return res.fetchall()

def query_row(sql, **kwargs):
    res = query(sql, **kwargs)
    return first(res)

def query_col(sql, **kwargs):
    res = query(sql, **kwargs).fetchall()
    return [r[0] for r in res]

def query_one(sql, **kwargs):
    t1 = time.time()
    res = conn.execute(text(sql), **kwargs)
    t2 = time.time()
    if t2 - t1 > 0.1:
        print("[SQL][{}ms] -- {}".format(int((t2-t1)*1000), sql[:250]))
    row = first(res)
    if row:
        return first(row)


def db_last_block():
    return query_one("SELECT MAX(num) FROM hive_blocks") or 0


# api specific
# ------------
def get_followers(account: str, skip: int, limit: int):
    sql = """
    SELECT follower, created_at FROM hive_follows WHERE following = :account
    ORDER BY created_at DESC LIMIT :limit OFFSET :skip
    """
    res = query(sql, account = account, skip = int(skip), limit = int(limit))
    return [[r[0],r[1]] for r in res.fetchall()]


def get_following(account: str, skip: int, limit: int):
    sql = """
    SELECT following, created_at FROM hive_follows WHERE follower = :account
    ORDER BY created_at DESC LIMIT :limit OFFSET :skip
    """
    res = query(sql, account = account, skip = int(skip), limit = int(limit))
    return [[r[0],r[1]] for r in res.fetchall()]


def following_count(account: str):
    sql = "SELECT COUNT(*) FROM hive_follows WHEERE follower = :account"
    return query_one(sql, account=account)


def follower_count(account: str):
    sql = "SELECT COUNT(*) FROM hive_follows WHEERE following = :account"
    return query_one(sql, account=account)


# evaluate replacing two above methods with this
def follow_stats(account: str):
    sql = """
    SELECT SUM(IF(follower  = :account, 1, 0)) following,
           SUM(IF(following = :account, 1, 0)) followers
      FROM hive_follows
     WHERE is_muted = 0
    """
    return first(query(sql))


# unused
def get_reblogs_since(account: str, since: str):
    sql = ("SELECT * FROM hive_reblogs r JOIN hive_posts p ON r.post_id = p.id "
          "WHERE p.author = :account AND r.created_at > :since ORDER BY r.created_at DESC")
    return [dict(r) for r in query(sql, account = account, since = since).fetchall()]


# given an array of post ids, returns full metadata in the same order
def get_posts(ids):
    sql = """
    SELECT post_id, author, permlink, title, preview, img_url, payout,
           promoted, created_at, payout_at, is_nsfw, rshares, votes, json
      FROM hive_posts_cache WHERE post_id IN (%s)
    """
    sql = sql % ','.join([str(id) for id in ids])
    posts = [dict(r) for r in query(sql).fetchall()]

    # key by id so we can return sorted by input order
    posts_by_id = {}
    for row in query(sql).fetchall():
        obj = dict(row)
        obj.pop('votes')
        obj.pop('json')
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
def get_discussions_by_sort_and_tag(sort, tag, skip, limit):
    if skip > 5000:
        raise Exception("cannot skip {} results".format(skip))
    if limit > 100:
        raise Exception("cannot limit {} results".format(limit))

    order = ''
    where = []
    table = 'hive_posts_cache'
    col   = 'post_id'

    if sort is 'trending':
        order = 'sc_trend DESC'
    elif sort is 'hot':
        order = 'sc_hot DESC'
    elif sort is 'new':
        order = 'id DESC'
        where.append('depth = 0')
        table = 'hive_posts'
        col = 'id'
    elif sort is 'promoted':
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

    sql = "SELECT %s FROM %s %s ORDER BY %s LIMIT :limit OFFSET :skip" % (col, table, where, order)
    ids = [r[0] for r in query(sql, tag=tag, limit=limit, skip=skip).fetchall()]
    return get_posts(ids)


# returns "homepage" feed for specified account
def get_user_feed(account: str, skip: int, limit: int):
    sql = """
      SELECT id, GROUP_CONCAT(account) accounts
        FROM hive_feed_cache
       WHERE account IN (SELECT following FROM hive_follows WHERE follower = :account)
    GROUP BY id
    ORDER BY MIN(created_at) DESC LIMIT :limit OFFSET :skip
    """
    res = query_all(sql, account = account, skip = skip, limit = limit)
    posts = get_posts([r[0] for r in res])

    # Merge reblogged_by data into result set
    accts = dict(res)
    for post in posts:
        rby = set(accts[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts


# returns a blog feed (posts and reblogs from the specified account)
def get_blog_feed(account: str, skip: int, limit: int):
    #sql = """
    #    SELECT id, created_at
    #      FROM hive_posts
    #     WHERE depth = 0 AND is_deleted = 0 AND author = :account
    # UNION ALL
    #    SELECT post_id, created_at
    #      FROM hive_reblogs
    #     WHERE account = :account AND (SELECT is_deleted FROM hive_posts WHERE id = post_id) = 0
    #  ORDER BY created_at DESC
    #     LIMIT :limit OFFSET :skip
    #"""
    sql = "SELECT id FROM hive_feed_cache WHERE account = :account ORDER BY created_at DESC LIMIT :limit OFFSET :skip"
    post_ids = query_col(sql, account = account, skip = skip, limit = limit)
    return get_posts(post_ids)


