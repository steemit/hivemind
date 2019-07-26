"""Hive API: Accounts methods"""
import logging
from hive.server.hive_api.common import get_account_id, estimated_sp
log = logging.getLogger(__name__)

async def get_account(context, name, observer):
    """Get a full account object by `name`.

    Observer: Includes `followed`/`muted` context."""
    db = context['db']
    sql = """SELECT id, name, display_name, about, vote_weight, created_at,
                    location, website, followers, following, profile_image,
                    cover_image, rank
               FROM hive_accounts WHERE name = :name"""
    row = await db.query_row(sql, name=name)

    account = {
        'id': row['id'],
        'name': row['name'],
        'display_name': row['display_name'],
        'about': row['about'],
        'vote_weight': estimated_sp(row['vote_weight']),
        'created': str(row['created_at']),
        'location': row['location'],
        'website': row['website'],
        'followers': row['followers'],
        'following': row['following'],
        'profile_image': row['profile_image'],
        'cover_image': row['cover_image'],
        'rank': row['rank'],
    }

    if observer:
        state = db.query_one("""SELECT state FROM hive_follows
                                 WHERE follower = :follower
                                   AND following = :following""",
                             follower=get_account_id(db, observer),
                             following=account['id'])
        account['context'] = {'followed': state == 1,
                              'muted': state == 2}

    return account

async def find_accounts(context, names, observer=None):
    """Find and return lite accounts by `names`."""
    db = context['db']

    sql = """SELECT id, name, display_name, about, vote_weight, created_at, followers, following
               FROM hive_accounts WHERE name IN :names"""
    rows = await db.query_all(sql, names=tuple(names))

    accounts = [{
        'id': row['id'],
        'name': row['name'],
        'display_name': row['display_name'],
        'about': row['about'],
        'vote_weight': estimated_sp(row['vote_weight']),
        'created': str(row['created_at']),
        'followers': row['followers'],
        'following': row['following'],
        } for row in rows]

    if observer:
        followed = db.query_col("""SELECT following FROM hive_follows
                                    WHERE follower = :account_id
                                      AND state = 1""",
                                account_id=get_account_id(db, observer))
        for account in accounts:
            if account['id'] in followed:
                account['context'] = {'followed': True}

    return accounts
