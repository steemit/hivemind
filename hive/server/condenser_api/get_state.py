"""Routes then builds a get_state response object"""

#pylint: disable=line-too-long,too-many-lines
import logging
from collections import OrderedDict
import ujson as json

from hive.utils.normalize import legacy_amount
from hive.server.common.mutes import Mutes

from hive.server.condenser_api.objects import (
    load_accounts,
    load_posts,
    load_posts_keyed,
    load_posts_reblogs)
from hive.server.condenser_api.common import (
    ApiError,
    return_error_info,
    valid_account,
    valid_permlink,
    valid_sort,
    valid_tag,
    get_post_id)
from hive.server.condenser_api.tags import (
    get_trending_tags,
    get_top_trending_tags_summary)

import hive.server.condenser_api.cursor as cursor

log = logging.getLogger(__name__)

# steemd account 'tabs' - specific post list queries
ACCOUNT_TAB_KEYS = {
    'blog': 'blog',
    'feed': 'feed',
    'comments': 'comments',
    'recent-replies': 'recent_replies'}

# dummy account paths used by condenser - just need account object
ACCOUNT_TAB_IGNORE = [
    'followed',
    'followers',
    'permissions',
    'password',
    'settings']

# misc dummy paths used by condenser - send minimal get_state structure
CONDENSER_NOOP_URLS = [
    'create_account',
    'approval',
    'recover_account_step_1',
    'recover_account_step_2',
    'submit.html',
    'market',
    'change_password',
    'login.html',
    'welcome',
    'tos.html',
    'privacy.html',
    'support.html',
    'faq.html',
    'about.html',
    'pick_account',
    'waiting_list.html',
]

# post list sorts
POST_LIST_SORTS = [
    'trending',
    'promoted',
    'hot',
    'created',
    'payout',
    'payout_comments',
    # unsupported:
    'recent',
    'trending30',
    'active',
    'votes',
    'responses',
    'cashout',
]

@return_error_info
async def get_state(context, path: str):
    """`get_state` reimplementation.

    See: https://github.com/steemit/steem/blob/06e67bd4aea73391123eca99e1a22a8612b0c47e/libraries/app/database_api.cpp#L1937
    """
    (path, part) = _normalize_path(path)

    db = context['db']

    state = {
        'feed_price': await _get_feed_price(db),
        'props': await _get_props_lite(db),
        'tags': {},
        'accounts': {},
        'content': {},
        'tag_idx': {'trending': []},
        'discussion_idx': {"": {}}}

    # account - `/@account/tab` (feed, blog, comments, replies)
    if part[0] and part[0][0] == '@':
        assert not part[1] == 'transfers', 'transfers API not served here'
        assert not part[2], 'unexpected account path[2] %s' % path

        if part[1] == '':
            part[1] = 'blog'

        account = valid_account(part[0][1:])
        state['accounts'][account] = await _load_account(db, account)

        if part[1] in ACCOUNT_TAB_KEYS:
            key = ACCOUNT_TAB_KEYS[part[1]]
            posts = await _get_account_discussion_by_key(db, account, key)
            state['content'] = _keyed_posts(posts)
            state['accounts'][account][key] = list(state['content'].keys())
        elif part[1] in ACCOUNT_TAB_IGNORE:
            pass # condenser no-op URLs
        else:
            # invalid/undefined case; probably requesting `@user/permlink`,
            # but condenser still relies on a valid response for redirect.
            state['error'] = 'invalid get_state account path %s' % path

    # discussion - `/category/@account/permlink`
    elif part[1] and part[1][0] == '@':
        author = valid_account(part[1][1:])
        permlink = valid_permlink(part[2])
        state['content'] = await _load_discussion(db, author, permlink)
        state['accounts'] = await _load_content_accounts(db, state['content'])

    # ranked posts - `/sort/category`
    elif part[0] in POST_LIST_SORTS:
        assert not part[2], "unexpected discussion path part[2] %s" % path
        sort = valid_sort(part[0])
        tag = valid_tag(part[1].lower(), allow_empty=True)
        pids = await cursor.pids_by_query(db, sort, '', '', 20, tag)
        state['content'] = _keyed_posts(await load_posts(db, pids))
        state['discussion_idx'] = {tag: {sort: list(state['content'].keys())}}
        state['tag_idx'] = {'trending': await get_top_trending_tags_summary(context)}

    # tag "explorer" - `/tags`
    elif part[0] == "tags":
        assert not part[1] and not part[2], 'invalid /tags request'
        for tag in await get_trending_tags(context):
            state['tag_idx']['trending'].append(tag['name'])
            state['tags'][tag['name']] = tag

    elif part[0] in CONDENSER_NOOP_URLS:
        assert not part[1] and not part[2]

    else:
        raise ApiError('unhandled path: /%s' % path)

    return state

