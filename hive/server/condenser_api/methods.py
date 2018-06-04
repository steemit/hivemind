"""Steemd/condenser_api compatibility layer API methods."""

import json
import re
import collections

from functools import wraps
from aiocache import cached

from hive.db.methods import query_one, query_row, query_col, query_all
from hive.utils.normalize import parse_amount
from hive.server.condenser_api.objects import load_accounts, load_posts
import hive.server.condenser_api.cursor as cursor

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

async def call(api, method, params):
    """Routes legacy-style `call` method requests.

    Example:
    ```
    {"id":0,"jsonrpc":"2.0","method":"call",
     "params":["database_api","get_state",["trending"]]}
    ```"""
    # pylint: disable=too-many-return-statements, too-many-branches
    assert api == 'condenser_api', "`call` requires condenser_api"

    # Follows
    if method == 'get_followers':
        return await get_followers(*_strict_list(params, 4))
    elif method == 'get_following':
        return await get_following(*_strict_list(params, 4))
    elif method == 'get_follow_count':
        return await get_follow_count(*_strict_list(params, 1))

    # Content primitives
    elif method == 'get_content':
        return await get_content(*_strict_list(params, 2))
    elif method == 'get_content_replies':
        return await get_content_replies(*_strict_list(params, 2))

    # Content monolith
    elif method == 'get_state':
        return await get_state(*_strict_list(params, 1))

    # Global discussion queries
    elif method == 'get_discussions_by_trending':
        return await get_discussions_by_trending(**_strict_query(params))
    elif method == 'get_discussions_by_hot':
        return await get_discussions_by_hot(**_strict_query(params))
    elif method == 'get_discussions_by_promoted':
        return await get_discussions_by_promoted(**_strict_query(params))
    elif method == 'get_discussions_by_created':
        return await get_discussions_by_created(**_strict_query(params))

    # Account discussion queries
    elif method == 'get_discussions_by_blog':
        return await get_discussions_by_blog(**_strict_query(params))
    elif method == 'get_discussions_by_feed':
        return await get_discussions_by_feed(**_strict_query(params))
    elif method == 'get_discussions_by_comments':
        return await get_discussions_by_comments(**_strict_query(params, 'tag'))
    elif method == 'get_replies_by_last_update':
        return await get_replies_by_last_update(*_strict_list(params, 3))

    raise Exception("unknown method: {}.{}({})".format(api, method, params))


# Follows Queries

def _follow_type_to_int(follow_type: str):
    """Convert steemd-style "follow type" into internal status (int)."""
    assert follow_type in ['blog', 'ignore'], "invalid follow_type"
    return 1 if follow_type == 'blog' else 2

def _legacy_follower(follower, following, follow_type):
    return dict(follower=follower, following=following, what=[follow_type])

async def get_followers(account: str, start: str, follow_type: str, limit: int):
    """Get all accounts following `account`. (EOL)"""
    followers = cursor.get_followers(
        _validate_account(account),
        _validate_account(start, allow_empty=True),
        _follow_type_to_int(follow_type),
        _validate_limit(limit, 1000))
    return [_legacy_follower(name, account, follow_type) for name in followers]


async def get_following(account: str, start: str, follow_type: str, limit: int):
    """Get all accounts `account` follows. (EOL)"""
    following = cursor.get_following(
        _validate_account(account),
        _validate_account(start, allow_empty=True),
        _follow_type_to_int(follow_type),
        _validate_limit(limit, 1000))
    return [_legacy_follower(account, name, follow_type) for name in following]

async def get_follow_count(account: str):
    """Get follow count stats. (EOL)"""
    count = cursor.get_follow_counts(account)
    return dict(account=account,
                following_count=count['following'],
                follower_count=count['followers'])


# Content Primitives

async def get_content(author: str, permlink: str):
    """Get a single post object."""
    _validate_account(author)
    _validate_permlink(permlink)
    post_id = _get_post_id(author, permlink)
    if not post_id:
        return {'id': 0, 'author': '', 'permlink': ''}
    return load_posts([post_id])[0]


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
    return load_posts(post_ids)


# Discussion Queries

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
    return load_posts(ids, truncate_body=truncate_body)


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
    return load_posts(ids, truncate_body=truncate_body)


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
    return load_posts(ids, truncate_body=truncate_body)


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
    return load_posts(ids, truncate_body=truncate_body)


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
    return load_posts(ids, truncate_body=truncate_body)


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
    posts = load_posts([r[0] for r in res], truncate_body=truncate_body)

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
    return load_posts(ids, truncate_body=truncate_body)


@nested_query_compat
async def get_replies_by_last_update(start_author: str, start_permlink: str = '',
                                     limit: int = 20, truncate_body: int = 0):
    """Get all replies made to any of author's posts."""
    ids = cursor.pids_by_replies_to_account(
        _validate_account(start_author),
        _validate_permlink(start_permlink, allow_empty=True),
        _validate_limit(limit, 50))
    return load_posts(ids, truncate_body=truncate_body)

def _normalize_path(path):
    if path[0] == '/':
        path = path[1:]
    if not path:
        path = 'trending'
    parts = path.split('/')
    if len(parts) > 3:
        raise Exception("invalid path %s" % path)
    while len(parts) < 3:
        parts.append('')
    return (path, parts)

def _keyed_posts(posts):
    out = collections.OrderedDict()
    for post in posts:
        ref = post['author'] + '/' + post['permlink']
        out[ref] = post
    return out

def _load_posts_accounts(posts):
    names = set(map(lambda p: p['author'], posts))
    accounts = load_accounts(names)
    return {a['name']: a for a in accounts}

