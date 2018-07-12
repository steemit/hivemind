#pylint: disable=missing-docstring,invalid-name
import pytest

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
)

@pytest.mark.asyncio
async def test_get_followers():
    assert await get_followers('test-safari', '', 'blog', 10)

@pytest.mark.asyncio
async def test_get_following():
    assert await get_following('test-safari', '', 'blog', 10)

@pytest.mark.asyncio
async def test_get_follow_count():
    assert await get_follow_count('test-safari')

@pytest.mark.asyncio
async def test_get_content():
    post = await get_content('test-safari', 'december-spam')
    assert post
    assert post['author'] == 'test-safari'

@pytest.mark.asyncio
async def test_get_content_replies():
    replies = await get_content_replies('test-safari', 'december-spam')
    assert replies
    assert len(replies) > 3
    assert replies[0]['author']

@pytest.mark.asyncio
async def test_nested_query_compat():
    params = dict(start_author='', start_permlink='', limit=10, tag='life', truncate_body=0)
    ret1 = await get_discussions_by_trending(**params)
    arg1 = [params]
    ret2 = await get_discussions_by_trending(*arg1)
    assert ret1 == ret2

@pytest.mark.asyncio
async def test_get_discussions_by_trending():
    assert await get_discussions_by_trending(
        start_author='', start_permlink='', limit=20, tag='', truncate_body=0)

@pytest.mark.asyncio
async def test_get_discussions_by_hot():
    assert await get_discussions_by_hot(
        start_author='', start_permlink='', limit=20, tag='', truncate_body=0)

@pytest.mark.asyncio
async def test_get_discussions_by_promoted():
    assert await get_discussions_by_promoted(
        start_author='', start_permlink='', limit=20, tag='', truncate_body=0)

@pytest.mark.asyncio
async def test_get_discussions_by_created():
    assert await get_discussions_by_created(
        start_author='', start_permlink='', limit=20, tag='', truncate_body=0)

@pytest.mark.asyncio
async def test_get_discussions_by_blog():
    assert await get_discussions_by_blog(
        tag='test-safari', start_author='', start_permlink='', limit=20, truncate_body=0)

@pytest.mark.asyncio
async def test_get_discussions_by_feed():
    assert await get_discussions_by_feed(
        tag='test-safari', start_author='', start_permlink='', limit=20, truncate_body=0)

@pytest.mark.asyncio
async def test_get_discussions_by_comments():
    assert await get_discussions_by_comments(
        start_author='test-safari',
        start_permlink='',
        limit=20,
        truncate_body=0)

@pytest.mark.asyncio
async def test_get_replies_by_last_update():
    assert await get_replies_by_last_update(
        start_author='test-safari',
        start_permlink='',
        limit=20,
        truncate_body=0)