async def _get_account_discussion_by_key(db, account, key):
    assert account, 'account must be specified'
    assert key, 'discussion key must be specified'

    if key == 'recent_replies':
        pids = await cursor.pids_by_replies_to_account(db, account, '', 20)
        posts = await load_posts(db, pids)
    elif key == 'comments':
        pids = await cursor.pids_by_account_comments(db, account, '', 20)
        posts = await load_posts(db, pids)
    elif key == 'blog':
        pids = await cursor.pids_by_blog(db, account, '', '', 20)
        posts = await load_posts(db, pids)
    elif key == 'feed':
        res = await cursor.pids_by_feed_with_reblog(db, account, '', '', 20)
        posts = await load_posts_reblogs(db, res)
    else:
        raise ApiError("unknown account discussion key %s" % key)

    return posts

def _normalize_path(path):
    if path and path[0] == '/':
        path = path[1:]

    # some clients pass the query string to get_state, and steemd allows it :(
    if '?' in path:
        path = path.split('?')[0]

    if not path:
        path = 'trending'
    assert '#' not in path, 'path contains hash mark (#)'
    assert '?' not in path, 'path contains query string: `%s`' % path

    parts = path.split('/')
    if len(parts) == 4 and parts[3] == '':
        parts = parts[:-1]
    assert len(parts) < 4, 'too many parts in path: `%s`' % path
    while len(parts) < 3:
        parts.append('')
    return (path, parts)

def _keyed_posts(posts):
    out = OrderedDict()
    for post in posts:
        out[_ref(post)] = post
    return out

def _ref(post):
    return post['author'] + '/' + post['permlink']

async def _load_content_accounts(db, content):
    if not content:
        return {}
    posts = content.values()
    names = set(map(lambda p: p['author'], posts))
    accounts = await load_accounts(db, names)
    return {a['name']: a for a in accounts}

async def _load_account(db, name):
    ret = await load_accounts(db, [name])
    assert ret, 'account not found: `%s`' % name
    account = ret[0]
    for key in ACCOUNT_TAB_KEYS.values():
        account[key] = []
    return account

async def _child_ids(db, parent_ids):
    """Load child ids for multuple parent ids."""
    sql = """
             SELECT parent_id, array_agg(id)
               FROM hive_posts
              WHERE parent_id IN :ids
                AND is_deleted = '0'
           GROUP BY parent_id
    """
    rows = await db.query_all(sql, ids=tuple(parent_ids))
    return [[row[0], row[1]] for row in rows]

async def _load_discussion(db, author, permlink):
    """Load a full discussion thread."""
    root_id = await get_post_id(db, author, permlink)
    if not root_id:
        return {}

    # build `ids` list and `tree` map
    ids = []
    tree = {}
    todo = [root_id]
    while todo:
        ids.extend(todo)
        rows = await _child_ids(db, todo)
        todo = []
        for pid, cids in rows:
            tree[pid] = cids
            todo.extend(cids)

    # load all post objects, build ref-map
    posts = await load_posts_keyed(db, ids)

    # remove posts/comments from muted accounts
    muted_accounts = Mutes.all()
    rem_pids = []
    for pid, post in posts.items():
        if post['author'] in muted_accounts:
            rem_pids.append(pid)
    for pid in rem_pids:
        if pid in posts:
            del posts[pid]
        if pid in tree:
            rem_pids.extend(tree[pid])

    refs = {pid: _ref(post) for pid, post in posts.items()}

    # add child refs to parent posts
    for pid, post in posts.items():
        if pid in tree:
            post['replies'] = [refs[cid] for cid in tree[pid]
                               if cid in refs]

    # return all nodes keyed by ref
    return {refs[pid]: post for pid, post in posts.items()}

async def _get_feed_price(db):
    """Get a steemd-style ratio object representing feed price."""
    price = await db.query_one("SELECT usd_per_steem FROM hive_state")
    return {"base": "%.3f SBD" % price, "quote": "1.000 STEEM"}

async def _get_props_lite(db):
    """Return a minimal version of get_dynamic_global_properties data."""
    raw = json.loads(await db.query_one("SELECT dgpo FROM hive_state"))

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
