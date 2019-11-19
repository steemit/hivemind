"""Hive API: Stats"""
import logging

from hive.server.common.helpers import return_error_info
from hive.server.common.payout_stats import PayoutStats
from hive.server.hive_api.common import valid_limit

log = logging.getLogger(__name__)

@return_error_info
async def get_payout_stats(context, limit=100):
    """Get payout stats for building treemap."""
    db = context['db']
    limit = valid_limit(limit, 100)

    stats = PayoutStats.instance()
    await stats.generate()

    sql = """
        SELECT hc.title, author, payout, posts, authors
          FROM payout_stats
     LEFT JOIN hive_communities hc ON hc.id = community_id
         WHERE (community_id IS NULL AND author IS NOT NULL)
            OR (community_id IS NOT NULL AND author IS NULL)
      ORDER BY payout DESC
         LIMIT :limit
    """

    rows = await db.query_all(sql, limit=limit)
    items = [(r['title'], r['author'], float(r['payout']),
              r['posts'], r['authors']) for r in rows]

    sql = """SELECT SUM(payout) FROM payout_stats WHERE author IS NULL"""
    total = await db.query_one(sql)

    sql = """SELECT SUM(payout) FROM payout_stats
              WHERE community_id IS NULL AND author IS NULL"""
    blog_ttl = await db.query_one(sql)

    return dict(items=items, total=float(total), blogs=float(blog_ttl))
