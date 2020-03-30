"""Cursor-based pagination queries, mostly supporting bridge_api."""

from datetime import datetime
from dateutil.relativedelta import relativedelta

# pylint: disable=too-many-lines

DEFAULT_CID = 1317453
PAYOUT_WINDOW = "now() + interval '12 hours' AND now() + interval '36 hours'"

def last_month():
    """Get the date 1 month ago."""
    return datetime.now() + relativedelta(months=-1)

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
    return _id

#TODO: async def posts_by_ranked
async def pids_by_ranked(db, sort, start_author, start_permlink, limit, tag, observer_id=None):
    """Get a list of post_ids for a given posts query.

    if `tag` is blank: global trending
    if `tag` is `my`: personal trending
    if `tag` is `hive-*`: community trending
    else `tag` is a tag: tag trending

    Valid `sort` values:
     - legacy: trending, hot, created, promoted, payout, payout_comments
     - hive: trending, hot, created, promoted, payout, muted
    """
    # TODO: `payout` should limit to ~24hrs
    # pylint: disable=too-many-arguments

    # list of comm ids to query, if tag is comms key
    cids = None
    single = None
    if tag == 'my':
        cids = await _subscribed(db, observer_id)
        if not cids: return []
    elif tag == 'all':
        cids = []
    elif tag[:5] == 'hive-':
        single = await _get_community_id(db, tag)
        if single: cids = [single]

    # if tag was comms key, then no tag filter
    if cids is not None: tag = None

    start_id = None
    if start_permlink:
        start_id = await _get_post_id(db, start_author, start_permlink)

    if cids is None:
        pids = await pids_by_category(db, tag, sort, start_id, limit)
    else:
        pids = await pids_by_community(db, cids, sort, start_id, limit)

    # if not filtered by tag, is first page trending: prepend pinned
    if not tag and not start_id and sort in ('trending', 'created'):
        prepend = await _pinned(db, single or DEFAULT_CID)
        for pid in prepend:
            if pid in pids:
                pids.remove(pid)
        pids = prepend + pids

    # first page prepend pinned
    if not tag and not cids and not start_id:
        first_prepend = await _pids_by_type(db, '2')
        for pid in first_prepend:
            if pid in pids:
                pids.remove(pid)
        pids = first_prepend + pids

    # hide posts
    hide_pids = await hide_pids_by_ids(db, pids)
    for pid in hide_pids:
        if pid in pids:
            pids.remove(pid)

    return pids


async def pids_by_community(db, ids, sort, seek_id, limit):
    """Get a list of post_ids for a given posts query.

    `sort` can be trending, hot, created, promoted, payout, or payout_comments.
    """
    # pylint: disable=bad-whitespace, line-too-long

    # TODO: `payout` should limit to ~24hrs
    definitions = {#         field         pending toponly gray   promoted
        'trending':        ('sc_trend',    False,  True,   False, False),
        'hot':             ('sc_hot',      False,  True,   False, False),
        'created':         ('created_at',  False,  True,   False, False),
        'promoted':        ('promoted',    True,   True,   False, True),
        'payout':          ('payout',      True,   False,  False, False),
        'muted':           ('payout',      True,   False,  True,  False)}

    # validate
    assert sort in definitions, 'unknown sort %s' % sort

    # setup
    field, pending, toponly, gray, promoted = definitions[sort]
    table = 'hive_posts_cache'
    where = ["community_id IN :ids"] if ids else ["community_id IS NOT NULL AND community_id != 1337319"]

    # select
    if gray:     where.append("is_grayed = '1'")
    if not gray: where.append("is_grayed = '0'")
    if toponly:  where.append("depth = 0")
    if pending:  where.append("is_paidout = '0'")
    if promoted: where.append('promoted > 0')
    if sort == 'payout': where.append("payout_at BETWEEN %s" % PAYOUT_WINDOW)

    # seek
    if seek_id:
        sval = "(SELECT %s FROM %s WHERE post_id = :seek_id)" % (field, table)
        sql = """((%s < %s) OR (%s = %s AND post_id > :seek_id))"""
        where.append(sql % (field, sval, field, sval))

        # simpler `%s <= %s` eval has edge case: many posts with payout 0
        #sql = "SELECT %s FROM %s WHERE post_id = :id)"
        #seek_val = await db.query_col(sql % (field, table), id=seek_id)
        #sql = """((%s < :seek_val) OR
        #          (%s = :seek_val AND post_id > :seek_id))"""
        #where.append(sql % (field, sval, field, sval))

    # hide posts
    #sql = "SELECT post_id FROM hive_posts_status WHERE list_type = '1'"
    #where.append("post_id NOT IN (%s)" % sql)

    # hide author
    sql = "SELECT author FROM hive_posts_status WHERE list_type = '3'"
    where.append("author NOT IN (%s)" % sql)

    # build
    sql = ("""SELECT post_id FROM %s WHERE %s
              ORDER BY %s DESC, post_id LIMIT :limit
              """ % (table, ' AND '.join(where), field))

    # execute
    return await db.query_col(sql, ids=tuple(ids), seek_id=seek_id, limit=limit)



