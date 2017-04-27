from funcy.seqs import first
from hive.db import conn
from sqlalchemy import text


def query(sql):
    res = conn.execute(text(sql).execution_options(autocommit=False))
    return res


def query_one(sql):
    res = conn.execute(text(sql))
    row = first(res)
    if row:
        return first(row)


def db_last_block():
    return query_one("SELECT MAX(num) FROM hive_blocks") or 0

