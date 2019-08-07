"""Handles legacy `call` method."""

from hive.server.condenser_api.common import (
    ApiError,
    return_error_info,
)
from hive.server.bridge_api.get_state import get_state
from hive.server.bridge_api.tags import get_trending_tags
from hive.server.bridge_api.methods import (
    get_discussions_by_trending,
    get_discussions_by_hot,
    get_discussions_by_promoted,
    get_discussions_by_created,
    get_discussions_by_blog,
    get_discussions_by_feed,
    get_discussions_by_comments,
    get_replies_by_last_update,

    get_post_discussions_by_payout,
    get_comment_discussions_by_payout,
)

def _strict_list(params, expected_len, min_len=None):
    assert isinstance(params, list), "params not a list"
    if min_len is None:
        assert len(params) == expected_len, "expected %d params" % expected_len
    else:
        assert (len(params) <= expected_len and
                len(params) >= min_len), "expected %d params" % expected_len
    return params

def _strict_query(params):
    query = _strict_list(params, 1)[0]
    assert isinstance(query, dict), "query must be dict"

    # remove optional-yet-blank param keys -- some clients include every key
    # possible, and steemd seems to ignore them silently. need to strip
    # them here, if blank, to avoid argument mismatch errors.
    all_keys = ['author',
                'start_author', 'start_permlink', 'start_tag', 'parent_author',
                'parent_permlink', 'start_parent_author', 'before_date', 'tag']
    for key in all_keys:
        if key in query and not query[key]:
            del query[key]

    optional_keys = set(['truncate_body', 'start_author', 'start_permlink', 'tag'])
    expected_keys = set(['limit'])

    provided_keys = query.keys()
    missing = expected_keys - provided_keys
    unknown = provided_keys - expected_keys - optional_keys
    assert not missing, "missing query key %s" % missing
    assert not unknown, "unknown query key %s" % unknown

    return query

@return_error_info
async def call(context, api, method, params):
    """Routes legacy-style `call` method requests.

    Example:
    ```
    {"id":0,"jsonrpc":"2.0","method":"call2",
     "params":["database_api","get_state",["trending"]]}
    ```"""
    # pylint: disable=too-many-return-statements, too-many-branches
    assert api == 'condenser_api', "`call` requires condenser_api"

    # Content monolith
    if method == 'get_state':
        return await get_state(context, *_strict_list(params, 1))
    elif method == 'get_trending_tags':
        return await get_trending_tags(context, *_strict_list(params, 2))

    # Global discussion queries
    elif method == 'get_discussions_by_trending':
        return await get_discussions_by_trending(context, **_strict_query(params))
    elif method == 'get_discussions_by_hot':
        return await get_discussions_by_hot(context, **_strict_query(params))
    elif method == 'get_discussions_by_promoted':
        return await get_discussions_by_promoted(context, **_strict_query(params))
    elif method == 'get_discussions_by_created':
        return await get_discussions_by_created(context, **_strict_query(params))
    elif method == 'get_post_discussions_by_payout':
        return await get_post_discussions_by_payout(context, **_strict_query(params))
    elif method == 'get_comment_discussions_by_payout':
        return await get_comment_discussions_by_payout(context, **_strict_query(params))

    # Account discussion queries
    elif method == 'get_discussions_by_blog':
        return await get_discussions_by_blog(context, **_strict_query(params))
    elif method == 'get_discussions_by_feed':
        return await get_discussions_by_feed(context, **_strict_query(params))
    elif method == 'get_discussions_by_comments':
        return await get_discussions_by_comments(context, **_strict_query(params))
    elif method == 'get_replies_by_last_update':
        return await get_replies_by_last_update(context, *_strict_list(params, 3))

    raise ApiError("[call2] unknown method: %s.%s" % (api, method))