async def pids_by_category(db, tag, sort, last_id, limit):
    """Get a list of post_ids for a given posts query.

    `sort` can be trending, hot, created, promoted, payout, or payout_comments.
    """
    # pylint: disable=bad-whitespace
    assert sort in ['trending', 'hot', 'created', 'promoted',
                    'payout', 'payout_comments', 'muted']

    params = {             # field      pending posts   comment promoted
        'trending':        ('sc_trend', True,   True,   False,  False),
        'hot':             ('sc_hot',   True,   True,   False,  False),
        'created':         ('post_id',  False,  True,   False,  False),
        'promoted':        ('promoted', True,   False,  False,  True),
        'payout':          ('payout',   True,   False,  False,  False),
        'payout_comments': ('payout',   True,   False,  True,   False),
        'muted':           ('payout',   True,   False,  False,  False),
    }[sort]

    table = 'hive_posts_cache'
    field = params[0]
    where = []

    # primary filters
    if params[1]: where.append("is_paidout = '0'")
    if params[2]: where.append('depth = 0')
    if params[3]: where.append('depth > 0')
    if params[4]: where.append('promoted > 0')
    if sort == 'muted': where.append("is_grayed = '1' AND payout > 0")
    if sort == 'payout': where.append("payout_at BETWEEN %s" % PAYOUT_WINDOW)

    # filter by category or tag
    if tag:
        if sort in ['payout', 'payout_comments']:
            where.append('category = :tag')
        else:
            sql = "SELECT post_id FROM hive_post_tags WHERE tag = :tag"
            where.append("post_id IN (%s)" % sql)

    if last_id:
        sval = "(SELECT %s FROM %s WHERE post_id = :last_id)" % (field, table)
        sql = """((%s < %s) OR (%s = %s AND post_id > :last_id))"""
        where.append(sql % (field, sval, field, sval))

    # hide posts
    #sql = "SELECT post_id FROM hive_posts_status WHERE list_type = '1'"
    #where.append("post_id NOT IN (%s)" % sql)

    # hide author
    sql = "SELECT author FROM hive_posts_status WHERE list_type = '3'"
    where.append("author NOT IN (%s)" % sql)

    sql = ("""SELECT post_id FROM %s WHERE %s
              ORDER BY %s DESC, post_id LIMIT :limit
              """ % (table, ' AND '.join(where), field))

    return await db.query_col(sql, tag=tag, last_id=last_id, limit=limit)


async def _subscribed(db, account_id):
    sql = """SELECT community_id FROM hive_subscriptions
              WHERE account_id = :account_id"""
    return await db.query_col(sql, account_id=account_id)


