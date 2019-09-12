"""Cursor-based pagination queries, mostly supporting bridge_api."""

from datetime import datetime
from dateutil.relativedelta import relativedelta

# pylint: disable=too-many-lines

def last_month():
    """Get the date 1 month ago."""
    return datetime.now() + relativedelta(months=-1)

async def get_post_id(db, author, permlink):
    """Given an author/permlink, retrieve the id from db."""
    sql = ("SELECT id FROM hive_posts WHERE author = :a "
           "AND permlink = :p AND is_deleted = '0' LIMIT 1")
    return await db.query_one(sql, a=author, p=permlink)

async def _get_post_id(db, author, permlink):
    """Get post_id from hive db. (does NOT filter on is_deleted)"""
    sql = "SELECT id FROM hive_posts WHERE author = :a AND permlink = :p"
    post_id = await db.query_one(sql, a=author, p=permlink)
    assert post_id, 'invalid author/permlink'
    return post_id

async def _get_account_id(db, name):
    """Get account id from hive db."""
    assert name, 'no account name specified'
    _id = await db.query_one("SELECT id FROM hive_accounts WHERE name = :n", n=name)
    assert _id, "account not found: `%s`" % name
    return _id

async def _get_community_id(db, name):
    """Get community id from hive db."""
    assert name, 'no comm name specified'
    _id = await db.query_one("SELECT id FROM hive_communities WHERE name = :n", n=name)
    assert _id, "comm not found: `%s`" % name
    return _id

async def pids_by_community(db, ids, sort, start_author, start_permlink, limit):
    """Get a list of post_ids for a given posts query.

    `sort` can be trending, hot, created, promoted, payout, or payout_comments.
    """
    # pylint: disable=too-many-arguments,bad-whitespace,line-too-long,too-many-locals

    definitions = {#         field      pending toponly gray   promoted
        'trending':        ('sc_trend', False,  True,   False, False),
        'hot':             ('sc_hot',   False,  True,   False, False),
        'created':         ('post_id',  False,  True,   False, False),
        'promoted':        ('promoted', True,   True,   False, True),
        'payout':          ('payout',   True,   False,  False, False),
        'muted':           ('payout',   True,   False,  True,  False)}

    # validate
    assert ids, 'no community ids provided to query'
    assert sort in definitions, 'unknown sort %s' % sort

    # setup
    field, pending, toponly, gray, promoted = definitions[sort]
    table = 'hive_posts_cache'
    where = ["community_id IN :ids"]

    # select
    if gray:     where.append("is_grayed = '1'")
    if not gray: where.append("is_grayed = '0'")
    if toponly:  where.append("depth = 0")
    if pending:  where.append("is_paidout = '0'")
    if promoted: where.append('promoted > 0')

    pinned_ids = []

    # seek
    seek_id = None
    if start_permlink:
        # simpler `%s <= %s` eval has edge case: many posts with payout 0
        seek_id = await _get_post_id(db, start_author, start_permlink)
        sval = "(SELECT %s FROM %s WHERE post_id = :seek_id)" % (field, table)
        sql = """((%s < %s) OR (%s = %s AND post_id > :seek_id))"""
        where.append(sql % (field, sval, field, sval))

        #seek_id = await _get_post_id(db, start_author, start_permlink)
        #sql = "SELECT %s FROM %s WHERE post_id = :id)"
        #seek_val = await db.query_col(sql % (field, table), id=seek_id)
        #sql = """((%s < :seek_val) OR
        #          (%s = :seek_val AND post_id > :seek_id))"""
        #where.append(sql % (field, sval, field, sval))
    elif len(ids) == 1:
        pinned_ids = await _pinned(db, ids[0])

    # build
    sql = ("""SELECT post_id FROM %s WHERE %s
              ORDER BY %s DESC, post_id LIMIT :limit
              """ % (table, ' AND '.join(where), field))

    # execute
    return pinned_ids + await db.query_col(sql, ids=tuple(ids), seek_id=seek_id, limit=limit)



