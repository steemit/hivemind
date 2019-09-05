"""Hive API: Threaded discussion handling"""
import logging

from hive.server.hive_api.common import url_to_id, valid_comment_sort, valid_limit
from hive.server.hive_api.objects import comments_by_id
log = logging.getLogger(__name__)

# pylint: disable=too-many-arguments

async def fetch_tree(context, root, sort='top', limit=20, observer=None):
    """Fetch comment tree. Includes comments and lite author data.

    If community: follows/applies mod rules
    If blog: hides comments by any muted accounts of the author's
    Sort: new, old, hot, payout"""
    db = context['db']
    root_id = await url_to_id(db, root)
    return await _fetch_children(db, root_id, None,
                                 valid_comment_sort(sort),
                                 valid_limit(limit, 50),
                                 observer)

async def fetch_more_children(context, root_id, last_sibling_id, sort='top',
                              limit=20, observer=None):
    """Fetch truncated siblings from tree."""
    db = context['db']
    return await _fetch_children(db, root_id, last_sibling_id,
                                 valid_comment_sort(sort),
                                 valid_limit(limit, 50),
                                 observer)

_SORTS = dict(hot='sc_hot', top='payout', new='post_id')
async def _fetch_children(db, root_id, start_id, sort, limit, observer=None):
    """Fetch truncated children from tree."""
    mutes = set() #TODO: Mutes.all(), author mutes
    field = _SORTS[sort]

    # load id skeleton
    tree, parent = await _load_tree(db, root_id, mutes, max_depth=3)

    # find most relevant ids in subset
    seek = ''
    if start_id:
        seek = """AND %s < (SELECT %s FROM hive_posts_cache
                             WHERE post_id = :start_id)""" % (field, field)
    sql = """SELECT post_id FROM hive_posts_cache
              WHERE post_id IN :ids %s ORDER BY %s DESC
              LIMIT :limit""" % (seek, field)
    relevant_ids = await db.query_col(sql, ids=tuple(parent.keys()),
                                      start_id=start_id, limit=limit)

    # fill in missing parents
    for _id in relevant_ids:
        if _id != root_id:
            if parent[_id] not in relevant_ids:
                relevant_ids.append(parent[_id])

    # load objects and assemble response tree
    comments = await comments_by_id(db, relevant_ids, observer)

    return {'accounts': comments['accounts'],
            'posts': _build_tree(tree[root_id], tree, comments['posts'], sort_ids=relevant_ids)}


def _build_tree(root_ids, tree, comments, sort_ids):
    # comments is sorted...

    # TODO: fetch account role/title, include in response

    ret = []
    for root_id in sorted(root_ids, key=sort_ids.index):
        assert root_id in comments, 'root not loaded'
        out = comments[root_id]
        out['type'] = 'comment'

        if root_id in tree:
            missing = 0
            loaded_ids = []
            for cid in tree[root_id]:
                if cid in comments:
                    assert not missing, 'missing mode: not expected to find'
                    loaded_ids.append(cid)
                else:
                    missing += 1

            if loaded_ids:
                out['children'] = _build_tree(loaded_ids, tree, comments, sort_ids)
            else:
                out['children'] = []
            if missing:
                last_id = loaded_ids[-1] if loaded_ids else None
                out['children'].append({'type': 'more-children',
                                        'root_id': root_id,
                                        'last_id': last_id,
                                        'count': missing})

        ret.append(out)

    return ret


async def _load_tree(db, root_id, muted, max_depth):
    """Build `ids` list and `tree` map."""
    parent = {} # only loaded to max_depth
    tree = {}   # loaded to max_depth + 1
    todo = [root_id]
    depth = 0
    while todo:
        depth += 1
        rows = await _child_ids(db, todo, muted)
        todo = []
        for pid, cids in rows:
            tree[pid] = cids
            todo.extend(cids)
            if depth <= max_depth:
                for cid in cids:
                    parent[cid] = pid
        if depth > max_depth:
            break

    return (tree, parent)

async def _child_ids(db, parent_ids, muted):
    """Load child ids for multiple parent ids."""
    filt = 'AND author NOT IN :muted' if muted else ''
    sql = """
             SELECT parent_id, array_agg(id)
               FROM hive_posts
              WHERE parent_id IN :ids
                AND is_deleted = '0'
                AND is_muted = '0'
                AND is_valid = '1' %s
           GROUP BY parent_id
    """ % filt
    rows = await db.query_all(sql, ids=tuple(parent_ids), muted=tuple(muted))
    return [[row[0], row[1]] for row in rows]
