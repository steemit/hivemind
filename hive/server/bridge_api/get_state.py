"""Routes then builds a get_state response object"""

import logging
from collections import OrderedDict

from hive.server.hive_api.community import if_tag_community
from hive.server.hive_api.common import get_account_id
import hive.server.bridge_api.cursor as cursor
from hive.server.bridge_api.thread import get_discussion
from hive.server.bridge_api.methods import get_account_posts, get_trending_topics
from hive.server.bridge_api.objects import load_posts
from hive.server.common.helpers import (
    ApiError,
    return_error_info,
    valid_account,
    valid_permlink,
    valid_sort,
    valid_tag)

log = logging.getLogger(__name__)

# steemd account 'tabs' - specific post list queries
ACCOUNT_TAB_KEYS = {
    'blog': 'blog',
    'feed': 'feed',
    'comments': 'comments',
    'recent-replies': 'replies',
    'payout': 'payout'}

# post list sorts
POST_LIST_SORTS = [
    'trending',
    'promoted',
    'hot',
    'created',
    'payout',
    'payout_comments',
    'muted',
]

def _parse_route(path):
    """`get_state` routes reimplementation.

    See steem/libraries/plugins/apis/condenser_api/condenser_api.cpp
    """
    assert path, 'path cannot be blank'
    assert path[0] != '/', 'path cannot start with forward slash'
    assert path[-1] != '/', 'path cannot end with forward slash'
    assert '#' not in path, 'path contains hash mark (#)'
    assert '?' not in path, 'path contains query string: `%s`' % path
    assert path.count('/') < 3, 'too many parts in path: `%s`' % path

    part = path.split('/')
    parts = len(part)

    # account - `/@account/tab` (feed, blog, comments, replies)
    if parts == 2 and part[0][0] == '@' and part[1] in ACCOUNT_TAB_KEYS:
        return dict(page='account',
                    sort=ACCOUNT_TAB_KEYS[part[1]],
                    tag=part[0],
                    key=valid_account(part[0][1:]))

    # discussion - `/category/@account/permlink`
    if parts == 3 and part[1][0] == '@':
        author = valid_account(part[1][1:])
        permlink = valid_permlink(part[2])
        return dict(page='thread',
                    sort=None,
                    tag=part[0],
                    key=author + '/' + permlink)

    # ranked posts - `/sort/category`
    if parts <= 2 and part[0] in POST_LIST_SORTS:
        return dict(page='list',
                    sort=valid_sort(part[0]),
                    tag=valid_tag(part[1]) if parts == 2 else '',
                    key=None)

    raise ApiError("invalid path /%s" % path)

@return_error_info
async def get_state(context, path, observer=None):
    """Modified `get_state` implementation."""
    params = _parse_route(path)

    db = context['db']
    observer_id = await get_account_id(db, observer) if observer else None

    state = {
        'content': {},
        'tag_idx': {'trending': []},
        'discussion_idx': {"": {}}, # {tag: sort: [keys]}
        'community': {}}

    page = params['page']
    sort = params['sort']
    tag = params['tag']
    key = params['key']

    if page == 'account':
        state['content'] = await _key_account_posts(db, sort, key, observer)
    elif page == 'thread':
        state['content'] = await get_discussion(context, *key.split('/'))
    elif page == 'list':
        state['content'] = await _key_ranked_posts(db, sort, tag, observer_id)

    # account & list
    if page in ('account', 'list'):
        state['discussion_idx'] = {tag: {sort: list(state['content'].keys())}}

    # move this logic to condenser
    if page in ('thread', 'list') and tag:
        state['community'] = await _comms_map(context, tag, observer)

    topics = await get_trending_topics(context, observer)
    for (name, label) in topics:
        state['tag_idx']['trending'].append(name)
        if label:
            state['community'][name] = {'title': label}

    return state

async def _key_account_posts(db, sort, account, observer):
    posts = await get_account_posts({'db': db}, sort, account, '', '', 20, observer)
    return _keyed_posts(posts)

async def _key_ranked_posts(db, sort, tag, observer_id):
    pids = await cursor.pids_by_ranked(db, sort, '', '', 20, tag, observer_id)
    posts = await load_posts(db, pids)
    return _keyed_posts(posts)

async def _comms_map(context, tag, observer):
    if not tag: return {}
    community = await if_tag_community(context, tag, observer)
    return {tag: community} if community else {}

def _keyed_posts(posts):
    out = OrderedDict()
    for post in posts:
        out[post['author'] + '/' + post['permlink']] = post
    return out
