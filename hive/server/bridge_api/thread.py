"""Routes then builds a get_state response object"""

import logging

from hive.server.bridge_api.objects import load_posts_keyed
from hive.server.common.helpers import (
    return_error_info,
    valid_account,
    valid_permlink)
from hive.server.bridge_api.cursor import hide_pids_by_ids

log = logging.getLogger(__name__)

@return_error_info
async def get_discussion(context, author, permlink):
    """Modified `get_state` thread implementation."""
    db = context['db']

    author = valid_account(author)
    permlink = valid_permlink(permlink)
    root_id = await _get_post_id(db, author, permlink)
    hide_id = await _get_author_hide_id(db, author)
    if not root_id or hide_id:
        return {}

    post_hide_id = await _check_posts_hide_id(db, root_id)
    if post_hide_id:
        return {}

    return await _load_discussion(db, root_id)

async def _get_post_id(db, author, permlink):
    """Given an author/permlink, retrieve the id from db."""
    # Generate cache key for post_id lookup
    # Post IDs don't change once created, so we can cache for a long time
    cache_key = f'post_id_{author}_{permlink}'
    
    sql = ("SELECT id FROM hive_posts WHERE author = :a "
           "AND permlink = :p AND is_deleted = '0' LIMIT 1")
    return await db.query_one(sql, a=author, p=permlink, 
                             cache_key=cache_key, cache_ttl=3600)


async def _get_author_hide_id(db, author):
    """Given an author, retrieve the id from db."""
    sql = ("SELECT id FROM hive_posts_status WHERE list_type = '3'"
           "AND author = :a LIMIT 1")
    return await db.query_one(sql, a=author)


async def _check_posts_hide_id(db, post_id):
    """Given an post_id, retrieve the id from db."""
    sql = ("SELECT id FROM hive_posts_status WHERE list_type = '1'"
           "AND post_id = :post_id LIMIT 1")
    return await db.query_one(sql, post_id=post_id)

def _ref(post):
    return post['author'] + '/' + post['permlink']

async def _child_ids(db, parent_ids):
    """Load child ids for multuple parent ids."""
    # Optimized: Use LEFT JOIN instead of NOT EXISTS to improve performance
    # This avoids executing a subquery for each row and better utilizes indexes
    sql = """
        SELECT p.parent_id as parent_id, array_agg(p.id) as child_ids
        FROM hive_posts p
        LEFT JOIN hive_posts_status s ON s.list_type = '3' AND s.author = p.author
        WHERE p.parent_id IN :ids
        AND p.is_deleted = '0'
        AND s.id IS NULL
        GROUP BY p.parent_id
    """
    rows = await db.query_all(sql, ids=tuple(parent_ids),
        cache_key="_child_ids_" + "_".join(map(str, parent_ids)), cache_ttl=30)
    return [[row['parent_id'], row['child_ids']] for row in rows]

async def _load_discussion(db, root_id):
    """Load a full discussion thread."""
    # build `ids` list and `tree` map
    ids = []
    tree = {}
    todo = [root_id]
    while todo:
        ids.extend(todo)
        rows = await _child_ids(db, todo)
        todo = []
        for pid, cids in rows:
            if cids:
                hide_pids = await hide_pids_by_ids(db, cids)
                for hide_pid in hide_pids:
                    if hide_pid in cids:
                        cids.remove(hide_pid)

            tree[pid] = cids
            todo.extend(cids)

    # load all post objects, build ref-map
    posts = await load_posts_keyed(db, ids)

    # remove posts/comments from muted accounts
    rem_pids = []
    for pid, post in posts.items():
        if post['stats']['hide']:
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
