"""Cursor-based pagination queries, mostly supporting condenser_api."""

from datetime import datetime
from dateutil.relativedelta import relativedelta

from hive.utils.normalize import rep_to_raw
from hive.db.methods import query_one, query_col, query_row, query_all

def last_month():
    """Get the date 1 month ago."""
    return datetime.now() + relativedelta(months=-1)

def _get_post_id(author, permlink):
    """Get post_id from hive db."""
    sql = "SELECT id FROM hive_posts WHERE author = :a AND permlink = :p"
    return query_one(sql, a=author, p=permlink)

def _get_account_id(name):
    """Get account id from hive db."""
    assert name, 'no account name specified'
    _id = query_one("SELECT id FROM hive_accounts WHERE name = :n", n=name)
    assert _id, "account `%s` not found" % name
    return _id


def get_followers(account: str, start: str, state: int, limit: int):
    """Get a list of accounts following a given account."""
    account_id = _get_account_id(account)
    seek = "AND name >= :start" if start else ''

    sql = """
        SELECT name FROM hive_follows hf
     LEFT JOIN hive_accounts ON hf.follower = id
         WHERE hf.following = :account_id
           AND state = :state %s
      ORDER BY name ASC
         LIMIT :limit
    """ % seek

    return query_col(sql, account_id=account_id, start=start, state=state, limit=limit)


def get_following(account: str, start: str, state: int, limit: int):
    """Get a list of accounts followed by a given account."""
    account_id = _get_account_id(account)
    seek = "AND name >= :start" if start else ''

    sql = """
        SELECT name FROM hive_follows hf
     LEFT JOIN hive_accounts ON hf.following = id
         WHERE hf.follower = :account_id
           AND state = :state %s
      ORDER BY name ASC
         LIMIT :limit
    """ % seek

    return query_col(sql, account_id=account_id, start=start, state=state, limit=limit)


def get_follow_counts(account: str):
    """Return following/followers count for `account`."""
    sql = """SELECT following, followers
               FROM hive_accounts
              WHERE name = :account"""
    return dict(query_row(sql, account=account))


def get_reblogged_by(author: str, permlink: str):
    """Return all rebloggers of a post."""
    post_id = _get_post_id(author, permlink)
    assert post_id, "post not found"
    sql = """SELECT name FROM hive_accounts
               JOIN hive_feed_cache ON id = account_id
              WHERE post_id = :post_id"""
    names = query_col(sql, post_id=post_id)
    names.remove(author)
    return names


def get_account_reputations(account_lower_bound, limit):
    """Enumerate account reputations."""
    seek = ''
    if account_lower_bound:
        seek = "WHERE name >= :start"

    sql = """SELECT name, reputation
               FROM hive_accounts %s
           ORDER BY name
              LIMIT :limit""" % seek
    rows = query_all(sql, start=account_lower_bound, limit=limit)
    return [dict(name=name, reputation=rep_to_raw(rep)) for name, rep in rows]


def pids_by_query(sort, start_author, start_permlink, limit, tag):
    """Get a list of post_ids for a given posts query.

    `sort` can be trending, hot, new, promoted.
    """
    assert sort in ['trending', 'hot', 'created', 'promoted']

    col = ''
    where = []
    if sort == 'trending':
        col = 'sc_trend'
    elif sort == 'hot':
        col = 'sc_hot'
    elif sort == 'created':
        col = 'post_id'
        where.append('depth = 0')
    elif sort == 'promoted':
        col = 'promoted'
        where.append("is_paidout = '0'")
        where.append('promoted > 0')

    if tag:
        tagged_pids = "SELECT post_id FROM hive_post_tags WHERE tag = :tag"
        where.append("post_id IN (%s)" % tagged_pids)

    def _where(conditions):
        return 'WHERE ' + ' AND '.join(conditions) if conditions else ''

    start_id = None
    if start_permlink:
        start_id = _get_post_id(start_author, start_permlink)
        if not start_id:
            return []

        sql = ("SELECT %s FROM hive_posts_cache %s ORDER BY %s DESC LIMIT 1"
               % (col, _where([*where, "post_id = :start_id"]), col))
        where.append("%s <= (%s)" % (col, sql))

    sql = ("SELECT post_id FROM hive_posts_cache %s ORDER BY %s DESC LIMIT :limit"
           % (_where(where), col))

    return query_col(sql, tag=tag, start_id=start_id, limit=limit)


