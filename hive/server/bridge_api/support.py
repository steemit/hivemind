"""Handles building condenser-compatible response objects."""

import logging
#import ujson as json

from hive.server.bridge_api.objects import _condenser_post_object
from hive.utils.post import post_to_internal
from hive.utils.normalize import sbd_amount
from hive.server.common.helpers import (
    #ApiError,
    return_error_info)

log = logging.getLogger(__name__)

ROLES = {-2: 'muted', 0: 'guest', 2: 'member', 4: 'admin', 6: 'mod', 8: 'admin'}

@return_error_info
async def get_post_header(context, author, permlink):
    """Fetch basic post data"""
    db = context['db']

    sql = """SELECT id, parent_id, author, permlink, category, depth
               FROM hive_posts
              WHERE author = :author AND permlink = :permlink"""
    row = await db.query_row(sql, author=author, permlink=permlink)

    return dict(
        author=row['author'],
        permlink=row['permlink'],
        category=row['category'],
        depth=row['depth'])


@return_error_info
async def normalize_post(context, post):
    """Takes a steemd post object and outputs bridge-api normalized version."""

    #post['category'] = core['category']
    #post['community_id'] = core['community_id']
    #post['gray'] = core['is_muted']
    #post['hide'] = not core['is_valid']

    db = context['db']

    # load core md
    sql = """SELECT id, category, community, is_muted, is_valid
               FROM hive_posts
              WHERE author = :author AND permlink = :permlink"""
    core = await db.query_row(sql, author=post['author'], permlink=post['permlink'])

    # load community
    sql = """SELECT id, title FROM hive_communities WHERE name = :name"""
    community = await db.query_row(sql, name=core['community'])

    # load author
    sql = """SELECT id, reputation FROM hive_accounts WHERE name = :name"""
    author = await db.query_row(sql, name=post['author'])

    # append core md
    post['category'] = core['category']
    post['community_id'] = community['id'] if community else None
    post['gray'] = core['is_muted']
    post['hide'] = not core['is_valid']

    promoted = sbd_amount(post['promoted']) if post['promoted'] != '0.000 STEEM' else None

    # convert to internal object
    row = post_to_internal(post, core['id'], level='insert', promoted=promoted)
    row = dict(row)
    if 'promoted' not in row: row['promoted'] = 0
    row['author_rep'] = author['reputation']
    print("GOING>>>%s" % row)
    ret = _condenser_post_object(row)

    # decorate
    if community:
        sql = """SELECT role_id, title
                   FROM hive_roles
                  WHERE community_id = :cid
                    AND account_id = :aid"""
        role = await db.query_row(sql, cid=community['id'], aid=author['id']) or (0, '')

        ret['community_title'] = community['title']
        ret['author_role'] = ROLES[role[0] or 0]
        ret['author_title'] = role[1] or ''


    #sql = """SELECT id, is_pinned
    #           FROM hive_posts WHERE id IN :ids"""
    #for row in await db.query_all(sql, ids=tuple(ids), cids=tuple(ctx.keys())):
    #    if row['id'] in posts_by_id:
    #        post = posts_by_id[row['id']]
    #        post['stats']['is_pinned'] = row['is_pinned']

    return ret
