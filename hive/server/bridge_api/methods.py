"""Bridge API public endpoints for posts"""

import hashlib
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
from hive.server.db import CACHE_NAMESPACE

#pylint: disable=too-many-arguments, no-else-return

async def _filter_hidden_posts(db, ids):
    """Filter out hidden posts from a list of post IDs. Returns filtered list."""
    if not ids:
        return ids
    hide_pids = await cursor.hide_pids_by_ids(db, ids)
    if hide_pids:
        # Use set for O(1) lookup instead of O(n) list operations
        hide_set = set(hide_pids)
        return [pid for pid in ids if pid not in hide_set]
    return ids

async def _get_post_id(db, author, permlink):
    """Get post_id from hive db."""
    # Generate cache key for post_id lookup
    # Post IDs don't change once created, so we can cache for a long time
    cache_key = f'post_id_{author}_{permlink}'
    
    sql = """SELECT id FROM hive_posts
              WHERE author = :a
                AND permlink = :p
                AND is_deleted = '0'"""
    post_id = await db.query_one(sql, a=author, p=permlink, 
                                 cache_key=cache_key, cache_ttl=86400)
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
    #assert not observer, 'observer not supported'
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
    #TODO: `observer` logic for user-post state
    db = context['db']
    observer_id = await get_account_id(db, observer) if observer else None
    
    # Generate cache key for the full post result
    # Include observer_id in cache key when observer logic is implemented
    author_valid = valid_account(author)
    permlink_valid = valid_permlink(permlink)
    cache_key_parts = ['get_post', author_valid, permlink_valid]
    if observer_id:
        cache_key_parts.append(str(observer_id))
    cache_key_str = '_'.join(cache_key_parts)
    cache_key = 'bridge_get_post_' + hashlib.md5(cache_key_str.encode()).hexdigest()
    
    # Try to get result from cache (cache for 180 seconds)
    if db.redis_cache is not None:
        cached_result = await db.redis_cache.get(cache_key, namespace=CACHE_NAMESPACE)
        if cached_result is not None:
            return cached_result
    
    pid = await _get_post_id(db, author_valid, permlink_valid)
    posts = await load_posts(db, [pid])
    assert len(posts) == 1, 'cache post not found'
    result = posts[0]
    
    # Store result in cache
    if db.redis_cache is not None:
        await db.redis_cache.set(cache_key, result, ttl=180, namespace=CACHE_NAMESPACE)
    
    return result


@return_error_info
async def get_ranked_posts(context, sort, start_author='', start_permlink='',
                           limit=20, tag=None, observer=None):
    """Query posts, sorted by given method."""

    db = context['db']
    observer_id = await get_account_id(db, observer) if observer else None

    assert sort in ['trending', 'hot', 'created', 'promoted',
                    'payout', 'payout_comments', 'muted'], 'invalid sort'
    
    # Validate and normalize parameters
    start_author = valid_account(start_author, allow_empty=True)
    start_permlink = valid_permlink(start_permlink, allow_empty=True)
    limit = valid_limit(limit, 100)
    tag = valid_tag(tag, allow_empty=True)
    
    # Generate cache key (based on all query parameters)
    # Note: when tag='my', observer_id affects the result (subscribed communities),
    # so we must include it in the cache key
    cache_key_parts = [
        'get_ranked_posts',
        sort,
        start_author or '',
        start_permlink or '',
        str(limit),
        tag or '',
    ]
    # Include observer_id in cache key when tag='my' (personalized content)
    if tag == 'my' and observer_id:
        cache_key_parts.append(str(observer_id))
    cache_key_str = '_'.join(cache_key_parts)
    # Use hash to shorten overly long cache keys
    cache_key = 'bridge_get_ranked_posts_' + hashlib.md5(cache_key_str.encode()).hexdigest()
    
    # Set different cache TTL based on sort type (in seconds)
    cache_ttl_map = {
        'created': 3,           # 3 seconds cache
        'trending': 300,        # 300 seconds cache
        'hot': 300,             # 300 seconds cache
        'promoted': 300,        # 300 seconds cache
        'payout': 30,           # 30 seconds cache
        'payout_comments': 30,  # 30 seconds cache
        'muted': 600,           # 600 seconds cache
    }
    cache_ttl = cache_ttl_map.get(sort, 60)  # Default 60 seconds
    
    # Try to get result from cache
    if db.redis_cache is not None:
        cached_result = await db.redis_cache.get(cache_key, namespace=CACHE_NAMESPACE)
        if cached_result is not None:
            return cached_result
    
    # Cache miss, execute query
    ids = await cursor.pids_by_ranked(
        context['db'],
        sort,
        start_author,
        start_permlink,
        limit,
        tag,
        observer_id)

    result = await load_posts(context['db'], ids)
    
    # Store result in cache
    if db.redis_cache is not None:
        await db.redis_cache.set(cache_key, result, ttl=cache_ttl, namespace=CACHE_NAMESPACE)
    
    return result

