"""Hive API: Community methods"""
import logging
import ujson as json
from hive.server.hive_api.common import (
    get_account_id, get_community_id)
from hive.server.common.helpers import return_error_info

log = logging.getLogger(__name__)

ROLES = {-2: 'muted', 0: 'guest', 2: 'member', 4: 'mod', 6: 'admin', 8: 'owner'}

async def if_tag_community(context, tag, observer=None):
    """Attempt to load community if tag is proper format."""
    if tag[:5] == 'hive-':
        db = context['db']
        cid = await get_community_id(db, tag)
        if cid:
            return await get_community(context, tag, observer)
    return None

@return_error_info
async def get_community(context, name, observer=None):
    """Retrieve full community object. Includes metadata, leadership team

    If `observer` is provided, get subcription status, user title, user role.
    """
    db = context['db']
    cid = await get_community_id(db, name)
    assert cid, 'community not found'
    communities = await load_communities(db, [cid], lite=False)

    if observer:
        observer_id = await get_account_id(db, observer)
        await _append_observer_roles(db, communities, observer_id)
        await _append_observer_subs(db, communities, observer_id)

    return communities[cid]

async def list_top_communities(context, limit=25, observer_id=None):
    """List all communities, paginated. Returns lite community list."""
    db = context['db']

    out = []
    if observer_id:
        sql = """SELECT name, title FROM hive_communities
                  WHERE id IN (SELECT community_id
                                 FROM hive_subscriptions
                                WHERE account_id = :account_id)"""
        out += await db.query_all(sql, account_id=observer_id)

    sql = """SELECT name, title FROM hive_communities
              WHERE rank > 0 ORDER BY rank LIMIT :limit"""
    out += await db.query_all(sql, limit=limit)

    return [(r[0], r[1]) for r in out]

async def list_communities(context, last='', limit=25, query=None, observer=None):
    """List all communities, paginated. Returns lite community list."""
    db = context['db']
    assert not query, 'query not yet supported'

    seek = ''
    if last:
        seek = """ WHERE rank > (SELECT rank
                                   FROM hive_communities
                                  WHERE name = :last) """

    sql = "SELECT id FROM hive_communities %s WHERE rank > 0 ORDER BY rank" % seek
    ids = await db.query_col(sql, last=last, limit=limit)
    communities = await load_communities(db, ids, lite=True)

    if observer:
        observer_id = await get_account_id(db, observer)
        await _append_observer_subs(db, communities, observer_id)

    return [communities[_id] for _id in ids]

async def list_community_roles(context, community, last='', limit=50):
    """List community account-roles (anyone with non-guest status)."""
    db = context['db']
    community_id = await get_community_id(db, community)
    seek = ' AND a.name > :last' if last else ''
    sql = """SELECT a.name, r.role_id, r.title FROM hive_roles r
               JOIN hive_accounts a ON r.account_id = a.id
              WHERE r.community_id = :id %s
                AND r.role_id != 0
           ORDER BY name LIMIT :limit""" % seek
    rows = await db.query_all(sql, id=community_id, last=last, limit=limit)
    return [(r['name'], ROLES[r['role_id']], r['title']) for r in rows]

async def list_community_titles(context, community, last='', limit=50):
    """List community account-titles (anyone with custom title)."""
    db = context['db']
    community_id = await get_community_id(db, community)
    seek = ' AND a.name > :last' if last else ''
    sql = """SELECT a.name, r.role_id, r.title FROM hive_roles r
               JOIN hive_accounts a ON r.account_id = a.id
              WHERE r.community_id = :id %s
                AND r.title != ''
           ORDER BY name LIMIT :limit""" % seek
    rows = await db.query_all(sql, id=community_id, last=last, limit=limit)
    return [(r['name'], ROLES[r['role_id']], r['title']) for r in rows]

async def list_all_subscriptions(context, account):
    """Lists all communities `account` subscribes to, and any role/title."""
    db = context['db']
    account_id = await get_account_id(db, account)

    sql = """SELECT community_id FROM hive_subscriptions
              WHERE account_id = :account_id"""
    ids = await db.query_col(sql, account_id=account_id)
    if not ids: return []
    communities = await load_communities(db, ids, lite=True)
    await _append_observer_roles(db, communities, account_id)
    for comm in communities.values():
        comm['context']['subscribed'] = True
    return [communities[_id] for _id in ids]


# Communities - internal
# ----------------------

async def load_communities(db, ids, lite=True):
    """Retrieve full community objects. If not lite: includes settings, team.

    Observer: adds subcription status, user title, user role.
    """

    sql = """SELECT id, name, title, about, lang, type_id, is_nsfw,
                    subscribers, created_at %s
               FROM hive_communities WHERE id IN :ids"""
    fields = ', description, flag_text, settings' if not lite else ''
    rows = await db.query_all(sql % fields, ids=tuple(ids))

    out = {}
    for row in rows:
        ret = {
            'id': row['id'],
            'name': row['name'],
            'title': row['title'] or ('@' + row['name']),
            'about': row['about'],
            'lang': row['lang'],
            'type_id': row['type_id'],
            'is_nsfw': row['is_nsfw'],
            'subscribers': row['subscribers'],
            'created_at': str(row['created_at']),
            'context': {},
        }

        if not lite:
            ret['description'] = row['description']
            ret['flag_text'] = row['flag_text']
            ret['settings'] = json.loads(row['settings'])
            ret['team'] = await _community_team(db, ret['id'])

        out[ret['id']] = ret

    return out

async def _community_team(db, community_id):
    sql = """SELECT a.name, r.role_id, r.title FROM hive_roles r
               JOIN hive_accounts a ON r.account_id = a.id
              WHERE r.community_id = :community_id
                AND r.role_id BETWEEN 4 AND 8
           ORDER BY r.role_id DESC"""
    rows = await db.query_all(sql, community_id=community_id)
    return [(r['name'], ROLES[r['role_id']], r['title']) for r in rows]

async def _append_observer_roles(db, communities, observer_id):
    ids = communities.keys()

    sql = """SELECT community_id, role_id, title FROM hive_roles
              WHERE account_id = :account_id
                AND community_id IN :ids"""
    rows = await db.query_all(sql, account_id=observer_id, ids=tuple(ids))
    roles = {r['community_id']: [r['role_id'], r['title']] for r in rows}

    for cid, comm in communities.items():
        role_id, title = roles[cid] if cid in roles else (0, '')
        comm['context']['role'] = ROLES[role_id]
        comm['context']['title'] = title

async def _append_observer_subs(db, communities, observer_id):
    ids = communities.keys()

    sql = """SELECT community_id FROM hive_subscriptions
              WHERE account_id = :account_id
                AND community_id IN :ids"""
    subs = await db.query_col(sql, account_id=observer_id, ids=tuple(ids))

    for cid, comm in communities.items():
        comm['context']['subscribed'] = cid in subs


# Stats
# -----

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
