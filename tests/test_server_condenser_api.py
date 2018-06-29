#pylint: disable=missing-docstring
import pytest
from hive.server.condenser_api.get_state import get_state

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

    assert await get_state('trending/blockchain')

    assert await get_state('tags')

    with pytest.raises(AssertionError):
        await get_state('trending/blockchain/xxx')

    with pytest.raises(AssertionError):
        await get_state('tags/xxx')

    with pytest.raises(Exception):
        await get_state('witnesses')
