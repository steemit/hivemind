#pylint: disable=missing-docstring
#pylint: disable=redefined-outer-name
import datetime
import pytest

from hive.utils.normalize import parse_time
from hive.steem.client import SteemClient

@pytest.fixture
def client():
    return SteemClient(url='https://api.steemit.com')

def test_instance(client):
    assert isinstance(client, SteemClient)

def test_get_accounts(client):
    accounts = client.get_accounts(['steemit', 'test-safari'])
    assert len(accounts) == 2
    assert accounts[0]['name'] == 'steemit'

def test_get_content_batch(client):
    tuples = [('test-safari', 'may-spam'), ('test-safari', 'june-spam')]
    posts = client.get_content_batch(tuples)
    assert len(posts) == 2
    assert posts[0]['author'] == 'test-safari'
    assert posts[1]['author'] == 'test-safari'

def test_get_block(client):
    block = client.get_block(23494494)
    assert block['block_id'] == '01667f5e194c421aa00eb02270d3219a5d9bf339'

def test_stream_blocks(client):
    start_at = client.last_irreversible()
    stop_at = client.head_block() + 2
    streamed = 0
    with pytest.raises(KeyboardInterrupt):
        for block in client.stream_blocks(start_at, trail_blocks=0, max_gap=100):
            assert 'block_id' in block
            num = int(block['block_id'][:8], base=16)
            assert num == start_at + streamed
            streamed += 1
            if streamed >= 20 and num >= stop_at:
                raise KeyboardInterrupt
    assert streamed >= 20
    assert num >= stop_at

def test_head_time(client):
    head = parse_time(client.head_time())
    assert head > datetime.datetime.now() - datetime.timedelta(minutes=15)

def test_head_block(client):
    assert client.head_block() > 23e6

def test_last_irreversible(client):
    assert client.last_irreversible() > 23e6

def test_gdgp_extended(client):
    ret = client.gdgp_extended()
    assert 'dgpo' in ret
    assert 'head_block_number' in ret['dgpo']
    assert 'usd_per_steem' in ret

def test_get_blocks_range(client):
    lbound = 23000000
    blocks = client.get_blocks_range(lbound, lbound + 5)
    assert len(blocks) == 5
