"""Steemd/condenser_api compatibility layer API methods."""

from functools import wraps

import hive.server.condenser_api.cursor as cursor
from hive.server.condenser_api.objects import load_posts, load_posts_reblogs
from hive.server.condenser_api.common import (
    ApiError,
    return_error_info,
    valid_account,
    valid_permlink,
    valid_tag,
    valid_offset,
    valid_limit,
    valid_follow_type,
    get_post_id,
    get_child_ids)


# Dummy

@return_error_info
async def get_account_votes(account):
    """Return an info message about get_acccount_votes being unsupported."""
    # pylint: disable=unused-argument
    raise ApiError("get_account_votes is no longer supported, for details see "
                   "https://steemit.com/steemit/@steemitdev/additional-public-api-change")


# Follows Queries

def _legacy_follower(follower, following, follow_type):
    return dict(follower=follower, following=following, what=[follow_type])

@return_error_info
async def get_followers(account: str, start: str, follow_type: str = None,
                        limit: int = None, **kwargs):
    """Get all accounts following `account`. (EOL)"""
    # `type` reserved word workaround
    if not follow_type and 'type' in kwargs:
        follow_type = kwargs['type']
    if not follow_type:
        follow_type = 'blog'
    followers = cursor.get_followers(
        valid_account(account),
        valid_account(start, allow_empty=True),
        valid_follow_type(follow_type),
        valid_limit(limit, 1000))
    return [_legacy_follower(name, account, follow_type) for name in followers]

@return_error_info
async def get_following(account: str, start: str, follow_type: str = None,
                        limit: int = None, **kwargs):
    """Get all accounts `account` follows. (EOL)"""
    # `type` reserved word workaround
    if not follow_type and 'type' in kwargs:
        follow_type = kwargs['type']
    if not follow_type:
        follow_type = 'blog'
    following = cursor.get_following(
        valid_account(account),
        valid_account(start, allow_empty=True),
        valid_follow_type(follow_type),
        valid_limit(limit, 1000))
    return [_legacy_follower(account, name, follow_type) for name in following]

@return_error_info
async def get_follow_count(account: str):
    """Get follow count stats. (EOL)"""
    count = cursor.get_follow_counts(valid_account(account))
    return dict(account=account,
                following_count=count['following'],
                follower_count=count['followers'])

@return_error_info
async def get_reblogged_by(author: str, permlink: str):
    """Get all rebloggers of a post."""
    return cursor.get_reblogged_by(
        valid_account(author),
        valid_permlink(permlink))

@return_error_info
async def get_account_reputations(account_lower_bound: str = None, limit: int = None):
    """List account reputations"""
    return dict(reputations=cursor.get_account_reputations(
        account_lower_bound,
        valid_limit(limit, 1000)))


# Content Primitives

@return_error_info
async def get_content(author: str, permlink: str):
    """Get a single post object."""
    valid_account(author)
    valid_permlink(permlink)
    post_id = get_post_id(author, permlink)
    if not post_id:
        return {'id': 0, 'author': '', 'permlink': ''}
    posts = load_posts([post_id])
    assert posts, 'post was not found in cache'
    return posts[0]


@return_error_info
async def get_content_replies(author: str, permlink: str):
    """Get a list of post objects based on parent."""
    valid_account(author)
    valid_permlink(permlink)
    parent_id = get_post_id(author, permlink)
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


