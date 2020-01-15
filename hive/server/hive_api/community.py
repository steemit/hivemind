"""Hive API: Community methods"""
import logging
from datetime import datetime
from dateutil.relativedelta import relativedelta
import ujson as json

from hive.server.hive_api.common import (get_account_id, get_community_id, valid_limit)
from hive.server.common.helpers import return_error_info

def days_ago(days):
    """Get the date `n` days ago."""
    return datetime.now() + relativedelta(days=-days)


# pylint: disable=too-many-lines

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

@return_error_info
async def get_community_context(context, name, account):
    """For a community/account: returns role, title, subscribed state"""
    db = context['db']
    cid = await get_community_id(db, name)
    assert cid, 'community not found'
    aid = await get_account_id(db, account)
    assert aid, 'account not found'

    sql = """SELECT role_id, title FROM hive_roles
              WHERE account_id = :aid
                AND community_id = :cid"""
    role = await db.query_row(sql, aid=aid, cid=cid) or (0, '')

    sql = """SELECT 1 FROM hive_subscriptions
              WHERE account_id = :aid
                AND community_id = :cid"""
    subscribed = bool(await db.query_one(sql, aid=aid, cid=cid))

    return dict(role=ROLES[role[0]], title=role[1], subscribed=subscribed)


@return_error_info
async def list_top_communities(context, limit=25):
    """List top communities. Returns lite community list."""
    assert limit < 100
    #sql = """SELECT name, title FROM hive_communities
    #          WHERE rank > 0 ORDER BY rank LIMIT :limit"""
    sql = """SELECT name, title FROM hive_communities
              WHERE id = 1344247 OR rank > 0
           ORDER BY (CASE WHEN id = 1344247 THEN 0 ELSE rank END)
              LIMIT :limit"""

    out = await context['db'].query_all(sql, limit=limit)

    return [(r[0], r[1]) for r in out]


@return_error_info
async def list_pop_communities(context, limit=25):
    """List communities by new subscriber count. Returns lite community list."""
    limit = valid_limit(limit, 25)
    sql = """SELECT name, title
               FROM hive_communities
               JOIN (
                         SELECT community_id, COUNT(*) newsubs
                           FROM hive_subscriptions
                          WHERE created_at > :cutoff
                       GROUP BY community_id
                    ) stats
                 ON stats.community_id = id
           ORDER BY newsubs DESC
              LIMIT :limit"""
    out = await context['db'].query_all(sql, limit=limit)

    return [(r[0], r[1]) for r in out]


@return_error_info
async def list_all_subscriptions(context, account):
    """Lists all communities `account` subscribes to, plus role and title in each."""
    db = context['db']
    account_id = await get_account_id(db, account)

    sql = """SELECT c.name, c.title, COALESCE(r.role_id, 0), COALESCE(r.title, '')
               FROM hive_communities c
               JOIN hive_subscriptions s ON c.id = s.community_id
          LEFT JOIN hive_roles r ON r.account_id = s.account_id
                                AND r.community_id = c.id
              WHERE s.account_id = :account_id
           ORDER BY COALESCE(role_id, 0) DESC, c.rank"""
    out = await db.query_all(sql, account_id=account_id)
    return [(r[0], r[1], ROLES[r[2]], r[3]) for r in out]

@return_error_info
async def list_subscribers(context, community):
    """Lists subscribers of `community`."""
    #limit = valid_limit(limit, 100)
    db = context['db']
    cid = await get_community_id(db, community)

    sql = """SELECT ha.name, hr.role_id, hr.title, hs.created_at
               FROM hive_subscriptions hs
          LEFT JOIN hive_roles hr ON hs.account_id = hr.account_id
                                 AND hs.community_id = hr.community_id
               JOIN hive_accounts ha ON hs.account_id = ha.id
              WHERE hs.community_id = :cid
           ORDER BY hs.created_at DESC
              LIMIT 250"""
    rows = await db.query_all(sql, cid=cid)
    return [(r['name'], ROLES[r['role_id'] or 0], r['title'],
             str(r['created_at'])) for r in rows]

@return_error_info
async def list_communities(context, last='', limit=100, query=None, observer=None):
    """List all communities, paginated. Returns lite community list."""
    limit = valid_limit(limit, 100)

    db = context['db']
    assert not query, 'query not yet supported'

    seek = ''
    if last:
        seek = """AND rank > (SELECT rank
                                FROM hive_communities
                               WHERE name = :last)"""

    sql = """SELECT id FROM hive_communities
              WHERE rank > 0 AND (num_pending > 0 OR LENGTH(about) > 3) %s
           ORDER BY rank LIMIT :limit""" % seek
    ids = await db.query_col(sql, last=last, limit=limit)
    if not ids: return []

    communities = await load_communities(db, ids, lite=True)
    if observer:
        observer_id = await get_account_id(db, observer)
        await _append_observer_subs(db, communities, observer_id)
        await _append_observer_roles(db, communities, observer_id)

    return [communities[_id] for _id in ids]

@return_error_info
async def list_community_roles(context, community, last='', limit=50):
    """List community account-roles (anyone with non-guest status)."""
    db = context['db']
    cid = await get_community_id(db, community)

    seek = ''
    lrole = None
    if last:
        sql = "SELECT role_id FROM hive_roles WHERE name = :name"
        lrole = await db.query_one(sql, name=last)
        assert lrole is not None, 'invalid start'
        seek = """AND (a.role_id < :lrole OR
                      (a.role_id = :lrole AND a.name > :last))"""

    sql = """SELECT a.name, r.role_id, r.title FROM hive_roles r
               JOIN hive_accounts a ON r.account_id = a.id
              WHERE r.community_id = :id %s
                AND r.role_id != 0
           ORDER BY r.role_id DESC, name LIMIT :limit""" % seek
    rows = await db.query_all(sql, id=cid, last=last, lrole=lrole, limit=limit)
    return [(r['name'], ROLES[r['role_id']], r['title']) for r in rows]

@return_error_info
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

# Communities - internal
# ----------------------

async def load_communities(db, ids, lite=True):
    """Retrieve full community objects. If not lite: includes settings, team.

    Observer: adds subcription status, user title, user role.
    """
    assert ids, 'no ids passed to load_communities'

    sql = """SELECT id, name, title, about, lang, type_id, is_nsfw, subscribers,
                    created_at, sum_pending, num_pending, num_authors %s
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
            'sum_pending': row['sum_pending'],
            'num_pending': row['num_pending'],
            'num_authors': row['num_authors'],
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
