"""condenser_api trending tag fetching methods"""

from aiocache import cached
from hive.server.condenser_api.common import (return_error_info, valid_limit)

@return_error_info
@cached(ttl=7200, timeout=1200)
async def get_top_trending_tags_summary(context, limit=50):
    """Get top trending tags among pending posts."""
    sql = """
        SELECT category
          FROM hive_posts_cache
         WHERE is_paidout = '0'
      GROUP BY category
      ORDER BY SUM(payout) DESC
         LIMIT :limit
    """
    return await context['db'].query_col(sql, limit=limit)

@return_error_info
@cached(ttl=3600, timeout=1200)
async def get_trending_tags(context, start_tag='', limit: int = 250):
    """Get top trending tags among pending posts, with stats."""
    assert not start_tag, 'pagination not supported'
    limit = valid_limit(limit, ubound=250)

    sql = """
      SELECT category,
             COUNT(*) AS total_posts,
             SUM(CASE WHEN depth = 0 THEN 1 ELSE 0 END) AS top_posts,
             SUM(payout) AS total_payouts
        FROM hive_posts_cache
       WHERE is_paidout = '0'
    GROUP BY category
    ORDER BY SUM(payout) DESC
       LIMIT :limit
    """

    out = []
    for row in await context['db'].query_all(sql, limit=limit):
        out.append({
            'name': row['category'],
            'comments': row['total_posts'] - row['top_posts'],
            'top_posts': row['top_posts'],
            'total_payouts': "%.3f SBD" % row['total_payouts']})

    return out
