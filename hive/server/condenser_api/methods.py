"""Steemd/condenser_api compatibility layer API methods."""

from functools import wraps

import hive.server.condenser_api.cursor as cursor
from hive.server.condenser_api.objects import load_posts
from hive.server.condenser_api.common import (
    valid_account,
    valid_permlink,
    valid_tag,
    valid_limit,
    get_post_id,
    get_child_ids)


# Follows Queries

def _follow_type_to_int(follow_type: str):
    """Convert steemd-style "follow type" into internal status (int)."""
    assert follow_type in ['blog', 'ignore'], "invalid follow_type"
    return 1 if follow_type == 'blog' else 2

def _legacy_follower(follower, following, follow_type):
    return dict(follower=follower, following=following, what=[follow_type])

async def get_followers(account: str, start: str, follow_type: str, limit: int):
    """Get all accounts following `account`. (EOL)"""
    followers = cursor.get_followers(
        valid_account(account),
        valid_account(start or '', allow_empty=True),
        _follow_type_to_int(follow_type),
        valid_limit(limit, 1000))
    return [_legacy_follower(name, account, follow_type) for name in followers]

async def get_following(account: str, start: str, follow_type: str, limit: int):
    """Get all accounts `account` follows. (EOL)"""
    following = cursor.get_following(
        valid_account(account),
        valid_account(start or '', allow_empty=True),
        _follow_type_to_int(follow_type),
        valid_limit(limit, 1000))
    return [_legacy_follower(account, name, follow_type) for name in following]

async def get_follow_count(account: str):
    """Get follow count stats. (EOL)"""
    count = cursor.get_follow_counts(valid_account(account))
    return dict(account=account,
                following_count=count['following'],
                follower_count=count['followers'])


# Content Primitives

async def get_content(author: str, permlink: str):
    """Get a single post object."""
    valid_account(author)
    valid_permlink(permlink)
    post_id = get_post_id(author, permlink)
    if not post_id:
        return {'id': 0, 'author': '', 'permlink': ''}
    return load_posts([post_id])[0]


async def get_content_replies(parent: str, parent_permlink: str):
    """Get a list of post objects based on parent."""
    valid_account(parent)
    valid_permlink(parent_permlink)
    parent_id = get_post_id(parent, parent_permlink)
    if parent_id:
        child_ids = get_child_ids(parent_id)
        if child_ids:
            return load_posts(child_ids)
    return []


# Discussion Queries

def nested_query_compat(function):
    """Unpack strange format used by some clients, accepted by steemd.

    Sometimes a discussion query object is nested inside a list[1]. Eg:

        {... "method":"condenser_api.get_discussions_by_hot",
             "params":[{"tag":"steem","limit":1}]}

    In these cases jsonrpcserver dispatch just shoves it into the first
    arg. This decorator checks for this specific condition and unpacks
    the query to be passed as kwargs.
    """
    @wraps(function)
    def wrapper(*args, **kwargs):
        """Checks for specific condition signature and unpacks query"""
        if args and not kwargs and len(args) < 2 and isinstance(args[0], dict):
            return function(**args[0])
        return function(*args, **kwargs)
    return wrapper


@nested_query_compat
async def get_discussions_by_trending(start_author: str = '', start_permlink: str = '',
                                      limit: int = 20, tag: str = None,
                                      truncate_body: int = 0):
    """Query posts, sorted by trending score."""
    ids = cursor.pids_by_query(
        'trending',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 20),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_hot(start_author: str = '', start_permlink: str = '',
                                 limit: int = 20, tag: str = None,
                                 truncate_body: int = 0):
    """Query posts, sorted by hot score."""
    ids = cursor.pids_by_query(
        'hot',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 20),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_promoted(start_author: str = '', start_permlink: str = '',
                                      limit: int = 20, tag: str = None,
                                      truncate_body: int = 0):
    """Query posts, sorted by promoted amount."""
    ids = cursor.pids_by_query(
        'promoted',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 20),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_created(start_author: str = '', start_permlink: str = '',
                                     limit: int = 20, tag: str = None,
                                     truncate_body: int = 0):
    """Query posts, sorted by creation date."""
    ids = cursor.pids_by_query(
        'created',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 20),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_blog(tag: str, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20,
                                  truncate_body: int = 0):
    """Retrieve account's blog posts."""
    ids = cursor.pids_by_blog(
        valid_account(tag),
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 20))
    return load_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_feed(tag: str, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20,
                                  truncate_body: int = 0):
    """Retrieve account's personalized feed."""
    res = cursor.pids_by_feed_with_reblog(
        valid_account(tag),
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 20))

    reblogged_by = dict(res)
    posts = load_posts([r[0] for r in res], truncate_body=truncate_body)

    # Merge reblogged_by data into result set
    for post in posts:
        rby = set(reblogged_by[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts


@nested_query_compat
async def get_discussions_by_comments(start_author: str, start_permlink: str = '',
                                      limit: int = 20, truncate_body: int = 0):
    """Get comments by made by author."""
    ids = cursor.pids_by_account_comments(
        valid_account(start_author),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 20))
    return load_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_replies_by_last_update(start_author: str, start_permlink: str = '',
                                     limit: int = 20, truncate_body: int = 0):
    """Get all replies made to any of author's posts."""
    ids = cursor.pids_by_replies_to_account(
        valid_account(start_author),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 50))
    return load_posts(ids, truncate_body=truncate_body)
