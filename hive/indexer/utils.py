import os
from datetime import datetime
from steem import utils as steem_utils
from steem.steemd import Steemd

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
        if not url:
            raise "STEEMD_URL undefined"
        _shared_adapter = SteemAdapter(url)
    return _shared_adapter


class SteemAdapter:

    def __init__(self, api_endpoint):
        self.steemd = Steemd(nodes=[api_endpoint])

    def get_content(self, account, permlink):
        return self.steemd.get_content(account, permlink)

    def get_block(self, num):
        return self.steemd.get_block(num)

    def get_blocks(lbound, ubound): # [lbound, ubound)
        return self.steemd.get_blocks_range(lbound, ubound)

    def head_block(self):
        return self.gdgp()['head_block_number']

    def head_time(self):
        return self.gdgp()['time']

    def last_irreversible_block_num(self):
        return self.gdgp()['last_irreversible_block_num']

    def gdgp(self):
        return self.steemd.get_dynamic_global_properties()

