"""Handles legacy `call` method."""

from hive.server.condenser_api.common import (
    ApiError,
    return_error_info,
)
from hive.server.condenser_api.get_state import get_state
from hive.server.condenser_api.tags import get_trending_tags
from hive.server.condenser_api.methods import (
    get_followers,
    get_following,
    get_follow_count,
    get_content,
    get_content_replies,
    get_discussions_by_trending,
    get_discussions_by_hot,
    get_discussions_by_promoted,
    get_discussions_by_created,
    get_discussions_by_blog,
    get_discussions_by_feed,
    get_discussions_by_comments,
    get_replies_by_last_update,

    get_discussions_by_author_before_date,
    get_blog,
    get_blog_entries,
)

def _strict_list(params, expected_len, min_len=None):
    assert isinstance(params, list), "params not a list"
    if min_len is None:
        assert len(params) == expected_len, "expected %d params" % expected_len
    else:
        assert (len(params) <= expected_len and
                len(params) >= min_len), "expected %d params" % expected_len
    return params

def _strict_query(params, ignore_key=None):
    query = _strict_list(params, 1)[0]
    assert isinstance(query, dict), "query must be dict"

    optional_keys = set(['truncate_body', 'start_author', 'start_permlink'])
    expected_keys = set(['limit', 'tag'])
    if ignore_key: # e.g. `tag` unused by get_discussion_by_comments
        expected_keys = expected_keys - set([ignore_key])

    provided_keys = query.keys()
    missing = expected_keys - provided_keys
    unknown = provided_keys - expected_keys - optional_keys
    assert not missing, "missing query key %s" % missing
    assert not unknown, "unknown query key %s" % unknown

    return query

@return_error_info
async def call(api, method, params):
    """Routes legacy-style `call` method requests.

    Example:
    ```
    {"id":0,"jsonrpc":"2.0","method":"call",
     "params":["database_api","get_state",["trending"]]}
    ```"""
    # pylint: disable=too-many-return-statements, too-many-branches
    assert api == 'condenser_api', "`call` requires condenser_api"

    # Follows
    if method == 'get_followers':
        return await get_followers(*_strict_list(params, 4))
    elif method == 'get_following':
        return await get_following(*_strict_list(params, 4))
    elif method == 'get_follow_count':
        return await get_follow_count(*_strict_list(params, 1))

    # Content primitives
    elif method == 'get_content':
        return await get_content(*_strict_list(params, 2))
    elif method == 'get_content_replies':
        return await get_content_replies(*_strict_list(params, 2))

    # Trending tags
    elif method == 'get_trending_tags':
        return await get_trending_tags(*_strict_list(params, 2))

    # Content monolith
    elif method == 'get_state':
        return await get_state(*_strict_list(params, 1))

    # Global discussion queries
    elif method == 'get_discussions_by_trending':
        return await get_discussions_by_trending(**_strict_query(params))
    elif method == 'get_discussions_by_hot':
        return await get_discussions_by_hot(**_strict_query(params))
    elif method == 'get_discussions_by_promoted':
        return await get_discussions_by_promoted(**_strict_query(params))
    elif method == 'get_discussions_by_created':
        return await get_discussions_by_created(**_strict_query(params))

    # Account discussion queries
    elif method == 'get_discussions_by_blog':
        return await get_discussions_by_blog(**_strict_query(params))
    elif method == 'get_discussions_by_feed':
        return await get_discussions_by_feed(**_strict_query(params))
    elif method == 'get_discussions_by_comments':
        return await get_discussions_by_comments(**_strict_query(params, 'tag'))
    elif method == 'get_replies_by_last_update':
        return await get_replies_by_last_update(*_strict_list(params, 3))

    # Exotic account discussion queries
    elif method == 'get_discussions_by_author_before_date':
        return await get_discussions_by_author_before_date(*_strict_list(params, 4))
    elif method == 'get_blog':
        return await get_blog(*_strict_list(params, 3, 2))
    elif method == 'get_blog_entries':
        return await get_blog_entries(*_strict_list(params, 3, 2))

    raise ApiError("unknown method: %s.%s" % (api, method))
