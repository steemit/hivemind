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
def get_followers(account: str, start = None):
    res = query("SELECT follower, created_at FROM hive_follows WHERE following = :account AND "
            "created_at < IFNULL(:start, NOW()) ORDER BY created_at DESC LIMIT 10", account = account, start = start)
    return [[r[0],r[1]] for r in res.fetchall()]


def get_following(account: str, start: None):
    res = query("SELECT following, created_at FROM hive_follows WHERE follower = :account AND "
            "created_at < IFNULL(:start, NOW()) ORDER BY created_at DESC LIMIT 10", account = account, start = start)
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


# following/follower counts
# SELECT SUM(IF(follower = 'roadscape', 1, 0)) following, SUM(IF(following = 'roadscape', 1, 0)) followers FROM hivepy.hive_follows WHERE is_muted = 0;


# notifications -- who reblogged you
# SELECT * FROM hive.hive_reblogs r
# JOIN hive_posts p ON r.post_id = p.id
# WHERE p.author = 'roadscape'
# ORDER BY r.created_at DESC;
