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


async def pids_by_ranked(db, sort, start_author, start_permlink, limit, tag):
    """Get a list of post_ids for a given posts query.

    `sort` can be trending, hot, created, promoted, payout, or payout_comments.
    """
    # pylint: disable=too-many-arguments,bad-whitespace,line-too-long
    assert sort in ['trending', 'hot', 'created', 'promoted',
                    'payout', 'payout_comments']

    params = {             # field      pending posts   comment promoted    todo        community
        'trending':        ('sc_trend', True,   False,  False,  False),   # posts=True  pending=False
        'hot':             ('sc_hot',   True,   False,  False,  False),   # posts=True  pending=False
        'created':         ('post_id',  False,  True,   False,  False),
        'promoted':        ('promoted', True,   False,  False,  True),    # posts=True
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

    # filter by community, category, or tag
    if tag:
        #if tag[:5] == 'hive-'
        #    cid = get_community_id(tag)
        #    where.append('community_id = :cid')
        if sort in ['payout', 'payout_comments']:
            where.append('category = :tag')
        else:
            if tag[:5] == 'hive-':
                where.append('category = :tag')
                if sort in ('trending', 'hot'):
                    where.append('depth = 0')
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
