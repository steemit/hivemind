import time

from decimal import Decimal
from hive.db.methods import query, query_one, query_col, query_row, query_all


async def db_head_state():
    sql = ("SELECT num,created_at,extract(epoch from created_at) ts "
           "FROM hive_blocks ORDER BY num DESC LIMIT 1")
    row = query_row(sql)
    return dict(db_head_block=row['num'],
                db_head_time=str(row['created_at']),
                db_head_age=int(time.time() - row['ts']))


# follow methods
# --------------

async def get_followers(account: str, skip: int, limit: int):
    account_id = _get_account_id(account)
    sql = """
      SELECT name FROM hive_follows hf
        JOIN hive_accounts ON hf.follower = id
       WHERE hf.following = :account_id AND state = 1
    ORDER BY hf.created_at DESC LIMIT :limit OFFSET :skip
    """
    return query_col(sql, account_id=account_id, skip=skip, limit=limit)


async def get_following(account: str, skip: int, limit: int):
    account_id = _get_account_id(account)
    sql = """
      SELECT name FROM hive_follows hf
        JOIN hive_accounts ON hf.following = id
       WHERE hf.follower = :account_id AND state = 1
    ORDER BY hf.created_at DESC LIMIT :limit OFFSET :skip
    """
    res = query(sql, account_id=account_id, skip=int(skip), limit=int(limit))
    return [[r[0], str(r[1])] for r in res.fetchall()]


async def get_follow_count(account: str):
    sql = "SELECT name, following, followers FROM hive_accounts WHERE name = :n"
    return query_row(sql, n=account)


# stats methods
# -------------

# all completed payouts
async def payouts_total():
    # memoized historical sum. To update:
    #  SELECT SUM(payout) FROM hive_posts_cache
    #  WHERE is_paidout = 1 AND payout_at <= precalc_date
    precalc_date = '2017-08-30 00:00:00'
    precalc_sum = Decimal('19358777.541')

    # sum all payouts since `precalc_date`
    sql = """
      SELECT SUM(payout) FROM hive_posts_cache
      WHERE is_paidout = '1' AND payout_at > '%s'
    """ % (precalc_date)

    return float(precalc_sum + query_one(sql)) #TODO: decimal

# sum of completed payouts last 24 hrs
async def payouts_last_24h():
    sql = """
      SELECT SUM(payout) FROM hive_posts_cache WHERE is_paidout = '1'
      AND payout_at > (NOW() AT TIME ZONE 'utc') - INTERVAL '24 HOUR'
    """
    return float(query_one(sql)) # TODO: decimal


# discussion apis
# ---------------

# builds SQL query to pull a list of posts for any sort order or tag
# sort can be: trending hot new promoted
async def get_discussions_by_sort_and_tag(sort, tag, skip, limit, context=None):
    if skip > 5000:
        raise Exception("cannot skip {} results".format(skip))
    if limit > 100:
        raise Exception("cannot limit {} results".format(limit))

    order = ''
    where = []

    if sort == 'trending':
        order = 'sc_trend DESC'
    elif sort == 'hot':
        order = 'sc_hot DESC'
    elif sort == 'new':
        order = 'post_id DESC'
        where.append('depth = 0')
    elif sort == 'promoted':
        order = 'promoted DESC'
        where.append('is_paidout = 0')
        where.append('promoted > 0')
    else:
        raise Exception("unknown sort order {}".format(sort))

    if tag:
        where.append('post_id IN (SELECT post_id FROM hive_post_tags WHERE tag = :tag)')

    if where:
        where = 'WHERE ' + ' AND '.join(where)
    else:
        where = ''

    sql = "SELECT post_id FROM hive_posts_cache %s ORDER BY %s LIMIT :limit OFFSET :skip" % (where, order)
    ids = [r[0] for r in query(sql, tag=tag, limit=limit, skip=skip).fetchall()]
    return _get_posts(ids, context)


# returns "homepage" feed for specified account
async def get_user_feed(account: str, skip: int, limit: int, context: str = None):
    account_id = _get_account_id(account)
    sql = """
      SELECT post_id, string_agg(name, ',') accounts
        FROM hive_feed_cache
        JOIN hive_follows ON account_id = hive_follows.following AND state = 1
        JOIN hive_accounts ON hive_follows.following = hive_accounts.id
       WHERE hive_follows.follower = :account
    GROUP BY post_id
    ORDER BY MIN(hive_feed_cache.created_at) DESC LIMIT :limit OFFSET :skip
    """
    res = query_all(sql, account=account_id, skip=skip, limit=limit)
    posts = _get_posts([r[0] for r in res], context)

    # Merge reblogged_by data into result set
    accts = dict(res)
    for post in posts:
        rby = set(accts[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts


# returns a blog feed (posts and reblogs from the specified account)
async def get_blog_feed(account: str, skip: int, limit: int, context: str = None):
    account_id = _get_account_id(account)
    sql = ("SELECT post_id FROM hive_feed_cache WHERE account_id = :account_id "
           "ORDER BY created_at DESC LIMIT :limit OFFSET :skip")
    post_ids = query_col(sql, account_id=account_id, skip=skip, limit=limit)
    return _get_posts(post_ids, context)


async def get_related_posts(account: str, permlink: str):
    sql = """
      SELECT p2.id
        FROM hive_posts p1
        JOIN hive_posts p2 ON p1.category = p2.category
        JOIN hive_posts_cache pc ON p2.id = pc.post_id
       WHERE p1.author = :a AND p1.permlink = :p
         AND sc_trend > :t AND p1.id != p2.id
    ORDER BY sc_trend DESC LIMIT 5
    """
    thresh = time.time() / 480000
    post_ids = query_col(sql, a=account, p=permlink, t=thresh)
    return _get_posts(post_ids)


# ---


def _get_account_id(name):
    return query_one("SELECT id FROM hive_accounts WHERE name = :n", n=name)


# given an array of post ids, returns full metadata in the same order
def _get_posts(ids, context=None):
    sql = """
    SELECT post_id, author, permlink, title, preview, img_url, payout,
           promoted, created_at, payout_at, is_nsfw, rshares, votes, json
      FROM hive_posts_cache WHERE post_id IN :ids
    """

    reblogged_ids = []
    if context:
        reblogged_ids = query_col("SELECT post_id FROM hive_reblogs WHERE account = :a AND post_id IN :ids", a=context, ids=tuple(ids))

    # key by id so we can return sorted by input order
    posts_by_id = {}
    for row in query(sql, ids=tuple(ids)).fetchall():
        obj = dict(row)

        if context:
            voters = [csa.split(",")[0] for csa in obj['votes'].split("\n")]
            obj['user_state'] = {
                'reblogged': row['post_id'] in reblogged_ids,
                'voted': context in voters
            }

        # TODO: Object of type 'Decimal' is not JSON serializable
        obj['payout'] = float(obj['payout'])
        obj['promoted'] = float(obj['promoted'])

        # TODO: Object of type 'datetime' is not JSON serializable
        obj['created_at'] = str(obj['created_at'])
        obj['payout_at'] = str(obj['payout_at'])

        obj.pop('votes') # temp
        obj.pop('json')  # temp
        posts_by_id[row['post_id']] = obj

    # in rare cases of cache inconsistency, recover and warn
    missed = set(ids) - posts_by_id.keys()
    if missed:
        print("WARNING: _get_posts do not exist in cache: {}".format(missed))
        for _id in missed:
            ids.remove(_id)

    return [posts_by_id[_id] for _id in ids]
