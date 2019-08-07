"""Steemd/condenser_api compatibility layer API methods."""

# pylint: disable=duplicate-code,too-many-arguments,invalid-name

import hive.server.condenser_api.cursor as cursor
from hive.server.bridge_api.objects import load_posts, load_posts_reblogs
from hive.server.condenser_api.common import (
    return_error_info,
    valid_account,
    valid_permlink,
    valid_tag,
    valid_limit)

@return_error_info
async def get_discussions_by_trending(context, start_author='', start_permlink='',
                                      limit=20, tag=None):
    """Query posts, sorted by trending score."""
    ids = await cursor.pids_by_query(
        context['db'],
        'trending',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return await load_posts(context['db'], ids)

@return_error_info
async def get_discussions_by_hot(context, start_author='', start_permlink='',
                                 limit=20, tag=None):
    """Query posts, sorted by hot score."""
    ids = await cursor.pids_by_query(
        context['db'],
        'hot',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return await load_posts(context['db'], ids)

@return_error_info
async def get_discussions_by_promoted(context, start_author='', start_permlink='',
                                      limit=20, tag=None):
    """Query posts, sorted by promoted amount."""
    ids = await cursor.pids_by_query(
        context['db'],
        'promoted',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return await load_posts(context['db'], ids)

@return_error_info
async def get_discussions_by_created(context, start_author='', start_permlink='',
                                     limit=20, tag=None):
    """Query posts, sorted by creation date."""
    ids = await cursor.pids_by_query(
        context['db'],
        'created',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return await load_posts(context['db'], ids)

@return_error_info
async def get_discussions_by_blog(context, tag=None, start_author='',
                                  start_permlink='', limit=20):
    """Retrieve account's blog posts, including reblogs."""
    assert tag, '`tag` cannot be blank'
    ids = await cursor.pids_by_blog(
        context['db'],
        valid_account(tag),
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))
    return await load_posts(context['db'], ids)

@return_error_info
async def get_discussions_by_feed(context, tag=None, start_author='',
                                  start_permlink='', limit=20):
    """Retrieve account's personalized feed."""
    assert tag, '`tag` cannot be blank'
    res = await cursor.pids_by_feed_with_reblog(
        context['db'],
        valid_account(tag),
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))
    return await load_posts_reblogs(context['db'], res)

@return_error_info
async def get_discussions_by_comments(context, start_author=None, start_permlink='',
                                      limit=20):
    """Get comments by made by author."""
    assert start_author, '`start_author` cannot be blank'
    ids = await cursor.pids_by_account_comments(
        context['db'],
        valid_account(start_author),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))
    return await load_posts(context['db'], ids)

@return_error_info
async def get_replies_by_last_update(context, start_author=None, start_permlink='',
                                     limit=20):
    """Get all replies made to any of author's posts."""
    assert start_author, '`start_author` cannot be blank'
    ids = await cursor.pids_by_replies_to_account(
        context['db'],
        valid_account(start_author),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100))
    return await load_posts(context['db'], ids)

@return_error_info
async def get_post_discussions_by_payout(context, start_author='', start_permlink='',
                                         limit=20, tag=None):
    """Query top-level posts, sorted by payout."""
    ids = await cursor.pids_by_query(
        context['db'],
        'payout',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return await load_posts(context['db'], ids)

@return_error_info
async def get_comment_discussions_by_payout(context, start_author='', start_permlink='',
                                            limit=20, tag=None):
    """Query comments, sorted by payout."""
    ids = await cursor.pids_by_query(
        context['db'],
        'payout_comments',
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return await load_posts(context['db'], ids)
