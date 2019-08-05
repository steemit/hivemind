"""Hive API: community statistics"""

import logging
from hive.server.hive_api.community import get_community_id
log = logging.getLogger(__name__)

async def top_community_voters(context, community):
    """Get a list of top 5 (pending) community voters."""
    db = context['db']
    top = await _top_community_posts(db, community)
    total = {}
    for _, votes, _ in top:
        for vote in votes.split("\n"):
            voter, rshares = vote.split(',')[:2]
            if voter not in total:
                total[voter] += abs(int(rshares))
    return sorted(total, key=total.get, reverse=True)[:5]

async def top_community_authors(context, community):
    """Get a list of top 5 (pending) community authors."""
    db = context['db']
    top = await _top_community_posts(db, community)
    total = {}
    for author, _, payout in top:
        if author not in total:
            total[author] = 0
        total[author] += payout
    return sorted(total, key=total.get, reverse=True)[:5]

async def top_community_muted(context, community):
    """Get top authors (by SP) who are muted in a community."""
    db = context['db']
    sql = """SELECT a.name, a.voting_weight, r.title FROM hive_accounts a
               JOIN hive_roles r ON a.id = r.account_id
              WHERE r.community_id = :community_id AND r.role_id < 0
           ORDER BY voting_weight DESC LIMIT 5"""
    return db.query(sql, community_id=await get_community_id(db, community))

async def _top_community_posts(db, community, limit=50):
    sql = """SELECT author, votes, payout FROM hive_posts_cache
              WHERE category = :community AND is_paidout = '0'
           ORDER BY payout DESC LIMIT :limit"""
    return await db.query_all(sql, community=community, limit=limit)
