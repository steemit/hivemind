"""Routes then builds a get_state response object"""

import logging

from hive.server.bridge_api.objects import load_posts_keyed
from hive.server.common.helpers import (
    return_error_info,
    valid_account,
    valid_permlink)
from hive.server.bridge_api.cursor import hide_pids_by_ids

log = logging.getLogger(__name__)

# Hard caps to prevent connection-pool exhaustion on pathological threads.
# _load_discussion walks the comment tree level by level, issuing one
# _child_ids query per depth level. Without bounds, a 1000+ comment / 50+
# depth thread can hold a connection for an unbounded time and starve the
# pool. MAX_DEPTH bounds the number of sequential queries; MAX_THREAD_POSTS
# bounds the total number of posts loaded into memory.
MAX_THREAD_POSTS = 500
MAX_DEPTH = 50

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
    # Hide status changes rarely; cache to avoid spending a connection on every
    # get_discussion request. The db layer caches the "not found" case too.
    sql = ("SELECT id FROM hive_posts_status WHERE list_type = '3'"
           "AND author = :a LIMIT 1")
    return await db.query_one(sql, a=author, cache_key='author_hide_id_' + author,
                              cache_ttl=300)


async def _check_posts_hide_id(db, post_id):
    """Given an post_id, retrieve the id from db."""
    sql = ("SELECT id FROM hive_posts_status WHERE list_type = '1'"
           "AND post_id = :post_id LIMIT 1")
    return await db.query_one(sql, post_id=post_id,
                              cache_key='post_hide_id_' + str(post_id),
                              cache_ttl=300)

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
        cache_key="_child_ids_" + "_".join(map(str, parent_ids)), cache_ttl=120)
    return [[row['parent_id'], row['child_ids']] for row in rows]

async def _load_discussion(db, root_id):
    """Load a full discussion thread."""
    # build `ids` list and `tree` map
    ids = []
    tree = {}
    todo = [root_id]
    depth = 0
    truncated = False
    while todo:
        # Bound the number of sequential _child_ids queries (one per depth).
        if depth >= MAX_DEPTH:
            truncated = True
            break
        # Bound total posts collected so a wide thread cannot exhaust memory
        # or hold connections for too long. If this batch would exceed the cap,
        # take only what fits, resolve their (empty) child mapping, and stop.
        if len(ids) + len(todo) > MAX_THREAD_POSTS:
            todo = todo[:MAX_THREAD_POSTS - len(ids)]
            ids.extend(todo)
            rows = await _child_ids(db, todo)
            for pid, _cids in rows:
                tree[pid] = []
            truncated = True
            break
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
        depth += 1

    if truncated:
        log.warning("discussion %s truncated at depth=%d posts=%d",
                    root_id, depth, len(ids))

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
