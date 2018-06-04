"""Cursor-based pagination queries, mostly supporting condenser_api."""

from hive.db.methods import query_one, query_col, query_row, query_all

def _get_post_id(author, permlink):
    """Get post_id from hive db."""
    sql = "SELECT id FROM hive_posts WHERE author = :a AND permlink = :p"
    return query_one(sql, a=author, p=permlink)

def _get_account_id(name):
    """Get account id from hive db."""
    _id = query_one("SELECT id FROM hive_accounts WHERE name = :n", n=name)
    assert _id, "invalid account `%s`" % name
    return _id


def get_followers(account: str, start: str, state: int, limit: int):
    """Get a list of accounts following a given account."""
    account_id = _get_account_id(account)

    seek = ''
    if start:
        seek = """
          AND hf.created_at <= (
            SELECT created_at FROM hive_follows
             WHERE following = :account_id
               AND follower = %d
               AND state = :state)
        """ % _get_account_id(start)

    sql = """
        SELECT name FROM hive_follows hf
          JOIN hive_accounts ON hf.follower = id
         WHERE hf.following = :account_id
           AND state = :state %s
      ORDER BY hf.created_at DESC
         LIMIT :limit
    """ % seek

    return query_col(sql, account_id=account_id, state=state, limit=limit)


def get_following(account: str, start: str, state: int, limit: int):
    """Get a list of accounts followed by a given account."""
    account_id = _get_account_id(account)

    seek = ''
    if start:
        seek = """
          AND hf.created_at <= (
            SELECT created_at FROM hive_follows
             WHERE follower = :account_id
               AND following = %d
               AND state = :state)
        """ % _get_account_id(start)

    sql = """
        SELECT name FROM hive_follows hf
          JOIN hive_accounts ON hf.following = id
         WHERE hf.follower = :account_id
           AND state = :state %s
      ORDER BY hf.created_at DESC
         LIMIT :limit
    """ % seek

    return query_col(sql, account_id=account_id, state=state, limit=limit)


def get_follow_counts(account: str):
    """Return following/followers count for `account`."""
    sql = """SELECT following, followers
               FROM hive_accounts
              WHERE name = :account"""
    return dict(query_row(sql, account=account))


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
      GROUP BY post_id %s
      ORDER BY MIN(hive_feed_cache.created_at) DESC LIMIT :limit
    """ % seek

    return query_all(sql, account=account_id, limit=limit)


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
     ORDER BY created_at DESC
        LIMIT :limit
    """ % seek

    return query_col(sql, parent=parent_account, limit=limit)
