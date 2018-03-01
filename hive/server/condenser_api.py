"""Steemd/condenser_api compatibility layer API methods."""

import json

from aiocache import cached
from hive.db.methods import query_one, query_col, query_row, query_all
from hive.steem.steem_client import SteemClient
import hive.server.cursor as cursor

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
    steemd = SteemClient.instance()
    if method == 'get_dynamic_global_properties':
        return steemd._gdgp() # condenser only uses total_vesting_fund_steem, total_vesting_shares, sbd_interest_rate
    elif method == 'get_accounts':
        return steemd.get_accounts(params[0])
    elif method == 'get_open_orders':
        return steemd._client.exec('get_open_orders', params[0])
    elif method == 'get_block':
        return steemd._client.exec('get_block', params[0])
    elif method == 'broadcast_transaction_synchronous':
        return steemd._client.exec('broadcast_transaction_synchronous', params[0], api='network_broadcast_api')
    elif method == 'get_savings_withdraw_to':
        return steemd._client.exec('get_savings_withdraw_to', params[0])
    elif method == 'get_savings_withdraw_from':
        return steemd._client.exec('get_savings_withdraw_from', params[0])

    raise Exception("unknown method: {}.{}({})".format(api, method, params))


async def get_followers(account: str, start: str, follow_type: str, limit: int):
    limit = _validate_limit(limit, 1000)
    state = _follow_type_to_int(follow_type)
    followers = cursor.get_followers(account, start, state, limit)
    return [dict(follower=name, following=account, what=[follow_type])
            for name in followers]

async def get_following(account: str, start: str, follow_type: str, limit: int):
    limit = _validate_limit(limit, 1000)
    state = _follow_type_to_int(follow_type)
    following = cursor.get_following(account, start, state, limit)
    return [dict(follower=account, following=name, what=[follow_type])
            for name in following]

async def get_follow_count(account: str):
    count = cursor.get_follow_counts(account)
    return dict(account=account,
                following_count=count['following'],
                follower_count=count['followers'])


async def get_discussions_by_trending(start_author: str, start_permlink: str = '',
                                      limit: int = 20, tag: str = None):
    limit = _validate_limit(limit, 20)
    ids = cursor.pids_by_query(
        'trending',
        start_author,
        start_permlink,
        limit,
        tag)
    return _get_posts(ids)


async def get_discussions_by_hot(start_author: str, start_permlink: str = '',
                                 limit: int = 20, tag: str = None):
    limit = _validate_limit(limit, 20)
    ids = cursor.pids_by_query(
        'hot',
        start_author,
        start_permlink,
        limit,
        tag)
    return _get_posts(ids)


async def get_discussions_by_promoted(start_author: str, start_permlink: str = '',
                                      limit: int = 20, tag: str = None):
    limit = _validate_limit(limit, 20)
    ids = cursor.pids_by_query(
        'promoted',
        start_author,
        start_permlink,
        limit,
        tag)
    return _get_posts(ids)


async def get_discussions_by_created(start_author: str, start_permlink: str = '',
                                     limit: int = 20, tag: str = None):
    limit = _validate_limit(limit, 20)
    ids = cursor.pids_by_query(
        'created',
        start_author,
        start_permlink,
        limit,
        tag)
    return _get_posts(ids)


async def get_discussions_by_blog(tag: str, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20):
    """Retrieve account's blog."""
    limit = _validate_limit(limit, 20)
    ids = cursor.pids_by_blog(
        tag,
        start_author,
        start_permlink,
        limit)
    return _get_posts(ids)


