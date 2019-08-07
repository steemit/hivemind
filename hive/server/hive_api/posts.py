"""Hive API: Community methods"""
import logging

from hive.server.hive_api.common import (
    get_account_id, valid_sort, url_to_id, valid_limit)
from hive.server.hive_api.objects import posts_by_id

log = logging.getLogger(__name__)

# pylint: disable=too-many-arguments

async def list_community_posts(context, community, sort='trending',
                               start='', limit=10, observer=None):
    """Paginated list of posts in a community. Includes pinned posts at the beginning.

    Observer: includes vote/reblog status on each post.

    Community:
      - `all`: renders site default
      - `my`: render's observer's subs
      - (blank): show global trending
      - (string): show community trending
    """
    db = context['db']

    pinned_ids = []

    if not community:
        # global trending: prefix home posts
        communities = []
        #if not start: pinned_ids = _pinned(db, DEFAULT_COMMUNITY)
    elif community[0] == '#':
        # feed for specific tag
        communities = [community[1:]]
    elif community[0] == '@':
        # user's subscribed communities feed
        communities = await _subscribed(db, community[1:])
        #if not start: pinned_ids = _pinned(db, DEFAULT_COMMUNITY)
    else:
        # specific community feed
        communities = [community]
        if not start: pinned_ids = _pinned(db, community)

    post_ids = ranked_pids(db,
                           sort=valid_sort(sort),
                           start_id=await url_to_id(db, start) if start else None,
                           limit=valid_limit(limit, 50),
                           communities=communities)

    # TODO: fetch account role/title, include in response
    # NOTE: consider including & interspercing promoted posts here

    posts = posts_by_id(db, pinned_ids + post_ids, observer=observer)

    # Add `pinned` flag to all pinned
    for pinned_id in pinned_ids:
        posts[pinned_id]['is_pinned'] = True

    return posts

async def _subscribed(db, account):
    sql = """SELECT c.name FROM hive_communities c
               JOIN hive_subscriptions s
                 ON c.id = s.community_id
              WHERE s.account_id = :account_id"""
    return await db.query_col(sql, account_id=get_account_id(db, account))

async def _pinned(db, community):
    """Get a list of pinned post `id`s in `community`."""
    sql = """SELECT id FROM hive_posts
              WHERE is_pinned = '1'
                AND is_deleted = '0'
                AND community = :community
            ORDER BY id DESC"""
    return db.query_col(sql, community=community)


async def ranked_pids(db, sort, start_id, limit, communities):
    """Get a list of post_ids for a given posts query.

    `sort` can be trending, hot, created, promoted, or payout.
    """

    assert sort in ['trending', 'hot', 'created', 'promoted', 'payout']

    table = 'hive_posts_cache'
    field = ''
    where = []

    if not sort == 'created':
        where.append("is_paidout = '0'")

    if sort == 'trending':
        field = 'sc_trend'
    elif sort == 'hot':
        field = 'sc_hot'
    elif sort == 'created':
        field = 'post_id'
        where.append('depth = 0')
    elif sort == 'promoted':
        field = 'promoted'
        where.append('promoted > 0')
    elif sort == 'payout':
        field = 'payout'
    elif sort == 'muted':
        field = 'payout'

    # TODO: index hive_posts (is_muted, category, id)
    # TODO: copy is_muted and category from hive_posts to hive_posts_cache?
    _filt = "is_muted = '%d'" % (1 if sort == 'muted' else 0)
    if communities: _filt += " AND category IN :communities"
    where.append("post_id IN (SELECT id FROM hive_posts WHERE %s)" % _filt)

    if start_id:
        sql = "%s <= (SELECT %s FROM %s WHERE post_id = :start_id)"
        where.append(sql % (field, field, table))

    sql = ("SELECT post_id FROM %s WHERE %s ORDER BY %s DESC LIMIT :limit"
           % (table, ' AND '.join(where), field))

    return await db.query_col(sql, communities=tuple(communities),
                              start_id=start_id, limit=limit)