async def pids_by_ranked(db, sort, start_author, start_permlink, limit, tag, observer_id=None):
    """Get a list of post_ids for a given posts query.

    `sort` can be trending, hot, created, promoted, payout, or payout_comments.
    """
    # pylint: disable=too-many-arguments,bad-whitespace,line-too-long

    # pylint: disable=too-many-locals,too-many-branches
    # branch on tag/observer
    if tag:
        cids = []
        if tag[:5] == 'hive-':
            cids = [await _get_community_id(db, tag)]
        elif tag == 'my':
            cids = await _subscribed(db, observer_id)
        if cids:
            return await pids_by_community(db, cids, sort, start_author,
                                           start_permlink, limit)

    assert sort in ['trending', 'hot', 'created', 'promoted',
                    'payout', 'payout_comments']

    params = {             # field      pending posts   comment promoted    todo
        'trending':        ('sc_trend', True,   False,  False,  False),   # depth=0
        'hot':             ('sc_hot',   True,   False,  False,  False),   # depth=0
        'created':         ('post_id',  False,  True,   False,  False),
        'promoted':        ('promoted', True,   False,  False,  True),
        'payout':          ('payout',   True,   True,   False,  False),
        'payout_comments': ('payout',   True,   False,  True,   False),
    }[sort]

    table = 'hive_posts_cache'
    field = params[0]
    where = []

    # primary filters
    if params[1]: where.append("is_paidout = '0'")
    if params[2]: where.append('depth = 0')
    if params[3]: where.append('depth > 0')
    if params[4]: where.append('promoted > 0')

    # filter by category or tag
    if tag:
        if sort in ['payout', 'payout_comments']:
            where.append('category = :tag')
        else:
            sql = "SELECT post_id FROM hive_post_tags WHERE tag = :tag"
            where.append("post_id IN (%s)" % sql)

    start_id = None
    if start_permlink:
        start_id = await _get_post_id(db, start_author, start_permlink)
        sql = "%s <= (SELECT %s FROM %s WHERE post_id = :start_id)"
        where.append(sql % (field, field, table))

    sql = ("SELECT post_id FROM %s WHERE %s ORDER BY %s DESC LIMIT :limit"
           % (table, ' AND '.join(where), field))

    return await db.query_col(sql, tag=tag, start_id=start_id, limit=limit)

async def _subscribed(db, account_id):
    sql = """SELECT community_id FROM hive_subscriptions
              WHERE account_id = :account_id"""
    return await db.query_col(sql, account_id=account_id)

async def _pinned(db, community_id):
    """Get a list of pinned post `id`s in `community`."""
    sql = "SELECT name FROM hive_communities WHERE id = :id"
    community = db.query_one(sql, id=community_id)
    sql = """SELECT id FROM hive_posts
              WHERE is_pinned = '1'
                AND is_deleted = '0'
                AND community = :community
                AND id IN (SELECT post_id FROM hive_post_tags
                            WHERE tag = :community)
            ORDER BY id DESC"""
    return await db.query_col(sql, community=community)


async def pids_by_payout(db, account: str, start_author: str = '',
                         start_permlink: str = '', limit: int = 20):
    """Get a list of post_ids for an author's blog."""
    seek = ''
    start_id = None
    if start_permlink:
        start_id = await _get_post_id(db, start_author, start_permlink)
        seek = """
          AND rshares <= (
            SELECT rshares
              FROM hive_posts_cache
             WHERE post_id = :start_id)
        """

    sql = """
        SELECT hpc.post_id
          FROM hive_posts_cache hpc
          JOIN hive_posts hp ON hp.id = hpc.post_id
         WHERE hp.author = :account
           AND hp.is_deleted = '0'
           AND hpc.is_paidout = '0' %s
           AND hpc.rshares > 0
      ORDER BY rshares DESC
         LIMIT :limit
    """ % seek

    return await db.query_col(sql, account=account, start_id=start_id, limit=limit)