async def get_state(path: str):
    """`get_state` reimplementation.

    See: https://github.com/steemit/steem/blob/06e67bd4aea73391123eca99e1a22a8612b0c47e/libraries/app/database_api.cpp#L1937
    """
    (path, part) = _normalize_path(path)

    state = {
        'feed_price': _get_feed_price(),
        'props': _get_props_lite(),
        'tags': {},
        'accounts': {},
        'content': {},
        'tag_idx': {'trending': []},
        'discussion_idx': {"": {}}}

    # account tabs (feed, blog, comments, replies)
    if part[0] and part[0][0] == '@':
        assert not part[1] == 'transfers', 'transfers API not served here'
        assert not part[1] == 'blog', 'canonical blog route is `/@account`'
        assert not part[2], 'unexpected account path[2] %s' % path

        account = _validate_account(part[0][1:])
        state['accounts'][account] = load_accounts([account])[0]

        # dummy paths used by condenser - just need account object
        ignore = ['followed', 'followers', 'permissions',
                  'password', 'settings']

        # steemd account 'tabs' - specific post list queries
        tabs = {'recent-replies': 'recent_replies',
                'comments': 'comments',
                'feed': 'feed',
                '': 'blog'}

        if part[1] not in ignore:
            assert part[1] in tabs, "invalid account path %s" % path
            tab = tabs[part[1]]

            if tab == 'recent_replies':
                posts = await get_replies_by_last_update(account, '', 20)
            elif tab == 'comments':
                posts = await get_discussions_by_comments(account, '', 20)
            elif tab == 'blog':
                posts = await get_discussions_by_blog(account, '', '', 20)
            elif tab == 'feed':
                posts = await get_discussions_by_feed(account, '', '', 20)

            state['content'] = _keyed_posts(posts)
            state['accounts'][account][tab] = list(state['content'].keys())

    # discussion thread
    elif part[1] and part[1][0] == '@':
        author = _validate_account(part[1][1:])
        permlink = _validate_permlink(part[2])
        state['content'] = _load_discussion_recursive(author, permlink)
        state['accounts'] = _load_posts_accounts(state['content'].values())

    # trending/etc pages
    elif part[0] in ['trending', 'promoted', 'hot', 'created']:
        assert not part[2], "unexpected discussion path part[2] %s" % path
        sort = _validate_sort(part[0])
        tag = _validate_tag(part[1].lower(), allow_empty=True)
        posts = load_posts(cursor.pids_by_query(sort, '', '', 20, tag))
        state['content'] = _keyed_posts(posts)
        state['discussion_idx'][tag][sort] = list(state['content'].keys())
        state['tag_idx']['trending'] = await _get_top_trending_tags()

    # tag "explorer"
    elif part[0] == "tags":
        assert not part[1] and not part[2], 'invalid /tags path'
        for tag in await _get_trending_tags():
            state['tag_idx']['trending'].append(tag['name'])
            state['tags'][tag['name']] = tag

    # witness list
    elif part[0] == 'witnesses' or part[0] == '~witnesses':
        raise Exception("not implemented")

    # non-matching path
    else:
        raise Exception('unknown path %s' % path)

    return state



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


def _legacy_amount(value):
    """Return a steem-style amount string given a (numeric, asset-str)."""
    if isinstance(value, str):
        return value # already legacy
    amount, asset = parse_amount(value)
    prec = {'SBD': 3, 'STEEM': 3, 'VESTS': 6}[asset]
    tmpl = ("%%.%df %%s" % prec)
    return tmpl % (amount, asset)


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
    post_id = _get_post_id(author, permlink)
    return _load_posts_recursive([post_id]) if post_id else {}

def _load_posts_recursive(post_ids):
    """Recursive post loader used by `_load_discussion_recursive`."""
    posts = load_posts(post_ids)

    out = {}
    for post, post_id in zip(posts, post_ids):
        out[post['author'] + '/' + post['permlink']] = post

        child_ids = query_col("SELECT id FROM hive_posts WHERE parent_id = %d "
                              "AND is_deleted = '0'" % post_id)
        if child_ids:
            children = _load_posts_recursive(child_ids)
            post['replies'] = list(children.keys())
            out = {**out, **children}

    return out



def _validate_account(name, allow_empty=False):
    assert isinstance(name, str), "account must be string; received: %s" % name
    if not (allow_empty and name == ''):
        assert len(name) >= 3 and len(name) <= 16, "invalid account: %s" % name
    return name

def _validate_permlink(permlink, allow_empty=False):
    assert isinstance(permlink, str), "permlink must be string: %s" % permlink
    if not (allow_empty and permlink == ''):
        assert permlink and len(permlink) <= 256, "invalid permlink"
    return permlink

def _validate_sort(sort, allow_empty=False):
    assert isinstance(sort, str), 'sort must be a string'
    if not (allow_empty and sort == ''):
        valid_sorts = ['trending', 'promoted', 'hot', 'created']
        assert sort in valid_sorts, 'invalid sort'
    return sort

def _validate_tag(tag, allow_empty=False):
    assert isinstance(tag, str), 'tag must be a string'
    if not (allow_empty and tag == ''):
        assert re.match('^[a-z0-9-]+$', str), 'invalid tag'
    return tag

def _validate_limit(limit, ubound=100):
    """Given a user-provided limit, return a valid int, or raise."""
    limit = int(limit)
    assert limit > 0, "limit must be positive"
    assert limit <= ubound, "limit exceeds max"
    return limit

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
