"""Routes then builds a get_state response object"""

import logging
from time import perf_counter

from hive.server.bridge_api.objects import load_posts_keyed
from hive.server.common.helpers import (
    return_error_info,
    valid_account,
    valid_permlink)

log = logging.getLogger(__name__)

# Hard caps to prevent connection-pool exhaustion on pathological threads.
# _load_discussion walks the comment tree with a single recursive CTE query;
# MAX_DEPTH bounds recursion depth and MAX_THREAD_POSTS bounds the total
# number of posts loaded into memory (LIMIT inside the CTE).
MAX_THREAD_POSTS = 500
MAX_DEPTH = 50

# Fetch the whole comment subtree of a root post in ONE DB round-trip.
# Previously this was a level-by-level BFS issuing one _child_ids query per
# depth level (observed 500-1300ms for depth 4-10 threads, each query adding
# a pool acquire + parse + round-trip). Hidden authors (list_type='3') and
# hidden posts (list_type='1') are filtered inside the recursion so their
# subtrees are never traversed. ORDER BY depth,id keeps BFS order so
# LIMIT truncation matches the old level-by-level semantics.
_DISCUSSION_TREE_SQL = """
    WITH RECURSIVE tree AS (
        SELECT id, parent_id, 0 AS depth
        FROM hive_posts
        WHERE id = :root_id

        UNION ALL

        SELECT p.id, p.parent_id, t.depth + 1
        FROM hive_posts p
        INNER JOIN tree t ON p.parent_id = t.id
        LEFT JOIN hive_posts_status s3
            ON s3.list_type = '3' AND s3.author = p.author
        LEFT JOIN hive_posts_status s1
            ON s1.list_type = '1' AND s1.post_id = p.id
        WHERE p.is_deleted = '0'
          AND s3.id IS NULL
          AND s1.id IS NULL
          AND t.depth < :max_depth
    )
    SELECT id, parent_id, depth
    FROM tree
    WHERE parent_id IS NOT NULL
    ORDER BY depth, id
    LIMIT :max_posts
"""

@return_error_info
async def get_discussion(context, author, permlink):
    """Modified `get_state` thread implementation."""
    db = context['db']
    t_total = perf_counter()

    author = valid_account(author)
    permlink = valid_permlink(permlink)

    t = perf_counter()
    root_id = await _get_post_id(db, author, permlink)
    ms_pid = (perf_counter() - t) * 1000

    t = perf_counter()
    hide_id = await _get_author_hide_id(db, author)
    ms_hide = (perf_counter() - t) * 1000

    if not root_id or hide_id:
        return {}

    t = perf_counter()
    post_hide_id = await _check_posts_hide_id(db, root_id)
    ms_phi = (perf_counter() - t) * 1000
    if post_hide_id:
        return {}

    t = perf_counter()
    result = await _load_discussion(db, root_id)
    ms_load = (perf_counter() - t) * 1000

    ms_all = (perf_counter() - t_total) * 1000
    if ms_all > 1000:
        log.warning(
            "[DISCUSSION_SLOW] %s/%s total=%.0fms post_id=%.0fms "
            "hide_chk=%.0fms post_hide=%.0fms load=%.0fms",
            author, permlink, ms_all, ms_pid, ms_hide, ms_phi, ms_load)

    return result

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

async def _load_discussion(db, root_id):
    """Load a full discussion thread in a single recursive CTE query."""
    t_tree = perf_counter()
    rows = await db.query_all(
        _DISCUSSION_TREE_SQL,
        root_id=root_id,
        max_depth=MAX_DEPTH,
        max_posts=MAX_THREAD_POSTS,
        cache_key="discussion_tree_%d" % root_id,
        cache_ttl=120)
    ms_tree = (perf_counter() - t_tree) * 1000

    # build `ids` list and `tree` map (rows come in BFS order: depth, id)
    ids = [root_id]
    tree = {}
    max_depth_seen = 0
    for row in rows:
        pid, cid, depth = row['parent_id'], row['id'], row['depth']
        tree.setdefault(pid, []).append(cid)
        ids.append(cid)
        if depth > max_depth_seen:
            max_depth_seen = depth
    # LIMIT hit (rows capped) or recursion hit MAX_DEPTH => truncated
    truncated = (len(ids) > MAX_THREAD_POSTS) or (max_depth_seen >= MAX_DEPTH)

    if truncated:
        log.warning("discussion %s truncated at depth=%d posts=%d",
                    root_id, max_depth_seen, len(ids))

    # load all post objects, build ref-map
    t_posts = perf_counter()
    posts = await load_posts_keyed(db, ids)
    ms_posts = (perf_counter() - t_posts) * 1000

    if ms_tree > 500 or ms_posts > 500:
        log.warning(
            "[DISCUSSION_BREAKDOWN] root_id=%d tree_walk=%.0fms(%d queries, "
            "depth=%d posts=%d) load_posts=%.0fms",
            root_id, ms_tree, 1, max_depth_seen, len(ids), ms_posts)

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
