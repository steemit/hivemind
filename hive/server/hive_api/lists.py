"""Hive API: various post list methods"""

import logging

from hive.server.hive_api.post import posts_by_id
from hive.server.condenser_api.cursor import pids_by_blog, pids_by_account_comments
# TODO: import valid_*s

log = logging.getLogger(__name__)

async def list_account_blog(context, account, start='', limit=10, observer=None):
    """Get a blog feed (posts and reblogs from the specified account)"""
    db = context['db']
    start_author, start_permlink = start.split('/')
    post_ids = pids_by_blog(db, account, start_author, start_permlink, limit)
    return posts_by_id(db, post_ids, observer)

async def list_account_posts(context, account, start='', limit=10, observer=None):
    """Get an account's posts and comments"""
    db = context['db']
    start_author, start_permlink = start.split('/')
    assert start_author == account
    post_ids = pids_by_account_comments(db, account, start_permlink, limit)
    return posts_by_id(db, post_ids, observer)
