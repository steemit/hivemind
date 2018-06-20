"""condenser_api trending tag fetching methods"""

from aiocache import cached
from hive.db.methods import query_col, query_all

@cached(ttl=3600)
async def get_top_trending_tags_summary():
    """Get top 50 trending tags among pending posts."""
    # Same results, more overhead:
    #return [tag['name'] for tag in await get_trending_tags('', 50)]
    sql = """
        SELECT category
          FROM hive_posts_cache
         WHERE is_paidout = '0'
      GROUP BY category
      ORDER BY SUM(payout) DESC
         LIMIT 50
    """
    return query_col(sql)

@cached(ttl=3600)
async def get_trending_tags(start_tag: str = '', limit: int = 250):
    """Get top 250 trending tags among pending posts, with stats."""
    assert start_tag is None or start_tag == '', 'tags pagination not supported'
    assert limit == 250, 'only returns exactly 250 tags'
    sql = """
      SELECT category,
             COUNT(*) AS total_posts,
             SUM(CASE WHEN depth = 0 THEN 1 ELSE 0 END) AS top_posts,
             SUM(payout) AS total_payouts
        FROM hive_posts_cache
       WHERE is_paidout = '0'
    GROUP BY category
    ORDER BY SUM(payout) DESC
       LIMIT 250
    """
    out = []
    for row in query_all(sql):
        out.append({
            'comments': row['total_posts'] - row['top_posts'],
            'name': row['category'],
            'top_posts': row['top_posts'],
            'total_payouts': "%.3f SBD" % row['total_payouts']})

    return out