async def _pinned(db, community_id):
    """Get a list of pinned post `id`s in `community`."""
    sql = """SELECT id FROM hive_posts
              WHERE is_pinned = '1'
                AND is_deleted = '0'
                AND community_id = :community_id
            ORDER BY id DESC"""
    return await db.query_col(sql, community_id=community_id)


async def _pids_by_type(db, list_type):
    """Get a list of post `id`s."""
    sql = """SELECT post_id FROM hive_posts_status
              WHERE list_type = :list_type
            ORDER BY created_at DESC"""
    return await db.query_col(sql, list_type=list_type)


async def hide_pids_by_ids(db, ids):
    """Get a list of hided post `id`s."""
    sql = """SELECT post_id FROM hive_posts_status
              WHERE list_type = '1' 
              AND post_id IN :ids"""
    return await db.query_col(sql, ids=tuple(ids))


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

    # ignore community posts which were not reblogged
    skip = """
        SELECT id FROM hive_posts
         WHERE author = :account
           AND is_deleted = '0'
           AND depth = 0
           AND community_id IS NOT NULL
           AND id NOT IN (SELECT post_id FROM hive_reblogs
                           WHERE account = :account)"""

    # hide posts
    #hide = "SELECT post_id FROM hive_posts_status WHERE list_type = '1'"

    sql = """
        SELECT post_id
          FROM hive_feed_cache
         WHERE account_id = :account_id %s
           AND post_id NOT IN (%s)
      ORDER BY created_at DESC
         LIMIT :limit
    """ % (seek, skip)

    # alternate implementation -- may be more efficient
    #sql = """
    #    SELECT id
    #      FROM (
    #             SELECT id, author account, created_at FROM hive_posts
    #              WHERE depth = 0 AND is_deleted = '0' AND community_id IS NULL
    #              UNION ALL
    #             SELECT post_id id, account, created_at FROM hive_reblogs
    #           ) blog
    #     WHERE account = :account %s
    #  ORDER BY created_at DESC
    #     LIMIT :limit
    #""" % seek

    return await db.query_col(sql, account_id=account_id, account=account,
                              start_id=start_id, limit=limit)

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


async def pids_by_posts(db, account: str, start_permlink: str = '', limit: int = 20):
    """Get a list of post_ids representing top-level posts by an author."""
    seek = ''
    start_id = None
    if start_permlink:
        start_id = await _get_post_id(db, account, start_permlink)
        if not start_id:
            return []

        seek = "AND id <= :start_id"

    # hide posts
    #hide = "SELECT post_id FROM hive_posts_status WHERE list_type = '1'"

    # `depth` in ORDER BY is a no-op, but forces an ix3 index scan (see #189)
    sql = """
        SELECT id FROM hive_posts
         WHERE author = :account %s
           AND is_deleted = '0'
           AND depth = '0'
      ORDER BY id DESC
         LIMIT :limit
    """ % seek

    return await db.query_col(sql, account=account, start_id=start_id, limit=limit)

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
           AND is_deleted = '0'
           AND depth > 0
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

async def pids_by_payout(db, account: str, start_author: str = '',
                         start_permlink: str = '', limit: int = 20):
    """Get a list of post_ids for an author's blog."""
    seek = ''
    start_id = None
    if start_permlink:
        start_id = await _get_post_id(db, start_author, start_permlink)
        last = "(SELECT payout FROM hive_posts_cache WHERE post_id = :start_id)"
        seek = ("""AND (payout < %s OR (payout = %s AND post_id > :start_id))"""
                % (last, last))

    # hide posts
    #hide = "SELECT post_id FROM hive_posts_status WHERE list_type = '1'"

    sql = """
        SELECT post_id
          FROM hive_posts_cache
         WHERE author = :account
           AND is_paidout = '0' %s
      ORDER BY payout DESC, post_id
         LIMIT :limit
    """ % seek

    return await db.query_col(sql, account=account, start_id=start_id, limit=limit)
