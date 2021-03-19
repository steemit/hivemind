"""Hive API: Public endpoints"""

import logging

from hive.server.hive_api.objects import accounts_by_name, posts_by_id
from hive.server.hive_api.common import (
    get_account_id, split_url,
    valid_account, valid_permlink, valid_limit)
from hive.server.condenser_api.cursor import get_followers, get_following
from hive.server.bridge_api.cursor import (
    pids_by_blog, pids_by_comments, pids_by_feed_with_reblog)


log = logging.getLogger(__name__)

# Accounts

async def get_account(context, name, observer):
    """Get a full account object by `name`.

    Observer: will include `followed`/`muted` context.
    """
    assert name, 'name cannot be blank'
    return await accounts_by_name(context['db'], [valid_account(name)], observer, lite=False)

async def get_accounts(context, names, observer=None):
    """Find and return lite accounts by `names`.

    Observer: will include `followed` context.
    """
    assert isinstance(names, list), 'names must be a list'
    assert names, 'names cannot be blank'
    assert len(names) < 100, 'too many accounts requested'
    return await accounts_by_name(context['db'], names, observer, lite=True)


# Follows/mute

async def list_followers(context, account, start='', limit=50, observer=None):
    """Get a list of all accounts following `account`."""
    followers = await get_followers(
        context['db'],
        valid_account(account),
        valid_account(start, allow_empty=True),
        'blog', valid_limit(limit, 100))
    return await accounts_by_name(context['db'], followers, observer, lite=True)

async def list_following(context, account, start='', limit=50, observer=None):
    """Get a list of all accounts `account` follows."""
    following = await get_following(
        context['db'],
        valid_account(account),
        valid_account(start, allow_empty=True),
        'blog', valid_limit(limit, 100))
    return await accounts_by_name(context['db'], following, observer, lite=True)

async def list_all_muted(context, account):
    """Get a list of all account names muted by `account`."""
    db = context['db']
    sql = """SELECT a.name FROM hive_follows f
               JOIN hive_accounts a ON f.following_id = a.id
              WHERE follower = :follower AND state IN (2,3)"""
    return await db.query_col(sql, follower=get_account_id(db, account))


# Account post lists

async def list_account_blog(context, account, limit=10, observer=None, last_post=None):
    """Get a blog feed (posts and reblogs from the specified account)"""
    db = context['db']

    post_ids = await pids_by_blog(
        db,
        valid_account(account),
        *split_url(last_post, allow_empty=True),
        valid_limit(limit, 50))
    return await posts_by_id(db, post_ids, observer)

async def list_account_posts(context, account, limit=10, observer=None, last_post=None):
    """Get an account's posts and comments"""
    db = context['db']
    start_author, start_permlink = split_url(last_post, allow_empty=True)
    assert not start_author or (start_author == account)
    post_ids = await pids_by_comments(
        db,
        valid_account(account),
        valid_permlink(start_permlink),
        valid_limit(limit, 50))
    return await posts_by_id(db, post_ids, observer)

async def list_account_feed(context, account, limit=10, observer=None, last_post=None):
    """Get all posts (blogs and resteems) from `account`'s follows."""
    db = context['db']
    ids_with_reblogs = await pids_by_feed_with_reblog(
        context['db'],
        valid_account(account),
        *split_url(last_post, allow_empty=True),
        valid_limit(limit, 50))

    reblog_by = dict(ids_with_reblogs)
    post_ids = [r[0] for r in ids_with_reblogs]
    posts = await posts_by_id(db, post_ids, observer)

    # Merge reblogged_by data into result set
    for post in posts:
        rby = set(reblog_by[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby: post['reblogged_by'] = list(rby)

    return posts
