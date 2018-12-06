"""Helpers for condenser_api calls."""

import re
from functools import wraps

from hive.db.methods import query_one, query_col

class ApiError(Exception):
    """API-specific errors: unimplemented/bad params. Pass back to client."""
    pass

def return_error_info(function):
    """Async API method decorator which catches and formats exceptions."""
    @wraps(function)
    async def wrapper(*args, **kwargs):
        """Catch ApiError and AssersionError (always due to user error)."""
        try:
            return await function(*args, **kwargs)
        except (ApiError, AssertionError) as e:
            return {
                "error": {
                    "code": -32000,
                    "message": str(e) + " (hivemind-alpha)"}}
    return wrapper

def valid_account(name, allow_empty=False):
    """Returns validated account name or throws Assert."""
    if not name:
        assert allow_empty, 'account must be specified'
        return ""
    assert isinstance(name, str), "account must be string; received: %s" % name
    assert len(name) >= 3 and len(name) <= 16, "invalid account: %s" % name
    assert re.match(r'^[a-z0-9-\.]+$', name), 'invalid account char'
    return name

def valid_permlink(permlink, allow_empty=False):
    """Returns validated permlink or throws Assert."""
    if not permlink:
        assert allow_empty, 'permlink cannot be blank'
        return ""
    assert isinstance(permlink, str), 'permlink must be string'
    assert len(permlink) <= 256, "invalid permlink length"
    return permlink

def valid_sort(sort, allow_empty=False):
    """Returns validated sort name or throws Assert."""
    if not sort:
        assert allow_empty, 'sort must be specified'
        return ""
    assert isinstance(sort, str), 'sort must be a string'
    valid_sorts = ['trending', 'promoted', 'hot', 'created']
    assert sort in valid_sorts, 'invalid sort'
    return sort

def valid_tag(tag, allow_empty=False):
    """Returns validated tag or throws Assert."""
    if not tag:
        assert allow_empty, 'tag was blank'
        return ""
    assert isinstance(tag, str), 'tag must be a string'
    assert re.match('^[a-z0-9-]+$', tag), 'invalid tag'
    return tag

def valid_limit(limit, ubound=100):
    """Given a user-provided limit, return a valid int, or raise."""
    limit = int(limit)
    assert limit > 0, "limit must be positive"
    assert limit <= ubound, "limit exceeds max"
    return limit

def get_post_id(author, permlink):
    """Given an author/permlink, retrieve the id from db."""
    sql = ("SELECT id FROM hive_posts WHERE author = :a "
           "AND permlink = :p AND is_deleted = '0' LIMIT 1")
    return query_one(sql, a=author, p=permlink)

def get_child_ids(post_id):
    """Given a parent post id, retrieve all child ids."""
    sql = "SELECT id FROM hive_posts WHERE parent_id = %d AND is_deleted = '0'"
    return query_col(sql % post_id)
