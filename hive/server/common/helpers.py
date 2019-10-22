"""Helpers for server/API functions."""

import re
from functools import wraps
import traceback
import logging

log = logging.getLogger(__name__)

class ApiError(Exception):
    """API-specific errors: unimplemented/bad params. Pass back to client."""
    # pylint: disable=unnecessary-pass
    pass

def return_error_info(function):
    """Async API method decorator which catches and formats exceptions."""
    @wraps(function)
    async def wrapper(*args, **kwargs):
        """Catch ApiError and AssersionError (always due to user error)."""
        try:
            return await function(*args, **kwargs)
        except (ApiError, AssertionError, TypeError, Exception) as e:
            if isinstance(e, ApiError) and 'get_account_votes' in str(e):
                raise e
            if isinstance(e, AssertionError) and 'community not found' in str(e):
                raise e
            if isinstance(e, TypeError) and 'unexpected keyword' not in str(e):
                # one specific TypeError we want to silence; others need a trace
                log.error("ERR1: %s\n%s", repr(e), traceback.format_exc())
                raise e
            if isinstance(e, AssertionError):
                log.error("ERR2: %s\n%s", repr(e), traceback.format_exc())
                raise e
            log.error("ERR0: %s\n%s", repr(e), traceback.format_exc())
            raise e
            #return {
            #    "error": {
            #        "code": -32000,
            #        "message": repr(e) + " (hivemind-beta)",
            #        "trace": traceback.format_exc()}}
    return wrapper

def valid_account(name, allow_empty=False):
    """Returns validated account name or throws Assert."""
    if not name:
        assert allow_empty, 'invalid account (not specified)'
        return ""
    assert isinstance(name, str), "invalid account name type"
    assert 3 <= len(name) <= 16, "invalid account name length: `%s`" % name
    assert name[0] != '@', "invalid account name char `@`"
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
    # TODO: differentiate valid sorts on comm vs tag
    valid_sorts = ['trending', 'promoted', 'hot', 'created',
                   'payout', 'payout_comments', 'muted']
    assert sort in valid_sorts, 'invalid sort `%s`' % sort
    return sort

def valid_tag(tag, allow_empty=False):
    """Returns validated tag or throws Assert."""
    if not tag:
        assert allow_empty, 'tag was blank'
        return ""
    assert isinstance(tag, str), 'tag must be a string'
    assert re.match('^[a-z0-9-_]+$', tag), 'invalid tag `%s`' % tag
    return tag

def valid_limit(limit, ubound=100):
    """Given a user-provided limit, return a valid int, or raise."""
    assert limit is not None, 'limit must be provided'
    limit = int(limit)
    assert limit > 0, "limit must be positive"
    assert limit <= ubound, "limit exceeds max (%d > %d)" % (limit, ubound)
    return limit

def valid_offset(offset, ubound=None):
    """Given a user-provided offset, return a valid int, or raise."""
    offset = int(offset)
    assert offset >= -1, "offset cannot be negative"
    if ubound is not None:
        assert offset <= ubound, "offset too large"
    return offset

def valid_follow_type(follow_type: str):
    """Ensure follow type is valid steemd type."""
    assert follow_type in ['blog', 'ignore'], 'invalid follow_type `%s`' % follow_type
    return follow_type
