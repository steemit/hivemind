"""Steemd/condenser_api compatibility layer API methods."""

import json
import inspect

from functools import wraps
from aiocache import cached
from hive.db.methods import query_one, query_row, query_col, query_all
from hive.steem.steem_client import SteemClient
from hive.utils.normalize import parse_amount
import hive.server.cursor as cursor

def _strict_list(params, expected_len):
    assert isinstance(params, list), "params not a list"
    assert len(params) == expected_len, "expected %d params" % expected_len
    return params

def _strict_query(params, ignore_key=None):
    query = _strict_list(params, 1)[0]
    assert isinstance(query, dict), "query must be dict"

    optional_keys = set(['truncate_body'])
    expected_keys = set(['start_author', 'start_permlink', 'limit', 'tag'])
    if ignore_key: # e.g. `tag` unused by get_discussion_by_comments
        expected_keys = expected_keys - set([ignore_key])

    provided_keys = query.keys()
    missing = expected_keys - provided_keys
    unknown = provided_keys - expected_keys - optional_keys
    assert not missing, "missing query key %s" % missing
    assert not unknown, "unknown query key %s" % unknown

    return query

# e.g. {"id":0,"jsonrpc":"2.0","method":"call",
#       "params":["database_api","get_state",["trending"]]}
async def call(api, method, params):
    """Routes legacy-style `call` method requests."""
    # pylint: disable=protected-access, too-many-return-statements, too-many-branches

    if method == 'get_followers':
        return await get_followers(*_strict_list(params, 4))
    elif method == 'get_following':
        return await get_following(*_strict_list(params, 4))
    elif method == 'get_follow_count':
        return await get_follow_count(*_strict_list(params, 1))

    elif method == 'get_content':
        return await get_content(*_strict_list(params, 2))
    elif method == 'get_content_replies':
        return await get_content_replies(*_strict_list(params, 2))
    elif method == 'get_state':
        return await get_state(*_strict_list(params, 1))

    elif method == 'get_discussions_by_trending':
        return await get_discussions_by_trending(**_strict_query(params))
    elif method == 'get_discussions_by_hot':
        return await get_discussions_by_hot(**_strict_query(params))
    elif method == 'get_discussions_by_promoted':
        return await get_discussions_by_promoted(**_strict_query(params))
    elif method == 'get_discussions_by_created':
        return await get_discussions_by_created(**_strict_query(params))
    elif method == 'get_discussions_by_blog':
        return await get_discussions_by_blog(**_strict_query(params))
    elif method == 'get_discussions_by_feed':
        return await get_discussions_by_feed(**_strict_query(params))
    elif method == 'get_discussions_by_comments':
        return await get_discussions_by_comments(**_strict_query(params, 'tag'))
    elif method == 'get_replies_by_last_update':
        return await get_replies_by_last_update(*_strict_list(params, 3))

    raise Exception("unknown method: {}.{}({})".format(api, method, params))


async def get_followers(account: str, start: str, follow_type: str, limit: int):
    """Get all accounts following `account`. (EOL)"""
    account = _validate_account(account)
    start = _validate_account(account, allow_empty=True)
    limit = _validate_limit(limit, 1000)
    state = _follow_type_to_int(follow_type)
    followers = cursor.get_followers(account, start, state, limit)
    return [dict(follower=name, following=account, what=[follow_type])
            for name in followers]

async def get_following(account: str, start: str, follow_type: str, limit: int):
    """Get all accounts `account` follows. (EOL)"""
    account = _validate_account(account)
    start = _validate_account(account, allow_empty=True)
    limit = _validate_limit(limit, 1000)
    state = _follow_type_to_int(follow_type)
    following = cursor.get_following(account, start, state, limit)
    return [dict(follower=account, following=name, what=[follow_type])
            for name in following]

async def get_follow_count(account: str):
    """Get follow count stats. (EOL)"""
    count = cursor.get_follow_counts(account)
    return dict(account=account,
                following_count=count['following'],
                follower_count=count['followers'])