@return_error_info
async def get_account_posts(context, sort, account, start_author='', start_permlink='',
                            limit=20, observer=None):
    """Get posts for an account -- blog, feed, comments, or replies."""
    valid_sorts = ['blog', 'feed', 'posts', 'comments', 'replies', 'payout']
    assert sort in valid_sorts, 'invalid account sort'
    assert account, 'account is required'

    db = context['db']
    account = valid_account(account)
    start_author = valid_account(start_author, allow_empty=True)
    start_permlink = valid_permlink(start_permlink, allow_empty=True)
    start = (start_author, start_permlink)
    limit = valid_limit(limit, 100)

    _id = await db.query_one("SELECT id FROM hive_posts_status WHERE author = :n", n=account)
    if _id:
        return []

    # pylint: disable=unused-variable
    observer_id = await get_account_id(db, observer) if observer else None # TODO

    # Generate cache key for account posts
    # Note: observer_id is not used yet, but included for future compatibility
    cache_key_parts = [
        'get_account_posts',
        sort,
        account,
        start_author or '',
        start_permlink or '',
        str(limit),
    ]
    if observer_id:
        cache_key_parts.append(str(observer_id))
    cache_key_str = '_'.join(cache_key_parts)
    cache_key = 'bridge_get_account_posts_' + hashlib.md5(cache_key_str.encode()).hexdigest()
    
    # Try to get result from cache (cache for 30 seconds)
    if db.redis_cache is not None:
        cached_result = await db.redis_cache.get(cache_key, namespace=CACHE_NAMESPACE)
        if cached_result is not None:
            return cached_result

    # Normalize start parameter for sorts that require it
    needs_start_normalization = sort in ['posts', 'comments', 'replies', 'payout']
    if needs_start_normalization:
        start = start if start_permlink else (account, None)
        if sort in ['posts', 'comments']:
            assert account == start[0], 'comments - account must match start author'

    if sort == 'blog':
        ids = await cursor.pids_by_blog(db, account, *start, limit)
        ids = await _filter_hidden_posts(db, ids)
        posts = await load_posts(context['db'], ids)
        for post in posts:
            if post['author'] != account:
                post['reblogged_by'] = [account]
        result = posts
    elif sort == 'feed':
        res = await cursor.pids_by_feed_with_reblog(db, account, *start, limit)
        result = await load_posts_reblogs(context['db'], res)
    elif sort == 'posts':
        ids = await cursor.pids_by_posts(db, *start, limit)
        ids = await _filter_hidden_posts(db, ids)
        result = await load_posts(context['db'], ids)
    elif sort == 'comments':
        ids = await cursor.pids_by_comments(db, *start, limit)
        result = await load_posts(context['db'], ids)
    elif sort == 'replies':
        ids = await cursor.pids_by_replies(db, *start, limit)
        result = await load_posts(context['db'], ids)
    elif sort == 'payout':
        ids = await cursor.pids_by_payout(db, account, *start, limit)
        ids = await _filter_hidden_posts(db, ids)
        result = await load_posts(context['db'], ids)
    
    # Store result in cache
    if db.redis_cache is not None:
        await db.redis_cache.set(cache_key, result, ttl=30, namespace=CACHE_NAMESPACE)
    
    return result
