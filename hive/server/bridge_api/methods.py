"""Bridge API public endpoints for posts"""

import hive.server.bridge_api.cursor as cursor
from hive.server.bridge_api.objects import load_posts, load_posts_reblogs, load_profiles
from hive.server.common.helpers import (
    return_error_info,
    valid_account,
    valid_permlink,
    valid_tag,
    valid_limit)
from hive.server.hive_api.common import get_account_id
from hive.server.hive_api.objects import _follow_contexts
from hive.server.hive_api.community import list_top_communities

#pylint: disable=too-many-arguments, no-else-return

async def _get_post_id(db, author, permlink):
    """Get post_id from hive db."""
    sql = """SELECT id FROM hive_posts
              WHERE author = :a
                AND permlink = :p
                AND is_deleted = '0'"""
    post_id = await db.query_one(sql, a=author, p=permlink)
    assert post_id, 'invalid author/permlink'
    return post_id

@return_error_info
async def get_profile(context, account, observer=None):
    """Load account/profile data."""
    db = context['db']
    ret = await load_profiles(db, [valid_account(account)])
    if not ret:
        return None

    observer_id = await get_account_id(db, observer) if observer else None
    if observer_id:
        await _follow_contexts(db, {ret[0]['id']: ret[0]}, observer_id, True)
    return ret[0]

@return_error_info
async def get_trending_topics(context, limit=10, observer=None):
    """Return top trending topics across pending posts."""
    # pylint: disable=unused-argument
    #db = context['db']
    #observer_id = await get_account_id(db, observer) if observer else None
    assert not observer, 'observer not supported'
    limit = valid_limit(limit, 25)
    out = []
    cells = await list_top_communities(context, limit)
    for name, title in cells:
        out.append((name, title or name))
    for tag in ('photography', 'travel', 'gaming',
                'crypto', 'newsteem', 'music', 'food'):
        if len(out) < limit:
            out.append((tag, '#' + tag))
    return out

@return_error_info
async def get_post(context, author, permlink, observer=None):
    """Fetch a single post"""
    # pylint: disable=unused-variable
    #TODO: `observer` logic
    db = context['db']
    observer_id = await get_account_id(db, observer) if observer else None
    pid = await _get_post_id(db,
                             valid_account(author),
                             valid_permlink(permlink))
    posts = await load_posts(db, [pid])
    assert len(posts) == 1, 'cache post not found'
    return posts[0]


@return_error_info
async def get_ranked_posts(context, sort, start_author='', start_permlink='',
                           limit=20, tag=None, observer=None):
    """Query posts, sorted by given method."""

    db = context['db']
    observer_id = await get_account_id(db, observer) if observer else None

    assert sort in ['trending', 'hot', 'created', 'promoted',
                    'payout', 'payout_comments', 'muted'], 'invalid sort'
    ids = await cursor.pids_by_ranked(
        context['db'],
        sort,
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True),
        observer_id)

    return await load_posts(context['db'], ids)

@return_error_info
async def get_account_posts(context, sort, account, start_author='', start_permlink='',
                            limit=20, observer=None):
    """Get posts for an account -- blog, feed, comments, or replies."""
    assert sort in ['blog', 'feed', 'comments', 'replies', 'payout'], 'invalid sort'
    assert account, 'account is required'

    db = context['db']
    account = valid_account(account)
    start_author = valid_account(start_author, allow_empty=True)
    start_permlink = valid_permlink(start_permlink, allow_empty=True)
    start = (start_author, start_permlink)
    limit = valid_limit(limit, 100)

    # pylint: disable=unused-variable
    observer_id = await get_account_id(db, observer) if observer else None # TODO

    if sort == 'blog':
        ids = await cursor.pids_by_blog(db, account, *start, limit)
        return await load_posts(context['db'], ids)
    elif sort == 'feed':
        res = await cursor.pids_by_feed_with_reblog(db, account, *start, limit)
        return await load_posts_reblogs(context['db'], res)
    elif sort == 'comments':
        start = start if start_permlink else (account, None)
        assert account == start[0], 'comments - account must match start author'
        ids = await cursor.pids_by_comments(db, *start, limit)
        return await load_posts(context['db'], ids)
    elif sort == 'replies':
        start = start if start_permlink else (account, None)
        ids = await cursor.pids_by_replies(db, *start, limit)
        return await load_posts(context['db'], ids)
    elif sort == 'payout':
        start = start if start_permlink else (account, None)
        ids = await cursor.pids_by_payout(db, account, *start, limit)
        return await load_posts(context['db'], ids)