@return_error_info
@nested_query_compat
async def get_discussions_by_trending(start_author: str = '', start_permlink: str = '',
                                      limit: int = 20, tag: str = None,
                                      truncate_body: int = 0, filter_tags: list = None):
    """Query posts, sorted by trending score."""
    assert not filter_tags, 'filter_tags not supported'
    ids = cursor.pids_by_query(
        'trending',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_discussions_by_hot(start_author: str = '', start_permlink: str = '',
                                 limit: int = 20, tag: str = None,
                                 truncate_body: int = 0, filter_tags: list = None):
    """Query posts, sorted by hot score."""
    assert not filter_tags, 'filter_tags not supported'
    ids = cursor.pids_by_query(
        'hot',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_discussions_by_promoted(start_author: str = '', start_permlink: str = '',
                                      limit: int = 20, tag: str = None,
                                      truncate_body: int = 0, filter_tags: list = None):
    """Query posts, sorted by promoted amount."""
    assert not filter_tags, 'filter_tags not supported'
    ids = cursor.pids_by_query(
        'promoted',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_discussions_by_created(start_author: str = '', start_permlink: str = '',
                                     limit: int = 20, tag: str = None,
                                     truncate_body: int = 0, filter_tags: list = None):
    """Query posts, sorted by creation date."""
    assert not filter_tags, 'filter_tags not supported'
    ids = cursor.pids_by_query(
        'created',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_discussions_by_blog(tag: str = None, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20,
                                  truncate_body: int = 0, filter_tags: list = None):
    """Retrieve account's blog posts, including reblogs."""
    assert tag, '`tag` cannot be blank'
    assert not filter_tags, 'filter_tags not supported'
    ids = cursor.pids_by_blog(
        valid_account(tag),
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))
    return load_posts(ids, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_discussions_by_feed(tag: str = None, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20,
                                  truncate_body: int = 0, filter_tags: list = None):
    """Retrieve account's personalized feed."""
    assert tag, '`tag` cannot be blank'
    assert not filter_tags, 'filter_tags not supported'
    res = cursor.pids_by_feed_with_reblog(
        valid_account(tag),
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))
    return load_posts_reblogs(res, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_discussions_by_comments(start_author: str = None, start_permlink: str = '',
                                      limit: int = 20, truncate_body: int = 0,
                                      filter_tags: list = None):
    """Get comments by made by author."""
    assert start_author, '`start_author` cannot be blank'
    assert not filter_tags, 'filter_tags not supported'
    ids = cursor.pids_by_account_comments(
        valid_account(start_author),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))
    return load_posts(ids, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_replies_by_last_update(start_author: str = None, start_permlink: str = '',
                                     limit: int = 20, truncate_body: int = 0):
    """Get all replies made to any of author's posts."""
    assert start_author, '`start_author` cannot be blank'
    ids = cursor.pids_by_replies_to_account(
        valid_account(start_author),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))
    return load_posts(ids, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_discussions_by_author_before_date(author: str = None, start_permlink: str = '',
                                                before_date: str = '', limit: int = 10):
    """Retrieve account's blog posts, without reblogs.

    NOTE: before_date is completely ignored, and it appears to be broken and/or
    completely ignored in steemd as well. This call is similar to
    get_discussions_by_blog but does NOT serve reblogs.
    """
    # pylint: disable=invalid-name,unused-argument
    assert author, '`author` cannot be blank'
    ids = cursor.pids_by_blog_without_reblog(
        valid_account(author),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))
    return load_posts(ids)


@return_error_info
@nested_query_compat
async def get_post_discussions_by_payout(start_author: str = '', start_permlink: str = '',
                                         limit: int = 20, tag: str = None,
                                         truncate_body: int = 0):
    """Query top-level posts, sorted by payout."""
    ids = cursor.pids_by_query(
        'payout',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_comment_discussions_by_payout(start_author: str = '', start_permlink: str = '',
                                            limit: int = 20, tag: str = None,
                                            truncate_body: int = 0):
    """Query comments, sorted by payout."""
    ids = cursor.pids_by_query(
        'payout_comments',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return load_posts(ids, truncate_body=truncate_body)


@return_error_info
@nested_query_compat
async def get_blog(account: str, start_entry_id: int = 0, limit: int = None):
    """Get posts for an author's blog (w/ reblogs), paged by index/limit.

    Equivalent to get_discussions_by_blog, but uses offset-based pagination.
    """
    return _get_blog(account, start_entry_id, limit)

@return_error_info
@nested_query_compat
async def get_blog_entries(account: str, start_entry_id: int = 0, limit: int = None):
    """Get 'entries' for an author's blog (w/ reblogs), paged by index/limit.

    Interface identical to get_blog, but returns minimalistic post references.
    """

    entries = _get_blog(account, start_entry_id, limit)
    for entry in entries:
        # replace the comment body with just author/permlink
        post = entry.pop('comment')
        entry['author'] = post['author']
        entry['permlink'] = post['permlink']

    return entries

def _get_blog(account: str, start_index: int, limit: int = None):
    """Get posts for an author's blog (w/ reblogs), paged by index/limit.

    Examples:
    (acct, 2) = returns blog entries 0 up to 2 (3 oldest)
    (acct, 0) = returns all blog entries (limit 0 means return all?)
    (acct, 2, 1) = returns 1 post starting at idx 2
    (acct, 2, 3) = returns 3 posts: idxs (2,1,0)
    (acct, -1, 10) = returns latest 10 posts
    """

    if start_index is None:
        start_index = 0

    if not limit:
        limit = start_index + 1

    ids = cursor.pids_by_blog_by_index(
        valid_account(account),
        valid_offset(start_index),
        valid_limit(limit, 500))

    out = []

    idx = int(start_index)
    for post in load_posts(ids):
        reblog = post['author'] != account
        reblog_on = post['created'] if reblog else "1970-01-01T00"
        out.append({"blog": account,
                    "entry_id": idx,
                    "comment": post,
                    "reblog_on": reblog_on})
        idx -= 1

    return out
