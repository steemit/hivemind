"""Routes then builds a get_state response object"""

import json
import collections

from aiocache import cached

from hive.db.methods import query_one, query_col, query_all
from hive.utils.normalize import legacy_amount

from hive.server.condenser_api.objects import (
    load_accounts,
    load_posts)
from hive.server.condenser_api.common import (
    valid_account,
    valid_permlink,
    valid_sort,
    valid_tag,
    get_post_id,
    get_child_ids)
from hive.server.condenser_api.methods import (
    get_replies_by_last_update,
    get_discussions_by_comments,
    get_discussions_by_blog,
    get_discussions_by_feed)

import hive.server.condenser_api.cursor as cursor

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

        account = valid_account(part[0][1:])
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
        author = valid_account(part[1][1:])
        permlink = valid_permlink(part[2])
        post_id = get_post_id(author, permlink)
        state['content'] = _load_posts_recursive([post_id]) if post_id else {}
        state['accounts'] = _load_content_accounts(state['content'])

    # trending/etc pages
    elif part[0] in ['trending', 'promoted', 'hot', 'created']:
        assert not part[2], "unexpected discussion path part[2] %s" % path
        sort = valid_sort(part[0])
        tag = valid_tag(part[1].lower(), allow_empty=True)
        posts = load_posts(cursor.pids_by_query(sort, '', '', 20, tag))
        state['content'] = _keyed_posts(posts)
        state['discussion_idx'] = {tag: {sort: list(state['content'].keys())}}
        state['tag_idx'] = {'trending': await _get_top_trending_tags()}

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

def _load_content_accounts(content):
    if not content:
        return {}
    posts = content.values()
    names = set(map(lambda p: p['author'], posts))
    accounts = load_accounts(names)
    return {a['name']: a for a in accounts}

def _load_posts_recursive(post_ids):
    """Recursively load a discussion thread."""
    out = {}
    if post_ids:
        posts = load_posts(post_ids)
        for post, post_id in zip(posts, post_ids):
            ref = post['author'] + '/' + post['permlink']
            out[ref] = post

            child_ids = get_child_ids(post_id)
            if child_ids:
                children = _load_posts_recursive(child_ids)
                post['replies'] = list(children.keys())
                out = {**out, **children}

    return out

def _get_feed_price():
    """Get a steemd-style ratio object representing feed price."""
    price = query_one("SELECT usd_per_steem FROM hive_state")
    return {"base": "%.3f SBD" % price, "quote": "1.000 STEEM"}

def _get_props_lite():
    """Return a minimal version of get_dynamic_global_properties data."""
    raw = json.loads(query_one("SELECT dgpo FROM hive_state"))

    # convert NAI amounts to legacy
    nais = ['virtual_supply', 'current_supply', 'current_sbd_supply',
            'pending_rewarded_vesting_steem', 'pending_rewarded_vesting_shares',
            'total_vesting_fund_steem', 'total_vesting_shares']
    for k in nais:
        if k in raw:
            raw[k] = legacy_amount(raw[k])

    return dict(
        time=raw['time'], #*
        sbd_print_rate=raw['sbd_print_rate'],
        sbd_interest_rate=raw['sbd_interest_rate'],
        head_block_number=raw['head_block_number'], #*
        total_vesting_shares=raw['total_vesting_shares'],
        total_vesting_fund_steem=raw['total_vesting_fund_steem'],
        last_irreversible_block_num=raw['last_irreversible_block_num'], #*
    )
