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


def get_reblogs_since(account: str, since: str):
    sql = ("SELECT * FROM hive_reblogs r JOIN hive_posts p ON r.post_id = p.id "
          "WHERE p.author = :account AND r.created_at > :since ORDER BY r.created_at DESC")
    return [dict(r) for r in query(sql, account = account, since = since).fetchall()]
