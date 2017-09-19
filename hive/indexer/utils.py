import os
import time

from datetime import datetime
from steem import utils as steem_utils
from steembase.http_client import HttpClient

def amount(str):
    return float(str.split(' ')[0])

def parse_time(block_time):
    return datetime.strptime(block_time, '%Y-%m-%dT%H:%M:%S')

def json_expand(json_op, key_name='json'):
    return steem_utils.json_expand(json_op, key_name)


_shared_adapter = None
def get_adapter():
    global _shared_adapter
    if not _shared_adapter:
        url = os.environ.get('STEEMD_URL')
        assert url, 'STEEMD_URL undefined'
        _shared_adapter = SteemAdapter(url)
    return _shared_adapter


class SteemAdapter:

    def __init__(self, api_endpoint):
        self._client = HttpClient(nodes=[api_endpoint])

    def get_accounts(self, accounts):
        return self.__exec('get_accounts', accounts)

    def get_content(self, account, permlink):
        return self.__exec('get_content', account, permlink)

    def get_block(self, num):
        return self.__exec('get_block', num)

    def _gdgp(self):
        return self.__exec('get_dynamic_global_properties')

    def head_time(self):
        return self._gdgp()['time']

    def head_block(self):
        return self._gdgp()['head_block_number']

    def last_irreversible_block_num(self):
        return self._gdgp()['last_irreversible_block_num']

    # https://github.com/steemit/steem-python/blob/master/steem/steemd.py
    def get_blocks_range(self, lbound, ubound): # [lbound, ubound)
        block_nums = range(lbound, ubound)
        required = set(block_nums)
        available = set()
        missing = required - available
        blocks = {}

        while missing:
            for block in self._get_blocks(missing):
                blocks[int(block['block_id'][:8], base=16)] = block

            available = set(blocks.keys())
            missing = required - available
            if missing:
                print("WARNING: API missed blocks {}".format(missing))
                time.sleep(3)

        return [blocks[x] for x in block_nums]

    def _get_blocks(self, blocks):
        return self._client.exec_multi_with_futures('get_block', blocks, max_workers=10)

    def __exec(self, method, *params):
        return self._client.exec(method, *params)