def nested_query_compat(function):
    """Unpack strange format used by some clients, accepted by steemd.

    Sometimes a discussion query object is nested inside a list[1]. Eg:

        {... "method":"condenser_api.get_discussions_by_hot",
             "params":[{"tag":"steem","limit":1}]}

    In these cases jsonrpcserver dispatch just shoves it into the first
    arg. This decorator checks for this specific condition and unpacks
    the query to be passed as kwargs.
    """
    @wraps(function)
    def wrapper(*args, **kwargs):
        """Checks for specific condition signature and unpacks query"""
        if args and not kwargs and len(args) < 2 and isinstance(args[0], dict):
            return function(**args[0])
        return function(*args, **kwargs)
    return wrapper


@nested_query_compat
async def get_discussions_by_trending(start_author: str = '', start_permlink: str = '',
                                      limit: int = 20, tag: str = None,
                                      truncate_body: int = 0):
    """Query posts, sorted by trending score."""
    ids = cursor.pids_by_query(
        'trending',
        _validate_account(start_author, allow_empty=True),
        _validate_permlink(start_permlink, allow_empty=True),
        _validate_limit(limit, 20),
        tag)
    return _get_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_hot(start_author: str = '', start_permlink: str = '',
                                 limit: int = 20, tag: str = None,
                                 truncate_body: int = 0):
    """Query posts, sorted by hot score."""
    ids = cursor.pids_by_query(
        'hot',
        _validate_account(start_author, allow_empty=True),
        _validate_permlink(start_permlink, allow_empty=True),
        _validate_limit(limit, 20),
        tag)
    return _get_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_promoted(start_author: str = '', start_permlink: str = '',
                                      limit: int = 20, tag: str = None,
                                      truncate_body: int = 0):
    """Query posts, sorted by promoted amount."""
    ids = cursor.pids_by_query(
        'promoted',
        _validate_account(start_author, allow_empty=True),
        _validate_permlink(start_permlink, allow_empty=True),
        _validate_limit(limit, 20),
        tag)
    return _get_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_created(start_author: str = '', start_permlink: str = '',
                                     limit: int = 20, tag: str = None,
                                     truncate_body: int = 0):
    """Query posts, sorted by creation date."""
    ids = cursor.pids_by_query(
        'created',
        _validate_account(start_author, allow_empty=True),
        _validate_permlink(start_permlink, allow_empty=True),
        _validate_limit(limit, 20),
        tag)
    return _get_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_blog(tag: str, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20,
                                  truncate_body: int = 0):
    """Retrieve account's blog posts."""
    ids = cursor.pids_by_blog(
        _validate_account(tag),
        _validate_account(start_author, allow_empty=True),
        _validate_permlink(start_permlink, allow_empty=True),
        _validate_limit(limit, 20))
    return _get_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_discussions_by_feed(tag: str, start_author: str = '',
                                  start_permlink: str = '', limit: int = 20,
                                  truncate_body: int = 0):
    """Retrieve account's personalized feed."""
    res = cursor.pids_by_feed_with_reblog(
        _validate_account(tag),
        _validate_account(start_author, allow_empty=True),
        _validate_permlink(start_permlink, allow_empty=True),
        _validate_limit(limit, 20))

    reblogged_by = dict(res)
    posts = _get_posts([r[0] for r in res], truncate_body=truncate_body)

    # Merge reblogged_by data into result set
    for post in posts:
        rby = set(reblogged_by[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts


@nested_query_compat
async def get_discussions_by_comments(start_author: str, start_permlink: str = '',
                                      limit: int = 20, truncate_body: int = 0):
    """Get comments by made by author."""
    ids = cursor.pids_by_account_comments(
        _validate_account(start_author),
        _validate_permlink(start_permlink, allow_empty=True),
        _validate_limit(limit, 20))
    return _get_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_replies_by_last_update(start_author: str, start_permlink: str = '',
                                     limit: int = 20, truncate_body: int = 0):
    """Get all replies made to any of author's posts."""
    ids = cursor.pids_by_replies_to_account(
        _validate_account(start_author),
        _validate_permlink(start_permlink, allow_empty=True),
        _validate_limit(limit, 50))
    return _get_posts(ids, truncate_body=truncate_body)


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

    # //-- debug; temp sanity check
    state1 = "{}".format(state)
    # //--

    # account tabs (feed, blog, comments, replies)
    if part[0] and part[0][0] == '@':
        if not part[1]:
            part[1] = 'blog'
        if part[1] == 'transfers':
            raise Exception("transfers API not served by hive")
        if part[2]:
            raise Exception("unexpected account path part[2] %s" % path)

        account = part[0][1:]

        # dummy paths used by condenser - just need account object
        ignore = ['followed', 'followers', 'permissions',
                  'password', 'settings']

        # steemd account 'tabs' - specific post list queries
        keys = {'recent-replies': 'recent_replies',
                'comments': 'comments',
                'blog': 'blog',
                'feed': 'feed'}

        if part[1] in ignore:
            key = None
        elif part[1] not in keys:
            raise Exception("invalid account path %s" % path)
        else:
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
        else:
            posts = [] # no-op for `ignore` paths

        state['accounts'][account][key] = []
        for post in posts:
            ref = post['author'] + '/' + post['permlink']
            state['accounts'][account][key].append(ref)
            state['content'][ref] = post

    # discussion thread
    elif part[1] and part[1][0] == '@':
        author = _validate_account(part[1][1:])
        permlink = _validate_permlink(part[2])
        state['content'] = _load_discussion_recursive(author, permlink)
        accounts = set(map(lambda p: p['author'], state['content'].values()))
        state['accounts'] = {a['name']: a for a in _load_accounts(accounts)}

    # trending/etc pages
    elif part[0] in ['trending', 'promoted', 'hot', 'created']:
        if part[2]:
            raise Exception("unexpected discussion path part[2] %s" % path)
        sort = part[0]
        tag = part[1].lower()
        ids = cursor.pids_by_query(sort, '', '', 20, tag)
        posts = _get_posts(ids)
        state['discussion_idx'][tag] = {sort: []}
        for post in posts:
            ref = post['author'] + '/' + post['permlink']
            state['content'][ref] = post
            state['discussion_idx'][tag][sort].append(ref)
        state['tag_idx']['trending'] = await _get_top_trending_tags()

    # witness list
    elif part[0] == 'witnesses' or part[0] == '~witnesses':
        raise Exception("not implemented")

    # tag "explorer"
    elif part[0] == "tags":
        state['tag_idx']['trending'] = []
        tags = await _get_trending_tags()
        for tag in tags:
            state['tag_idx']['trending'].append(tag['name'])
            state['tags'][tag['name']] = tag

    # non-matching path
    else:
        print("[WARNING] unknown path {}".format(path))
        return state

    # //-- debug; should not happen
    state2 = "{}".format(state)
    if state1 == state2: # if state did not change, complain
        raise Exception("unrecognized path `{}`" % path)
    # //--

    return state


async def get_content(author: str, permlink: str):
    """Get a single post object."""
    _validate_account(author)
    _validate_permlink(permlink)
    post_id = _get_post_id(author, permlink)
    if not post_id:
        return {'id': 0, 'author': '', 'permlink': ''}
    return _get_posts([post_id])[0]


async def get_content_replies(parent: str, parent_permlink: str):
    """Get a list of post objects based on parent."""
    _validate_account(parent)
    _validate_permlink(parent_permlink)
    post_id = _get_post_id(parent, parent_permlink)
    if not post_id:
        return []
    post_ids = query_col("SELECT id FROM hive_posts WHERE "
                         "parent_id = %d AND is_deleted = '0'" % post_id)
    if not post_ids:
        return []
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
    raw = json.loads(query_one("SELECT dgpo FROM hive_state"))

    # convert NAI amounts to legacy
    nais = ['virtual_supply', 'current_supply', 'current_sbd_supply',
            'pending_rewarded_vesting_steem', 'pending_rewarded_vesting_shares',
            'total_vesting_fund_steem', 'total_vesting_shares']
    for k in nais:
        if k in raw:
            raw[k] = _legacy_amount(raw[k])

    return dict(
        time=raw['time'], #*
        sbd_print_rate=raw['sbd_print_rate'],
        sbd_interest_rate=raw['sbd_interest_rate'],
        head_block_number=raw['head_block_number'], #*
        total_vesting_shares=raw['total_vesting_shares'],
        total_vesting_fund_steem=raw['total_vesting_fund_steem'],
        last_irreversible_block_num=raw['last_irreversible_block_num'], #*
    )

def _get_feed_price():
    """Get a steemd-style ratio object representing feed price."""
    price = query_one("SELECT usd_per_steem FROM hive_state")
    return {"base": "%.3f SBD" % price, "quote": "1.000 STEEM"}

def _load_discussion_recursive(author, permlink):
    """`get_state`-compatible recursive thread loader."""
    _validate_account(author)
    _validate_permlink(permlink)
    post_id = _get_post_id(author, permlink)
    if not post_id:
        return {}
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

def _validate_account(name, allow_empty=False):
    assert isinstance(name, str), "account must be string; received: %s" % name
    if not (allow_empty and name == ''):
        assert len(name) >= 3 and len(name) <= 16, "invalid account: %s" % name
    return name

def _validate_permlink(permlink, allow_empty=False):
    # pylint: disable=len-as-condition
    assert isinstance(permlink, str), "permlink must be string: %s" % permlink
    if not (allow_empty and permlink == ''):
        assert len(permlink) > 0 and len(permlink) <= 256, "invalid permlink"
    return permlink

def _validate_limit(limit, ubound=100):
    """Given a user-provided limit, return a valid int, or raise."""
    limit = int(limit)
    assert limit > 0, "limit must be positive"
    assert limit <= ubound, "limit exceeds max"
    return limit

def _follow_type_to_int(follow_type: str):
    """Convert steemd-style "follow type" into internal status (int)."""
    assert follow_type in ['blog', 'ignore'], "invalid follow_type"
    return 1 if follow_type == 'blog' else 2

def _get_post_id(author, permlink):
    """Given an author/permlink, retrieve the id from db."""
    sql = "SELECT id, is_deleted FROM hive_posts WHERE author = :a AND permlink = :p"
    row = query_row(sql, a=author, p=permlink)
    if not row:
        print("_get_post_id - post not found: %s/%s" % (author, permlink))
        return None
    _id, deleted = row
    if deleted:
        print("_get_post_id - post was deleted %s/%s" % (author, permlink))
        return None
    return _id

def _get_posts(ids, truncate_body=0):
    """Given an array of post ids, returns full objects in the same order."""
    if not ids:
        caller = inspect.stack()[1][3]
        print("empty result for %s" % caller)
        return []

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
        post = _condenser_post_object(row, truncate_body=truncate_body)
        posts_by_id[row['post_id']] = post

    # in rare cases of cache inconsistency, recover and warn
    missed = set(ids) - posts_by_id.keys()
    if missed:
        print("WARNING: get_posts do not exist in cache: {}".format(missed))
        for _id in missed:
            ids.remove(_id)

    return [posts_by_id[_id] for _id in ids]


def _condenser_post_object(row, truncate_body=0):
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
    post['body'] = row['body'][0:truncate_body] if truncate_body else row['body']
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
    #curator_payout = amount(raw_json['curator_payout_value'])
    #post['total_payout_value'] = _amount(row['payout'] - curator_payout) if paid else _amount(0)

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

def _legacy_amount(value):
    """Return a steem-style amount string given a (numeric, asset-str)."""
    if isinstance(value, str):
        return value # already legacy
    amount, asset = parse_amount(value)
    prec = {'SBD': 3, 'STEEM': 3, 'VESTS': 6}[asset]
    tmpl = ("%%.%df %%s" % prec)
    return tmpl % (amount, asset)

if __name__ == '__main__':
    print(_load_discussion_recursive('roadscape', 'hello-world'))