async def get_discussions_by_feed(tag: str, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20):
    """Retrieve account's feed."""
    limit = _validate_limit(limit, 20)
    res = cursor.pids_by_feed_with_reblog(
        tag,
        start_author,
        start_permlink,
        limit)

    reblogged_by = dict(res)
    posts = _get_posts([r[0] for r in res])

    # Merge reblogged_by data into result set
    for post in posts:
        rby = set(reblogged_by[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts


async def get_discussions_by_comments(start_author: str, start_permlink: str = '', limit: int = 20):
    """Get comments by author."""
    ids = cursor.pids_by_account_comments(
        start_author,
        start_permlink,
        _validate_limit(limit, 20))
    return _get_posts(ids)


async def get_replies_by_last_update(start_author: str, start_permlink: str = '', limit: int = 20):
    """Get replies to author."""
    ids = cursor.pids_by_replies_to_account(
        start_author,
        start_permlink,
        _validate_limit(limit, 50))
    return _get_posts(ids)


async def get_state(path: str):
    """`get_state` reimplementation.

    See: https://github.com/steemit/steem/blob/06e67bd4aea73391123eca99e1a22a8612b0c47e/libraries/app/database_api.cpp#L1937
    """
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
        account_obj = SteemClient.instance().get_accounts([account])[0]
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
        posts = cursor.pids_by_query(sort, '', '', 20, tag)
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
    """Get a single post object."""
    post_id = _get_post_id(author, permlink)
    return _get_posts([post_id])[0]


async def get_content_replies(parent: str, parent_permlink: str):
    """Get a list of post objects based on parent."""
    post_id = _get_post_id(parent, parent_permlink)
    post_ids = query_col("SELECT id FROM hive_posts WHERE parent_id = %d" % post_id)
    return _get_posts(post_ids)

@cached(ttl=3600)
async def _get_top_trending_tags():
    """Get top 50 trending tags among pending posts."""
    sql = """
        SELECT category FROM hive_posts_cache WHERE is_paidout = '0'
      GROUP BY category ORDER BY SUM(payout) DESC LIMIT 50
    """
    return query_col(sql)

@cached(ttl=3600)
async def _get_trending_tags():
    """Get top 250 trending tags among pending posts, with stats."""
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
    """Return a minimal version of get_dynamic_global_properties data."""
    # TODO: trim this response; really only need: total_vesting_fund_steem,
    #   total_vesting_shares, sbd_interest_rate
    return json.loads(query_one("SELECT dgpo FROM hive_state"))

def _get_feed_price():
    """Get a steemd-style ratio object representing feed price."""
    price = query_one("SELECT usd_per_steem FROM hive_state")
    return {"base": "%.3f SBD" % price, "quote": "1.000 STEEM"}

def _load_discussion_recursive(author, permlink):
    """`get_state`-compatible recursive thread loader."""
    post_id = _get_post_id(author, permlink)
    return _load_posts_recursive([post_id])

def _load_posts_recursive(post_ids):
    """Recursive post loader used by `_load_discussion_recursive`."""
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

def _load_accounts(names):
    """`get_accounts`-style lookup for `get_state` compat layer."""
    sql = """SELECT id, name, display_name, about, reputation
               FROM hive_accounts WHERE name IN :names"""
    rows = query_all(sql, names=tuple(names))
    return [_condenser_account(row) for row in rows]

def _condenser_account(row):
    """Convert an internal account record into legacy-steemd style."""
    return {
        'name': row['name'],
        'reputation': _rep_to_raw(row['reputation']),
        'json_metadata': json.dumps({
            'profile': {'name': row['display_name'], 'about': row['about']}})}

def _validate_limit(limit, ubound=100):
    """Given a user-provided limit, return a valid int, or raise."""
    limit = int(limit)
    if limit <= 0:
        raise Exception("invalid limit")
    if limit > ubound:
        raise Exception("limit exceeded")
    return limit

def _follow_type_to_int(follow_type: str):
    """Convert steemd-style "follow type" into internal status (int)."""
    if follow_type not in ['blog', 'ignore']:
        raise Exception("Invalid follow_type")
    return 1 if follow_type == 'blog' else 2

def _get_post_id(author, permlink):
    """Given an author/permlink, retrieve the id from db."""
    sql = "SELECT id FROM hive_posts WHERE author = :a AND permlink = :p"
    _id = query_one(sql, a=author, p=permlink)
    if not _id:
        raise Exception("post not found: %s/%s" % (author, permlink))
    return _id

def _get_posts(ids):
    """Given an array of post ids, returns full objects in the same order."""
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


def _condenser_post_object(row):
    """Given a hive_posts_cache row, create a legacy-style post object."""
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

    post['last_payout'] = _json_date(row['payout_at'] if paid else None)
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
    #post['net_votes'] = post['total_votes'] - row['up_votes']
    #post['allow_replies'] = raw_json['allow_replies']
    #post['allow_votes'] = raw_json['allow_votes']
    #post['allow_curation_rewards'] = raw_json['allow_curation_rewards']
    #post['beneficiaries'] = raw_json['beneficiaries']
    #post['curator_payout_value'] = raw_json['curator_payout_value'] if paid else _amount(0)
    #post['total_payout_value'] = _amount(row['payout'] - float(raw_json['curator_payout_value'].split(' ')[0])) if paid else _amount(0)

    return post

def _amount(amount, asset='SBD'):
    """Return a steem-style amount string given a (numeric, asset-str)."""
    if asset == 'SBD':
        return "%.3f SBD" % amount
    raise Exception("unexpected %s" % asset)

def _hydrate_active_votes(vote_csv):
    """Convert minimal CSV representation into steemd-style object."""
    if not vote_csv:
        return []
    cols = 'voter,rshares,percent,reputation'.split(',')
    votes = vote_csv.split("\n")
    return [dict(zip(cols, line.split(','))) for line in votes]

def _json_date(date=None):
    """Given a db datetime, return a steemd/json-friendly version."""
    if not date:
        return '1969-12-31T23:59:59'
    return 'T'.join(str(date).split(' '))

def _rep_to_raw(rep):
    """Convert a UI-ready rep score back into its approx raw value."""
    if not isinstance(rep, (str, float, int)):
        return 0
    rep = float(rep) - 25
    rep = rep / 9
    rep = rep + 9
    sign = 1 if rep >= 0 else -1
    return int(sign * pow(10, rep))


if __name__ == '__main__':
    print(_load_discussion_recursive('roadscape', 'hello-world'))
