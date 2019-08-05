"""Hive API: Community methods"""
import logging

from hive.server.hive_api.common import get_account_id, valid_sort, url_to_id, valid_limit
from hive.server.hive_api.post import posts_by_id, ranked_pids

log = logging.getLogger(__name__)

# pylint: disable=too-many-arguments

ROLES = {-2: 'muted', 0: 'guest', 2: 'member', 4: 'mod', 6: 'admin', 8: 'owner'}

async def get_community_id(db, name):
    """Get community id from db."""
    return db.query_one("SELECT id FROM hive_communities WHERE name = :name",
                        name=name)

async def get_community(context, name, observer=None):
    """Retrieve full community object. Includes metadata, leadership team

    If `observer` is provided, get subcrption status, user title, user role.
    """
    db = context['db']

    observer_id = get_account_id(db, observer) if observer else None

    # community md
    sql = """SELECT id, name, title, about, lang, type_id, is_nsfw,
                    subscribers, created_at, settings
               FROM hive_communities WHERE name = :name"""
    row = db.query_row(sql, name=name)
    community_id = row['id']

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
    }

    # leadership
    sql = """SELECT a.name, r.role_id, r.title FROM hive_roles r
               JOIN hive_accounts a ON r.account_id = a.id
              WHERE r.community_id = :community_id AND r.role_id >= :min_role"""
    roles = db.query_all(sql, community_id=community_id, min_role=4)
    ret['team'] = {'owner': {}, 'admin': {}, 'mod': {}}
    for account, role_id, title in roles:
        ret['team'][ROLES[role_id]][account] = title

    # context: role, title, subscribed
    if observer_id:
        row = db.query_row("""SELECT role_id, title FROM hive_roles
                               WHERE community_id = :community_id
                                 AND account_id = :account_id""",
                           community_id=community_id,
                           account_id=observer_id)
        role, title = row if row else (0, None)
        subscribed = db.query_one("""SELECT 1 FROM hive_subscriptions
                                      WHERE community_id = :community_id
                                        AND account_id = :account_id""",
                                  community_id=community_id,
                                  account_id=observer_id)
        ret['context'] = {
            'role': role,
            'title': title,
            'subscribed' : subscribed == 1}

    return ret


async def list_communities(context, start='', limit=25, query=None, observer=None):
    """List all communities, paginated. Returns lite community list.

    Fields: (id, name, title, about, lang, type, nsfw, subs, created_at)
    """
    db = context['db']

    assert not query, 'query not yet supported'

    seek = ''
    if start:
        seek = ' WHERE rank <= (SELECT rank FROM hive_communities WHERE name = :start)'

    sql = """SELECT id, name, title, about, lang, type_id, is_nsfw, rank,
                    subscribers, created_at
               FROM hive_communities %s
           ORDER BY rank DESC""" % seek
    result = {r['id']: r for r in db.query_all(sql, start=start, limit=limit)}

    if observer:
        sql = """SELECT community_id FROM hive_subscriptions
                  WHERE account_id = :account_id AND community_id IN (:ids)"""
        subscribed = db.query_col(sql,
                                  account_id=get_account_id(db, observer),
                                  ids=tuple(result.keys()))
        for _id in subscribed:
            result[_id]['subscribed'] = True

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
    return db.query_all(sql, community_id=community_id, start=start, limit=limit)


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
    result = db.query_all(sql, account_id=get_account_id(db, account))

    if observer:
        sql = """SELECT community_id FROM hive_subscriptions
                  WHERE account_id = :account_id AND community_id IN :ids"""
        subscribed = db.query_col(sql, account_id=get_account_id(db, observer),
                                  ids=[r['id'] for r in result])
        for row in result:
            if row['id'] in subscribed:
                row['context'] = {'subscribed': True}
    return result

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
