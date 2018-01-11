import json

from aiocache import cached
from hive.db.methods import query_one, query_col, query_row, query_all
from hive.indexer.steem_client import get_adapter

# e.g. {"id":0,"jsonrpc":"2.0","method":"call",
#       "params":["database_api","get_state",["trending"]]}
async def call(api, method, params):
    # pylint: disable=line-too-long, protected-access, too-many-return-statements, too-many-branches
    if method == 'get_followers':
        return await get_followers(params[0], params[1], params[2], params[3])
    elif method == 'get_following':
        return await get_following(params[0], params[1], params[2], params[3])
    elif method == 'get_follow_count':
        return await get_follow_count(params[0])
    elif method == 'get_discussions_by_trending':
        return await get_discussions_by_trending(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'], params[0]['tag'])
    elif method == 'get_discussions_by_hot':
        return await get_discussions_by_hot(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'], params[0]['tag'])
    elif method == 'get_discussions_by_promoted':
        return await get_discussions_by_promoted(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'], params[0]['tag'])
    elif method == 'get_discussions_by_created':
        return await get_discussions_by_created(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'], params[0]['tag'])
    elif method == 'get_discussions_by_blog':
        return await get_discussions_by_blog(params[0]['tag'], params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'])
    elif method == 'get_discussions_by_feed':
        return await get_discussions_by_feed(params[0]['tag'], params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'])
    elif method == 'get_discussions_by_comments':
        return await get_discussions_by_comments(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'])
    elif method == 'get_replies_by_last_update':
        return await get_replies_by_last_update(params[0], params[1], params[2])
    elif method == 'get_content':
        return await get_content(params[0], params[1]) # after submit vote/post
    elif method == 'get_content_replies':
        return await get_content_replies(params[0], params[1])
    elif method == 'get_state':
        return await get_state(params[0])

    # passthrough -- TESTING ONLY!
    if method == 'get_dynamic_global_properties':
        return get_adapter()._gdgp() # condenser only uses total_vesting_fund_steem, total_vesting_shares, sbd_interest_rate
    elif method == 'get_accounts':
        return get_adapter().get_accounts(params[0])
    elif method == 'get_open_orders':
        return get_adapter()._client.exec('get_open_orders', params[0])
    elif method == 'get_block':
        return get_adapter()._client.exec('get_block', params[0])
    elif method == 'broadcast_transaction_synchronous':
        return get_adapter()._client.exec('broadcast_transaction_synchronous', params[0], api='network_broadcast_api')
    elif method == 'get_savings_withdraw_to':
        return get_adapter()._client.exec('get_savings_withdraw_to', params[0])
    elif method == 'get_savings_withdraw_from':
        return get_adapter()._client.exec('get_savings_withdraw_from', params[0])

    raise Exception("unknown method: {}.{}({})".format(api, method, params))


async def get_followers(account: str, start: str, follow_type: str, limit: int):
    limit = _validate_limit(limit, 1000)
    state = _follow_type_to_int(follow_type)
    account_id = _get_account_id(account)
    seek = ''

    if start:
        seek = """
          AND hf.created_at <= (
            SELECT created_at FROM hive_follows
             WHERE following = :account_id AND follower = %d AND state = :state)
        """ % _get_account_id(start)

    sql = """
        SELECT name FROM hive_follows hf
          JOIN hive_accounts ON hf.follower = id
         WHERE hf.following = :account_id AND state = :state %s
      ORDER BY hf.created_at DESC LIMIT :limit
    """ % seek

    res = query_col(sql, account_id=account_id, state=state, limit=int(limit))
    return [dict(follower=r, following=account, what=[follow_type])
            for r in res]


async def get_following(account: str, start: str, follow_type: str, limit: int):
    limit = _validate_limit(limit, 1000)
    state = _follow_type_to_int(follow_type)
    account_id = _get_account_id(account)
    seek = ''

    if start:
        seek = """
          AND hf.created_at <= (
            SELECT created_at FROM hive_follows
             WHERE follower = :account_id AND following = %d AND state = :state)
        """ % _get_account_id(start)

    sql = """
        SELECT name FROM hive_follows hf
          JOIN hive_accounts ON hf.following = id
         WHERE hf.follower = :account_id AND state = :state %s
      ORDER BY hf.created_at DESC LIMIT :limit
    """ % seek

    res = query_col(sql, account_id=account_id, state=state, limit=int(limit))
    return [dict(follower=account, following=r, what=[follow_type])
            for r in res]


async def get_follow_count(account: str):
    sql = """
        SELECT name as account,
               following as following_count,
               followers as follower_count
          FROM hive_accounts WHERE name = :n
    """
    return dict(query_row(sql, n=account))


async def get_discussions_by_trending(start_author: str, start_permlink: str = '',
                                      limit: int = 20, tag: str = None):
    return _get_discussions('trending', start_author, start_permlink, limit, tag)

async def get_discussions_by_hot(start_author: str, start_permlink: str = '',
                                 limit: int = 20, tag: str = None):
    return _get_discussions('hot', start_author, start_permlink, limit, tag)

async def get_discussions_by_promoted(start_author: str, start_permlink: str = '',
                                      limit: int = 20, tag: str = None):
    return _get_discussions('promoted', start_author, start_permlink, limit, tag)

async def get_discussions_by_created(start_author: str, start_permlink: str = '',
                                     limit: int = 20, tag: str = None):
    return _get_discussions('created', start_author, start_permlink, limit, tag)


# author blog
async def get_discussions_by_blog(tag: str, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20):
    limit = _validate_limit(limit, 20)
    account_id = _get_account_id(tag)
    seek = ''

    if start_permlink:
        seek = """
          AND created_at <= (
            SELECT created_at FROM hive_feed_cache
             WHERE account_id = :account_id AND post_id = %d)
        """ % _get_post_id(start_author, start_permlink)

    sql = """
        SELECT post_id FROM hive_feed_cache WHERE account_id = :account_id %s
      ORDER BY created_at DESC LIMIT :limit
    """ % seek

    ids = query_col(sql, account_id=account_id, limit=limit)
    return _get_posts(ids)


# author feed
async def get_discussions_by_feed(tag: str, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20):
    limit = _validate_limit(limit, 20)
    account_id = _get_account_id(tag)
    seek = ''

    if start_permlink:
        seek = """
          HAVING MIN(hive_feed_cache.created_at) <= (
            SELECT MIN(created_at) FROM hive_feed_cache WHERE post_id = %d
               AND account_id IN (SELECT following FROM hive_follows
                                  WHERE follower = :account AND state = 1))
        """ % _get_post_id(start_author, start_permlink)

    sql = """
        SELECT post_id, string_agg(name, ',') accounts
          FROM hive_feed_cache
          JOIN hive_follows ON account_id = hive_follows.following AND state = 1
          JOIN hive_accounts ON hive_follows.following = hive_accounts.id
         WHERE hive_follows.follower = :account
      GROUP BY post_id %s
      ORDER BY MIN(hive_feed_cache.created_at) DESC LIMIT :limit
    """ % seek

    res = query_all(sql, account=account_id, limit=limit)
    posts = _get_posts([r[0] for r in res])

    # Merge reblogged_by data into result set
    accts = dict(res)
    for post in posts:
        rby = set(accts[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts


# author comments
async def get_discussions_by_comments(start_author: str, start_permlink: str = '', limit: int = 20):
    limit = _validate_limit(limit, 20)
    seek = ''

    if start_permlink:
        seek = """
          AND created_at <= (SELECT created_at FROM hive_posts WHERE id = %d)
        """ % _get_post_id(start_author, start_permlink)

    sql = """
        SELECT id FROM hive_posts WHERE author = :account %s AND depth > 0
      ORDER BY created_at DESC LIMIT :limit
    """ % seek

    ids = query_col(sql, account=start_author, limit=limit)
    return _get_posts(ids)


# author replies
async def get_replies_by_last_update(start_author: str, start_permlink: str = '', limit: int = 20):
    limit = _validate_limit(limit, 50)
    parent = start_author
    seek = ''

    if start_permlink:
        parent, start_date = query_row("""
          SELECT p.author, c.created_at
            FROM hive_posts c
            JOIN hive_posts p ON c.parent_id = p.id
           WHERE c.author = :a AND c.permlink = :p
        """, a=start_author, p=start_permlink)
        seek = "AND created_at <= '%s'" % start_date

    sql = """
       SELECT id FROM hive_posts
        WHERE parent_id IN (SELECT id FROM hive_posts WHERE author = :parent) %s
     ORDER BY created_at DESC LIMIT :limit
    """ % seek

    ids = query_col(sql, parent=parent, limit=limit)
    return _get_posts(ids)


# https://github.com/steemit/steem/blob/06e67bd4aea73391123eca99e1a22a8612b0c47e/libraries/app/database_api.cpp#L1937
async def get_state(path: str):
    # pylint: disable=too-many-locals, too-many-branches, too-many-statements
    if path[0] == '/':
        path = path[1:]
    if not path:
        path = 'trending'
    part = path.split('/')
    if len(part) > 3:
        raise Exception("invalid path %s" % path)
    while len(part) < 3:
        part.append('')

    state = {}
    state['current_route'] = path
    state['props'] = _get_props_lite()
    state['tags'] = {}
    state['tag_idx'] = {}
    state['tag_idx']['trending'] = []
    state['content'] = {}
    state['accounts'] = {}
    state['discussion_idx'] = {"": {}}
    state['feed_price'] = _get_feed_price()
    state1 = "{}".format(state)

    # account tabs (feed, blog, comments, replies)
    if part[0] and part[0][0] == '@':
        if not part[1]:
            part[1] = 'blog'
        if part[1] == 'transfers':
            raise Exception("transfers API not served by hive")
        if part[2]:
            raise Exception("unexpected account path part[2] %s" % path)

        account = part[0][1:]

        keys = {'recent-replies': 'recent_replies',
                'comments': 'comments',
                'blog': 'blog',
                'feed': 'feed'}

        if part[1] not in keys:
            raise Exception("invalid account path %s" % path)
        key = keys[part[1]]

        # TODO: use _load_accounts([account])? Examine issue w/ login
        account_obj = get_adapter().get_accounts([account])[0]
        state['accounts'][account] = account_obj

        if key == 'recent_replies':
            posts = await get_replies_by_last_update(account, "", 20)
        elif key == 'comments':
            posts = await get_discussions_by_comments(account, "", 20)
        elif key == 'blog':
            posts = await get_discussions_by_blog(account, "", "", 20)
        elif key == 'feed':
            posts = await get_discussions_by_feed(account, "", "", 20)

        state['accounts'][account][key] = []
        for post in posts:
            ref = post['author'] + '/' + post['permlink']
            state['accounts'][account][key].append(ref)
            state['content'][ref] = post

    # discussion thread
    elif part[1] and part[1][0] == '@':
        author = part[1][1:]
        permlink = part[2]
        state['content'] = _load_discussion_recursive(author, permlink)
        accounts = set(map(lambda p: p['author'], state['content'].values()))
        state['accounts'] = {a['name']: a for a in _load_accounts(accounts)}

    # trending/etc pages
    elif part[0] in ['trending', 'promoted', 'hot', 'created']:
        if part[2]:
            raise Exception("unexpected discussion path part[2] %s" % path)
        sort = part[0]
        tag = part[1].lower()
        posts = _get_discussions(sort, '', '', 20, tag)
        state['discussion_idx'][tag] = {sort: []}
        for post in posts:
            ref = post['author'] + '/' + post['permlink']
            state['content'][ref] = post
            state['discussion_idx'][tag][sort].append(ref)
        state['tag_idx']['trending'] = await _get_top_trending_tags()

    # witness list
    elif part[0] == 'witnesses':
        raise Exception("not implemented")

    # tag "explorer"
    elif part[0] == "tags":
        state['tag_idx']['trending'] = []
        tags = await _get_trending_tags()
        for tag in tags:
            state['tag_idx']['trending'].append(tag['name'])
            state['tags'][tag['name']] = tag

    else:
        raise Exception("unknown path {}".format(path))

    # (debug; should not happen) if state did not change, complain
    state2 = "{}".format(state)
    if state1 == state2:
        raise Exception("unrecognized path `{}`" % path)

    return state


async def get_content(author: str, permlink: str):
    post_id = _get_post_id(author, permlink)
    return _get_posts([post_id])[0]


async def get_content_replies(parent: str, parent_permlink: str):
    post_id = _get_post_id(parent, parent_permlink)
    post_ids = query_col("SELECT id FROM hive_posts WHERE parent_id = %d" % post_id)
    return _get_posts(post_ids)

@cached(ttl=3600)
async def _get_top_trending_tags():
    sql = """
        SELECT category FROM hive_posts_cache WHERE is_paidout = '0'
      GROUP BY category ORDER BY SUM(payout) DESC LIMIT 50
    """
    return query_col(sql)

@cached(ttl=3600)
async def _get_trending_tags():
    sql = """
      SELECT category,
             COUNT(*) AS total_posts,
             SUM(CASE WHEN depth = 0 THEN 1 ELSE 0 END) AS top_posts,
             SUM(payout) AS total_payouts
        FROM hive_posts_cache
       WHERE is_paidout = '0'
    GROUP BY category
    ORDER BY SUM(payout) DESC
       LIMIT 250
    """
    out = []
    for row in query_all(sql):
        out.append({
            'comments': row['total_posts'] - row['top_posts'],
            'name': row['category'],
            'top_posts': row['top_posts'],
            'total_payouts': "%.3f SBD" % row['total_payouts']})

    return out

def _get_props_lite():
    # TODO: trim this response; really only need: total_vesting_fund_steem,
    #   total_vesting_shares, sbd_interest_rate
    return json.loads(query_one("SELECT dgpo FROM hive_state"))

def _get_feed_price():
    price = query_one("SELECT usd_per_steem FROM hive_state")
    return {"base": "%.3f SBD" % price, "quote": "1.000 STEEM"}

def _load_discussion_recursive(author, permlink):
    post_id = _get_post_id(author, permlink)
    return _load_posts_recursive([post_id])

def _load_posts_recursive(post_ids):
    posts = _get_posts(post_ids)

    out = {}
    for post, post_id in zip(posts, post_ids):
        out[post['author'] + '/' + post['permlink']] = post

        child_ids = query_col("SELECT id FROM hive_posts WHERE parent_id = %d" % post_id)
        if child_ids:
            children = _load_posts_recursive(child_ids)
            post['replies'] = list(children.keys())
            out = {**out, **children}

    return out

# sort can be trending, hot, new, promoted
def _get_discussions(sort, start_author, start_permlink, limit, tag):
    limit = _validate_limit(limit, 20)

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
    else:
        raise Exception("unknown sort order {}".format(sort))

    if tag:
        tagged_posts = "SELECT post_id FROM hive_post_tags WHERE tag = :tag"
        where.append("post_id IN (%s)" % tagged_posts)

    start_id = None
    if start_permlink:
        start_id = _get_post_id(start_author, start_permlink)
        sql = ("SELECT %s FROM hive_posts_cache %s ORDER BY %s DESC LIMIT 1"
               % (col, _where([*where, "post_id = :start_id"]), col))
        where.append("%s <= (%s)" % (col, sql))

    sql = ("SELECT post_id FROM hive_posts_cache %s ORDER BY %s DESC LIMIT :limit"
           % (_where(where), col))
    ids = query_col(sql, tag=tag, start_id=start_id, limit=limit)
    return _get_posts(ids)

def _load_accounts(names):
    sql = """SELECT id, name, display_name, about, reputation
               FROM hive_accounts WHERE name IN :names"""
    rows = query_all(sql, names=tuple(names))
    return [_condenser_account(row) for row in rows]

def _condenser_account(row):
    return {
        'name': row['name'],
        'reputation': _rep_to_raw(row['reputation']),
        'json_metadata': json.dumps({
            'profile': {'name': row['display_name'], 'about': row['about']}})}

def _where(conditions):
    if not conditions:
        return ''
    return 'WHERE ' + ' AND '.join(conditions)

def _validate_limit(limit, ubound=100):
    limit = int(limit)
    if limit <= 0:
        raise Exception("invalid limit")
    if limit > ubound:
        raise Exception("limit exceeded")
    return limit

def _follow_type_to_int(follow_type: str):
    if follow_type not in ['blog', 'ignore']:
        raise Exception("Invalid follow_type")
    return 1 if follow_type == 'blog' else 2

def _get_post_id(author, permlink):
    sql = "SELECT id FROM hive_posts WHERE author = :a AND permlink = :p"
    _id = query_one(sql, a=author, p=permlink)
    if not _id:
        raise Exception("post not found: %s/%s" % (author, permlink))
    return _id

def _get_account_id(name):
    _id = query_one("SELECT id FROM hive_accounts WHERE name = :n", n=name)
    if not _id:
        raise Exception("invalid account `%s`" % name)
    return _id

# given an array of post ids, returns full metadata in the same order
def _get_posts(ids):
    if not ids:
        raise Exception("no ids provided")

    sql = """
    SELECT post_id, author, permlink, title, body, promoted, payout, created_at,
           payout_at, is_paidout, rshares, raw_json, category, depth, json,
           children, votes, author_rep,

           preview, img_url, is_nsfw
      FROM hive_posts_cache WHERE post_id IN :ids
    """

    # key by id so we can return sorted by input order
    posts_by_id = {}
    for row in query_all(sql, ids=tuple(ids)):
        row = dict(row)
        post = _condenser_post_object(row)
        posts_by_id[row['post_id']] = post

    # in rare cases of cache inconsistency, recover and warn
    missed = set(ids) - posts_by_id.keys()
    if missed:
        print("WARNING: get_posts do not exist in cache: {}".format(missed))
        for _id in missed:
            ids.remove(_id)

    return [posts_by_id[_id] for _id in ids]


# given a hive_posts_cache row, create a condenser-api style post object
def _condenser_post_object(row):
    paid = row['is_paidout']

    post = {}
    post['post_id'] = row['post_id']
    post['author'] = row['author']
    post['permlink'] = row['permlink']
    post['category'] = row['category']
    post['parent_permlink'] = ''
    post['parent_author'] = ''

    post['title'] = row['title']
    post['body'] = row['body']
    post['json_metadata'] = row['json']

    post['created'] = _json_date(row['created_at'])
    post['depth'] = row['depth']
    post['children'] = row['children']
    post['net_rshares'] = row['rshares']

    post['cashout_time'] = _json_date(None if paid else row['payout_at'])
    post['total_payout_value'] = _amount(row['payout'] if paid else 0)
    post['curator_payout_value'] = _amount(0)
    post['pending_payout_value'] = _amount(0 if paid else row['payout'])
    post['promoted'] = "%.3f SBD" % row['promoted']

    post['replies'] = []
    post['body_length'] = len(row['body'])
    post['active_votes'] = _hydrate_active_votes(row['votes'])
    post['author_reputation'] = _rep_to_raw(row['author_rep'])

    # import fields from legacy object
    assert row['raw_json']
    assert len(row['raw_json']) > 32
    raw_json = json.loads(row['raw_json'])

    if row['depth'] > 0:
        post['parent_permlink'] = raw_json['parent_permlink']
        post['parent_author'] = raw_json['parent_author']

    post['root_title'] = raw_json['root_title']
    post['max_accepted_payout'] = raw_json['max_accepted_payout']
    post['percent_steem_dollars'] = raw_json['percent_steem_dollars']
    post['url'] = raw_json['url']

    # not used by condenser, but may be useful
    #post['net_votes'] = raw_json['net_votes']
    #post['allow_replies'] = raw_json['allow_replies']
    #post['allow_votes'] = raw_json['allow_votes']
    #post['allow_curation_rewards'] = raw_json['allow_curation_rewards']
    #post['beneficiaries'] = raw_json['benificiaries']

    return post

def _amount(amount, asset='SBD'):
    if asset == 'SBD':
        return "%.3f SBD" % amount
    raise Exception("unexpected %s" % asset)

def _hydrate_active_votes(vote_csv):
    if not vote_csv:
        return []
    cols = 'voter,rshares,percent,reputation'.split(',')
    votes = vote_csv.split("\n")
    return [dict(zip(cols, line.split(','))) for line in votes]

def _json_date(date=None):
    if not date:
        return '1969-12-31T23:59:59'
    return 'T'.join(str(date).split(' '))

def _rep_to_raw(rep):
    if not isinstance(rep, (str, float, int)):
        return 0
    rep = float(rep) - 25
    rep = rep / 9
    rep = rep + 9
    sign = 1 if rep >= 0 else -1
    return int(sign * pow(10, rep))


if __name__ == '__main__':
    print(_load_discussion_recursive('roadscape', 'hello-world'))
