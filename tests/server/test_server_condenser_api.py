#pylint: disable=missing-docstring
import pytest
from hive.server.condenser_api.get_state import get_state
from hive.server.condenser_api.tags import get_trending_tags
from hive.server.condenser_api.call import call

@pytest.mark.asyncio
async def test_get_state():
    ret = await get_state('/trending')
    assert 'discussion_idx' in ret

    assert await get_state('trending')
    assert await get_state('promoted')
    assert await get_state('created')
    assert await get_state('hot')

    assert await get_state('@test-safari')
    assert await get_state('@test-safari/feed')
    assert await get_state('@test-safari/comments')
    assert await get_state('@test-safari/recent-replies')

    assert await get_state('spam/@test-safari/1ncq2-may-spam')
    assert await get_state('spam/@test-safari/october-spam')

    assert await get_state('trending/blockchain')

    assert await get_state('tags')

    with pytest.raises(AssertionError):
        await get_state('trending/blockchain/xxx')

    with pytest.raises(AssertionError):
        await get_state('tags/xxx')

    with pytest.raises(Exception):
        await get_state('witnesses')

@pytest.mark.asyncio
async def test_call():
    assert await call('condenser_api',
                      'get_followers',
                      ['test-safari', '', 'blog', 10])
    assert await call('condenser_api',
                      'get_discussions_by_blog',
                      [{"tag": "test-safari",
                        "start_author": "",
                        "start_permlink": "",
                        "limit": 10}])

@pytest.mark.asyncio
async def test_get_trending_tags():
    full = await get_trending_tags()
    assert full

    # blank params should result in same order
    short = await get_trending_tags('', 10)
    assert full[3] == short[3]

    # ensure pagination works
    paged = await get_trending_tags(full[2], 2)
    assert full[3] == paged[1]
