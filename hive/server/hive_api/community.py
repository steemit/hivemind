"""Hive API: Community methods"""
import logging

from hive.server.hive_api.common import (
    get_account_id, get_community_id)

log = logging.getLogger(__name__)

# pylint: disable=too-many-arguments

ROLES = {-2: 'muted', 0: 'guest', 2: 'member', 4: 'mod', 6: 'admin', 8: 'owner'}

async def get_community(context, name, observer=None):
    """Retrieve full community object. Includes metadata, leadership team

    If `observer` is provided, get subcrption status, user title, user role.
    """
    db = context['db']

    # community md
    sql = """SELECT id, name, title, about, lang, type_id, is_nsfw,
                    subscribers, created_at, settings
               FROM hive_communities WHERE name = :name"""
    row = await db.query_row(sql, name=name)
    assert row, 'community not found'

    ret = {
        'id': row['id'],
        'name': row['name'],
        'title': row['title'],
        'about': row['about'],
        'lang': row['lang'],
        'type': row['type_id'],
        'is_nsfw': row['is_nsfw'],
        'subscribers': row['subscribers'],
        'created_at': row['created_at'],
        'settings': row['settings'],
        'team': {'owner': {}, 'admin': {}, 'mod': {}},
    }

    # leadership
    sql = """SELECT a.name, r.role_id, r.title FROM hive_roles r
               JOIN hive_accounts a ON r.account_id = a.id
              WHERE r.community_id = :community_id
                AND r.role_id >= :min_role"""
    for row in await db.query_all(sql, community_id=ret['id'], min_role=4):
        role = ROLES[row['role_id']]
        ret['team'][role][row['account']] = row['title']

    if observer: # context: role, title, subscribed
        observer_id = await get_account_id(db, observer)
        await _community_contexts(db, [ret], observer_id)

    return ret

async def _community_contexts(db, communities, observer_id):
    comms = {c['id']: c for c in communities}
    ids = comms.keys()

    # load role and title in each community
    sql = """SELECT community_id, role_id, title FROM hive_roles
              WHERE account_id = :account_id
                AND community_id IN :ids"""
    rows = await db.query_all(sql, account_id=observer_id, ids=tuple(ids))
    roles = {cid: [role_id, title] for cid, role_id, title in rows}

    # load subscription status
    sql = """SELECT community_id FROM hive_subscriptions
              WHERE account_id = :account_id
                AND community_id IN :ids"""
    subs = await db.query_col(sql, account_id=observer_id, ids=tuple(ids))

    for cid, comm in comms.items():
        role, title = roles[cid] if cid in roles else (0, '')
        comm['context'] = {
            'role': role,
            'title': title,
            'subscribed' : cid in subs}

async def list_communities(context, start='', limit=25, query=None, observer=None):
    """List all communities, paginated. Returns lite community list.

    Fields: (id, name, title, about, lang, type, nsfw, subs, created_at)
    """
    db = context['db']
    observer_id = await get_account_id(db, observer) if observer else None

    assert not query, 'query not yet supported'

    seek = ''
    if start:
        seek = """ WHERE rank <= (SELECT rank
                                   FROM hive_communities
                                  WHERE name = :start)"""

    sql = """SELECT id, name, title, about, lang, type_id, is_nsfw, rank,
                    subscribers, created_at
               FROM hive_communities %s
           ORDER BY rank DESC""" % seek
    result = [dict(r) for r in await db.query_all(sql, start=start, limit=limit)]

    if observer_id:
        sql = """SELECT community_id FROM hive_subscriptions
                  WHERE account_id = :account_id
                    AND community_id IN (:ids)"""
        subscribed = await db.query_col(sql,
                                        account_id=observer_id,
                                        ids=tuple([r['id'] for r in result]))
        for comm in result:
            comm['context']['subscribed'] = comm['id'] in subscribed

    return result


async def list_community_roles(context, community, start='', limit=50):
    """List community account-roles (non-guests and those with usertitles)."""
    db = context['db']
    community_id = await get_community_id(db, community)
    seek = ' AND account >= :start' if start else ''
    sql = """SELECT a.name, r.role_id, r.title FROM hive_roles
               JOIN hive_accounts a ON r.account_id = a.id
              WHERE r.community_id = :community_id %s
           ORDER BY name LIMIT :limit""" % seek
    return await db.query_all(sql, community_id=community_id, start=start, limit=limit)


async def list_all_subscriptions(context, account, observer=None):
    """Lists all communities `account` subscribes to, and any role/title.

    Observer: includes `subscribed` status."""
    db = context['db']

    sql = """SELECT c.name, r.role_id, r.title
               FROM hive_communities c
               JOIN hive_subscriptions s ON c.id = s.community_id
          LEFT JOIN hive_roles r ON r.community_id = s.community_id
                                AND r.account_id = s.account_id
              WHERE s.account_id = :account_id"""
    result = await db.query_all(sql, account_id=await get_account_id(db, account))

    if observer:
        sql = """SELECT community_id FROM hive_subscriptions
                  WHERE account_id = :account_id AND community_id IN :ids"""
        subscribed = await db.query_col(sql, account_id=await get_account_id(db, observer),
                                        ids=[r['id'] for r in result])
        for row in result:
            if row['id'] in subscribed:
                row['context'] = {'subscribed': True}
    return result

async def top_community_voters(context, community):
    """Get a list of top 5 (pending) community voters."""
    # TODO: which are voting on muted posts?
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
    return await db.query(sql, community_id=await get_community_id(db, community))

async def _top_community_posts(db, community, limit=50):
    # TODO: muted equivalent
    sql = """SELECT author, votes, payout FROM hive_posts_cache
              WHERE category = :community AND is_paidout = '0'
                AND post_id IN (SELECT id FROM hive_posts WHERE is_muted = '0')
           ORDER BY payout DESC LIMIT :limit"""
    return await db.query_all(sql, community=community, limit=limit)
