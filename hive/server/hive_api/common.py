"""Hive API: Internal supporting methods"""
import logging

from hive.server.condenser_api.common import (
    valid_account,
    valid_permlink,
    valid_limit)

log = logging.getLogger(__name__)

def __used_refs():
    # pylint
    valid_account('')
    valid_permlink('')
    valid_limit('')

def estimated_sp(vests):
    """Convert VESTS to SP units for display."""
    return vests * 0.0005034

VALID_SORTS = ['trending', 'created', 'hot', 'payout']
def valid_sort(sort):
    """Validate and return provided `sort`, otherwise throw."""
    assert isinstance(sort, str), 'sort was not a string'
    assert sort in VALID_SORTS, 'invalid sort `%s`' % sort
    return sort

VALID_COMMENT_SORTS = [
    'hot'  # hot algo
    'top', # payout
    'new', # newest
    #'votes', # highest number of votes (excludes comm. muted?)
]

def valid_comment_sort(sort):
    """Validate and return provided `sort`, otherwise throw."""
    assert isinstance(sort, str), 'sort was not a string'
    assert sort in VALID_COMMENT_SORTS, 'invalid sort `%s`' % sort
    return sort

def split_url(url, allow_empty=False):
    """Validate and split a post url into author/permlink."""
    if not url:
        assert allow_empty, 'url must be specified'
        return None
    assert isinstance(url, str), 'url must be a string'

    parts = url.split('/')
    assert len(parts) == 2, 'invalid url parts'

    author = valid_account(parts[0])
    permlink = valid_permlink(parts[1])
    return (author, permlink)

async def url_to_id(db, url):
    """Get post_id based on post url."""
    pid = await get_post_id(db, *split_url(url))
    assert pid, 'post id not found'
    return pid

async def get_post_id(db, author, permlink):
    """Get post_id based on author/permlink."""
    sql = "SELECT id FROM hive_posts WHERE author = :a AND permlink = :p"
    return await db.query_one(sql, a=author, p=permlink)

async def get_account_id(db, name):
    """Get account id from account name."""
    assert name, 'no account name specified'
    _id = await db.query_one("SELECT id FROM hive_accounts WHERE name = :n", n=name)
    assert _id, "account not found: `%s`" % name
    return _id
