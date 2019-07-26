"""Hive API: Follows plugin methods"""
import logging

from hive.server.hive_api.common import (get_account_id, split_url,
                                         valid_account, valid_permlink, valid_limit)
from hive.server.hive_api.account import find_accounts
from hive.server.hive_api.post import posts_by_id
from hive.server.condenser_api.cursor import pids_by_feed_with_reblog, get_followers, get_following

log = logging.getLogger(__name__)

async def list_followers(context, account, start='', limit=50, observer=None):
    """Get a list of all accounts following `account`."""
    followers = await get_followers(
        context['db'],
        valid_account(account),
        valid_account(start, allow_empty=True),
        'blog',
        valid_limit(limit, 100))
    return find_accounts(context['db'], followers, observer)

async def list_following(context, account, start='', limit=50, observer=None):
    """Get a list of all accounts `account` follows."""
    following = await get_following(
        context['db'],
        valid_account(account),
        valid_account(start, allow_empty=True),
        'blog',
        valid_limit(limit, 100))
    return find_accounts(context['db'], following, observer)

async def list_all_muted(context, account):
    """Get a list of all account names muted by `account`."""
    db = context['db']
    sql = """SELECT a.name FROM hive_follows f
               JOIN hive_accounts a ON f.following_id = a.id
              WHERE follower = :follower AND state = 2"""
    names = db.query_col(sql, follower=get_account_id(db, account))
    return names

async def list_followed_posts(context, account, start='', limit=10, observer=None):
    """Get all posts (blogs and resteems) from `account`'s follows."""
    db = context['db']
    if start:
        start_author, start_permlink = split_url(start, allow_empty=True)
    ids_with_reblogs = await pids_by_feed_with_reblog(
        context['db'],
        valid_account(account),
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))

    post_ids = [r[0] for r in ids_with_reblogs]
    reblog_by = dict(ids_with_reblogs)
    posts = await posts_by_id(db, post_ids, observer)

    # Merge reblogged_by data into result set
    for post in posts:
        rby = set(reblog_by[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts
