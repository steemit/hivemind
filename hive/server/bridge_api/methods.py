"""Bridge API public endpoints for posts"""

import hive.server.bridge_api.cursor as cursor
from hive.server.bridge_api.objects import load_posts, load_posts_reblogs
from hive.server.common.helpers import (
    return_error_info,
    valid_account,
    valid_permlink,
    valid_tag,
    valid_limit)

#pylint: disable=too-many-arguments, no-else-return

@return_error_info
async def get_ranked_posts(context, sort, start_author='', start_permlink='', limit=20, tag=None):
    """Query posts, sorted by given method."""
    assert sort in ['trending', 'hot', 'created', 'promoted',
                    'payout', 'payout_comments'], 'invalid sort'
    ids = await cursor.pids_by_query(
        context['db'],
        sort,
        valid_account(start_author, allow_empty=True),
        valid_permlink(start_permlink, allow_empty=True),
        valid_limit(limit, 100),
        valid_tag(tag, allow_empty=True))
    return await load_posts(context['db'], ids)

@return_error_info
async def get_account_posts(context, sort, account, start_author='', start_permlink='', limit=20):
    """Get posts for an account -- blog, feed, comments, or replies."""
    assert sort in ['blog', 'feed', 'comments', 'replies'], 'invalid sort'
    assert account, 'account is required'

    db = context['db']
    account = valid_account(account)
    start_author = valid_account(start_author, allow_empty=True)
    start_permlink = valid_permlink(start_permlink, allow_empty=True)
    start = (start_author, start_permlink)
    limit = valid_limit(limit, 100)

    if sort == 'blog':
        ids = await cursor.pids_by_blog(db, account, *start, limit)
        return await load_posts(context['db'], ids)
    elif sort == 'feed':
        res = await cursor.pids_by_feed_with_reblog(db, account, *start, limit)
        return await load_posts_reblogs(context['db'], res)
    elif sort == 'comments':
        start = start if start_permlink else (account, None)
        assert account == start[0], 'comments - account must match start author'
        ids = await cursor.pids_by_account_comments(db, *start, limit)
        return await load_posts(context['db'], ids)
    elif sort == 'replies':
        start = start if start_permlink else (account, None)
        ids = await cursor.pids_by_replies_to_account(db, *start, limit)
        return await load_posts(context['db'], ids)
