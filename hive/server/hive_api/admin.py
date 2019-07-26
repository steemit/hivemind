"""Hive API: Administrative calls."""
import logging

from hive.server.hive_api.post import posts_by_id
from hive.server.hive_api.common import get_post_id, get_account_id
log = logging.getLogger(__name__)

async def list_mod_actions(context, community, start_id, limit):
    """Paginated list of moderator actions."""
    db = context['db']

    seek = ''
    if start_id:
        seek = 'AND id < :start_id'

    sql = """SELECT * FROM hive_modlog
              WHERE community_id = :community_id %s
           ORDER BY id DESC LIMIT :limit""" % seek
    return db.query_all(sql,
                        community_id=get_account_id(db, community),
                        start_id=start_id,
                        limit=limit)

async def list_invalid_posts(context, start, limit, community=None, observer=None):
    """Paginated list of invalid/muted posts, returned highest payout first."""
    db = context['db']

    seek = ''
    if start:
        start_author, start_permlink = start.split('/')
        start_id = get_post_id(db, start_author, start_permlink)
        seek = """AND payout <= (SELECT payout FROM hive_posts_cache
                                  WHERE id = :start_id)"""

    where = ''
    if community:
        where = 'AND community = :community'

    sql = """SELECT post_id FROM hive_posts_cache
              WHERE is_muted = '1'
                AND payout > 0 %s %s
           ORDER BY payout DESC
              LIMIT :limit""" % (seek, where)
    pids = db.query_col(sql, start_id=start_id, limit=limit)
    return posts_by_id(db, pids, observer)
