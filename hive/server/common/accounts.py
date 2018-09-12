"""Hive API: supporting methods for accounts"""

from aiocache import cached
from hive.db.adapter import Db

DB = Db.instance()

@cached(ttl=300)
async def get_accounts_impl(names):
    """Retrieve basic account metadata for named accounts. Order preserved."""
    sql = """SELECT name, vote_weight, created_at, reputation
               FROM hive_accounts WHERE name IN :names"""

    out = {}
    for row in DB.query_all(sql, names=tuple(names)):
        out[row['name']] = dict(
            name=row['name'],
            vote_sp=int(row['vote_weight'] * 0.000494513),
            joined_at=str(row['created_at']),
            reputation=row['reputation'])

    return [out[n] for n in names if n in out]

@cached(ttl=60)
async def get_accounts_ac_impl(query, ctx):
    """Search for accounts by context."""

    ctx_id = _get_account_id(ctx) if ctx else None

    out = {
        'friend': [],
        'global': []}
    ignore = set()

    if len(query) > 1 and ctx_id:
        sql = """SELECT name FROM hive_accounts ha
                   JOIN hive_follows hf ON ha.id = hf.following
                  WHERE hf.follower = :ctx_id
                    AND hf.state = 1
                    AND name LIKE :query
               ORDER BY name LIMIT 10"""
        names = DB.query_col(sql, query=query+'%', ctx_id=ctx_id)
        if names:
            for account in await get_accounts_impl(names):
                account['context'] = dict(is_following=True)
                out['friend'].append(account)
            ignore = set(names)

    if len(query) >= 3:
        skip = "AND name NOT IN :ignore" if ignore else ""
        sql = """SELECT name FROM hive_accounts
                  WHERE name LIKE :query %s
               ORDER BY vote_weight DESC LIMIT 10""" % skip
        names = DB.query_col(sql, query=query+'%', ignore=tuple(ignore))
        if names:
            out['global'] = await get_accounts_impl(names)

    return out

def _get_account_id(name):
    return DB.query_one("SELECT id FROM hive_accounts WHERE name = :n", n=name)