async def pids_by_blog(db, account: str, start_author: str = '',
                       start_permlink: str = '', limit: int = 20):
    """Get a list of post_ids for an author's blog."""
    account_id = await _get_account_id(db, account)

    seek = ''
    start_id = None
    if start_permlink:
        start_id = await _get_post_id(db, start_author, start_permlink)
        seek = """
          AND created_at <= (
            SELECT created_at
              FROM hive_feed_cache
             WHERE account_id = :account_id
               AND post_id = :start_id)
        """

    sql = """
        SELECT post_id
          FROM hive_feed_cache
         WHERE account_id = :account_id %s
      ORDER BY created_at DESC
         LIMIT :limit
    """ % seek

    return await db.query_col(sql, account_id=account_id, start_id=start_id, limit=limit)

async def pids_by_feed_with_reblog(db, account: str, start_author: str = '',
                                   start_permlink: str = '', limit: int = 20):
    """Get a list of [post_id, reblogged_by_str] for an account's feed."""
    account_id = await _get_account_id(db, account)

    seek = ''
    start_id = None
    if start_permlink:
        start_id = await _get_post_id(db, start_author, start_permlink)
        if not start_id:
            return []

        seek = """
          HAVING MIN(hive_feed_cache.created_at) <= (
            SELECT MIN(created_at) FROM hive_feed_cache WHERE post_id = :start_id
               AND account_id IN (SELECT following FROM hive_follows
                                  WHERE follower = :account AND state = 1))
        """

    sql = """
        SELECT post_id, string_agg(name, ',') accounts
          FROM hive_feed_cache
          JOIN hive_follows ON account_id = hive_follows.following AND state = 1
          JOIN hive_accounts ON hive_follows.following = hive_accounts.id
         WHERE hive_follows.follower = :account
           AND hive_feed_cache.created_at > :cutoff
      GROUP BY post_id %s
      ORDER BY MIN(hive_feed_cache.created_at) DESC LIMIT :limit
    """ % seek

    result = await db.query_all(sql, account=account_id, start_id=start_id,
                                limit=limit, cutoff=last_month())
    return [(row[0], row[1]) for row in result]


async def pids_by_comments(db, account: str, start_permlink: str = '', limit: int = 20):
    """Get a list of post_ids representing comments by an author."""
    seek = ''
    start_id = None
    if start_permlink:
        start_id = await _get_post_id(db, account, start_permlink)
        if not start_id:
            return []

        seek = "AND id <= :start_id"

    # `depth` in ORDER BY is a no-op, but forces an ix3 index scan (see #189)
    sql = """
        SELECT id FROM hive_posts
         WHERE author = :account %s
           AND depth > 0
           AND is_deleted = '0'
      ORDER BY id DESC, depth
         LIMIT :limit
    """ % seek

    return await db.query_col(sql, account=account, start_id=start_id, limit=limit)


async def pids_by_replies(db, start_author: str, start_permlink: str = '',
                          limit: int = 20):
    """Get a list of post_ids representing replies to an author.

    To get the first page of results, specify `start_author` as the
    account being replied to. For successive pages, provide the
    last loaded reply's author/permlink.
    """
    seek = ''
    start_id = None
    if start_permlink:
        sql = """
          SELECT parent.author,
                 child.id
            FROM hive_posts child
            JOIN hive_posts parent
              ON child.parent_id = parent.id
           WHERE child.author = :author
             AND child.permlink = :permlink
        """

        row = await db.query_row(sql, author=start_author, permlink=start_permlink)
        if not row:
            return []

        parent_account = row[0]
        start_id = row[1]
        seek = "AND id <= :start_id"
    else:
        parent_account = start_author

    sql = """
       SELECT id FROM hive_posts
        WHERE parent_id IN (SELECT id FROM hive_posts
                             WHERE author = :parent
                               AND is_deleted = '0'
                          ORDER BY id DESC
                             LIMIT 10000) %s
          AND is_deleted = '0'
     ORDER BY id DESC
        LIMIT :limit
    """ % seek

    return await db.query_col(sql, parent=parent_account, start_id=start_id, limit=limit)