def pids_by_blog(account: str, start_author: str = '',
                 start_permlink: str = '', limit: int = 20):
    """Get a list of post_ids for an author's blog."""
    account_id = _get_account_id(account)

    seek = ''
    if start_permlink:
        start_id = _get_post_id(start_author, start_permlink)
        if not start_id:
            return []

        seek = """
          AND created_at <= (
            SELECT created_at
              FROM hive_feed_cache
             WHERE account_id = :account_id
               AND post_id = %d)
        """ % start_id

    sql = """
        SELECT post_id
          FROM hive_feed_cache
         WHERE account_id = :account_id %s
      ORDER BY created_at DESC
         LIMIT :limit
    """ % seek

    return query_col(sql, account_id=account_id, limit=limit)


def pids_by_blog_by_index(account: str, start_index: int, limit: int = 20):
    """Get post_ids for an author's blog (w/ reblogs), paged by index/limit.

    Examples:
    (acct, 2) = returns blog entries 0 up to 2 (3 oldest)
    (acct, 0) = returns all blog entries (limit 0 means return all?)
    (acct, 2, 1) = returns 1 post starting at idx 2
    (acct, 2, 3) = returns 3 posts: idxs (2,1,0)
    """


    sql = """
        SELECT post_id
          FROM hive_feed_cache
         WHERE account_id = :account_id
      ORDER BY created_at
         LIMIT :limit
        OFFSET :offset
    """

    account_id = _get_account_id(account)

    offset = start_index - limit + 1
    assert offset >= 0, 'start_index and limit combination is invalid'

    ids = query_col(sql, account_id=account_id, limit=limit, offset=offset)
    return list(reversed(ids))


def pids_by_blog_without_reblog(account: str, start_permlink: str = '', limit: int = 20):
    """Get a list of post_ids for an author's blog without reblogs."""

    seek = ''
    if start_permlink:
        start_id = _get_post_id(account, start_permlink)
        if not start_id:
            return []
        seek = "AND id <= %d" % start_id

    sql = """
        SELECT id
          FROM hive_posts
         WHERE author = :account %s
           AND is_deleted = '0'
           AND depth = 0
      ORDER BY id DESC
         LIMIT :limit
    """ % seek

    return query_col(sql, account=account, limit=limit)


def pids_by_feed_with_reblog(account: str, start_author: str = '',
                             start_permlink: str = '', limit: int = 20):
    """Get a list of [post_id, reblogged_by_str] for an account's feed."""
    account_id = _get_account_id(account)

    seek = ''
    if start_permlink:
        start_id = _get_post_id(start_author, start_permlink)
        if not start_id:
            return []

        seek = """
          HAVING MIN(hive_feed_cache.created_at) <= (
            SELECT MIN(created_at) FROM hive_feed_cache WHERE post_id = %d
               AND account_id IN (SELECT following FROM hive_follows
                                  WHERE follower = :account AND state = 1))
        """ % start_id

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

    return query_all(sql, account=account_id, limit=limit, cutoff=last_month())


def pids_by_account_comments(account: str, start_permlink: str = '', limit: int = 20):
    """Get a list of post_ids representing comments by an author."""
    seek = ''
    if start_permlink:
        start_id = _get_post_id(account, start_permlink)
        if not start_id:
            return []

        seek = """
          AND created_at <= (SELECT created_at FROM hive_posts WHERE id = %d)
        """ % start_id

    sql = """
        SELECT id FROM hive_posts
         WHERE author = :account %s
           AND depth > 0
           AND is_deleted = '0'
      ORDER BY created_at DESC
         LIMIT :limit
    """ % seek

    return query_col(sql, account=account, limit=limit)


def pids_by_replies_to_account(start_author: str, start_permlink: str = '', limit: int = 20):
    """Get a list of post_ids representing replies to an author.

    To get the first page of results, specify `start_author` as the
    account being replied to. For successive pages, provide the
    last loaded reply's author/permlink.
    """
    seek = ''
    if start_permlink:
        sql = """
          SELECT parent.author,
                 child.created_at
            FROM hive_posts child
            JOIN hive_posts parent
              ON child.parent_id = parent.id
           WHERE child.author = :author
             AND child.permlink = :permlink
        """

        row = query_row(sql, author=start_author, permlink=start_permlink)
        if not row:
            return []

        parent_account = row[0]
        seek = "AND created_at <= '%s'" % row[1]
    else:
        parent_account = start_author

    sql = """
       SELECT id FROM hive_posts
        WHERE parent_id IN (SELECT id FROM hive_posts WHERE author = :parent) %s
          AND is_deleted = '0'
     ORDER BY created_at DESC
        LIMIT :limit
    """ % seek

    return query_col(sql, parent=parent_account, limit=limit)
