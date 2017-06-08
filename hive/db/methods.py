from funcy.seqs import first
from hive.db import conn
from hive.db.schema import (
    hive_follows,
)
from sqlalchemy import text, select, func


# generic
# -------
def query(sql, **kwargs):
    res = conn.execute(text(sql).execution_options(autocommit=False), **kwargs)
    return res


def query_one(sql):
    res = conn.execute(text(sql))
    row = first(res)
    if row:
        return first(row)


def db_last_block():
    return query_one("SELECT MAX(num) FROM hive_blocks") or 0


# api specific
# ------------
def get_followers(account: str, skip: int, limit: int):
    # q = select([hive_follows]). \
    #     where(hive_follows.c.following == account). \
    #     skip(skip).limit(limit)
    # return conn.execute(q)
    res = query("SELECT follower, created_at FROM hive_follows WHERE following = :account "
            "ORDER BY created_at DESC LIMIT :limit OFFSET :skip", account = account, skip = int(skip), limit = int(limit))
    return [[r[0],r[1]] for r in res.fetchall()]


def get_following(account: str, skip: int, limit: int):
    # q = select([hive_follows]). \
    #     where(hive_follows.c.follower == account). \
    #     skip(skip).limit(limit)
    # return conn.execute(q)
    res = query("SELECT following, created_at FROM hive_follows WHERE follower = :account "
            "ORDER BY created_at DESC LIMIT :limit OFFSET :skip", account = account, skip = int(skip), limit = int(limit))
    return [[r[0],r[1]] for r in res.fetchall()]


def following_count(account: str):
    q = select([func.count(hive_follows.c.hive_follows_fk1)]). \
        where(hive_follows.c.follower == account). \
        as_scalar()
    return conn.execute(q)


def follower_count(account: str):
    q = select([func.count(hive_follows.c.hive_follows_fk1)]). \
        where(hive_follows.c.following == account). \
        as_scalar()
    return conn.execute(q)


def follow_stats(account: str):
    sql = ("SELECT SUM(IF(follower = :account, 1, 0)) following, "
          "SUM(IF(following = :account, 1, 0)) followers "
          "FROM hive_follows WHERE is_muted = 0")
    return first(query(sql))


def get_discussions_by_trending(skip: int, limit: int):
    sql = ("SELECT CONCAT('@', p.author, '/', p.permlink) url, p.created_at, p.depth, c.* FROM hive.hive_posts_cache c "
          "JOIN hive_posts p ON c.post_id = p.id ORDER BY sc_trend DESC")
    return query(sql)
    sql = "SELECT post_id FROM hive_posts_cache ORDER BY sc_trend DESC LIMIT :limit OFFSET :skip"
    return query(sql, limit = limit, skip = skip)


def get_posts(ids):

    sql = """
    SELECT CONCAT('@', p.author, '/', p.permlink) url, p.created_at, p.depth, c.*
    FROM hive.hive_posts_cache c
    JOIN hive_posts p ON c.post_id = p.id
    WHERE p.id IN (%s)
    ORDER BY sc_trend DESC
    """ % ','.join(ids)
    return query(sql)


def get_discussions_by_created(skip: int, limit: int):
    sql = ("SELECT CONCAT('@', p.author, '/', p.permlink) url, p.created_at, p.depth, c.* FROM hive.hive_posts_cache c "
            "JOIN hive_posts p ON c.post_id = p.id ORDER BY post_id DESC")
    return query(sql)


def get_user_feed(account: str, skip: int, limit: int):
    sql = """
        SELECT id, GROUP_CONCAT(account) accounts -- , MIN(created_at) date
        FROM hive_feed_cache
        WHERE account IN (SELECT following FROM hive_follows WHERE follower = :account)
        GROUP BY id
        ORDER BY MIN(created_at) DESC
        LIMIT :limit OFFSET :skip
    """
    return query(sql, account = account, skip = skip, limit = limit).fetchall()


def get_blog_feed(account: str, skip: int, limit: int):
    sql = """
        SELECT p.author, p.permlink, p.created_at
          FROM hive_posts p 
         WHERE depth = 0 AND p.author = :account
     UNION ALL
        SELECT p.author, p.permlink, r.created_at
          FROM hive_posts p JOIN hive_reblogs r ON p.id = r.post_id
         WHERE depth = 0 AND r.account = :account
      ORDER BY created_at DESC
         LIMIT :limit OFFSET :offset
    """
    return query(sql, account = account, skip = skip, limit = limit)


def get_reblogs_since(account: str, since: str):
    sql = ("SELECT * FROM hive_reblogs r JOIN hive_posts p ON r.post_id = p.id "
          "WHERE p.author = :account AND r.created_at > :since ORDER BY r.created_at DESC")
    return [dict(r) for r in query(sql, account = account, since = since).fetchall()]
