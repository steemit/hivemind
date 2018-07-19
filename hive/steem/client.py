"""Tight and reliable steem API client for hive indexer."""

import time
from decimal import Decimal

from hive.utils.stats import Stats
from hive.utils.normalize import parse_amount, steem_amount, vests_amount
from hive.steem.http_client import HttpClient
from hive.steem.block.stream import BlockStream

class SteemClient:
    """Handles upstream calls to jussi/steemd, with batching and retrying."""

    def __init__(self, url, max_batch=500, max_workers=1):
        assert url, 'steem-API endpoint undefined'
        assert max_batch > 0 and max_batch <= 5000
        assert max_workers > 0 and max_workers <= 64

        self._max_batch = max_batch
        self._max_workers = max_workers
        self._client = HttpClient(nodes=[url])

    def get_accounts(self, accounts):
        """Fetch multiple accounts by name."""
        assert accounts, "no accounts passed to get_accounts"
        assert len(accounts) <= 1000, "max 1000 accounts"
        ret = self.__exec('get_accounts', [accounts])
        assert len(accounts) == len(ret), ("requested %d accounts got %d"
                                           % (len(accounts), len(ret)))
        return ret

    def get_content_batch(self, tuples):
        """Fetch multiple comment objects."""
        posts = self.__exec_batch('get_content', tuples)
        # TODO: how are we ensuring sequential results? need to set and sort id.
        for post in posts: # sanity-checking jussi responses
            assert 'author' in post, "invalid post: %s" % post
        return posts

    def get_block(self, num):
        """Fetches a single block.

        If the result does not contain a `block` key, it's assumed
        this block does not yet exist and None is returned.
        """
        result = self.__exec('get_block', {'block_num': num})
        return result['block'] if 'block' in result else None

    def stream_blocks(self, start_from, trail_blocks=0, max_gap=100):
        """Stream blocks. Returns a generator."""
        return BlockStream.stream(self, start_from, trail_blocks, max_gap)

    def _gdgp(self):
        ret = self.__exec('get_dynamic_global_properties')
        assert 'time' in ret, "gdgp invalid resp: %s" % ret
        return ret

    def head_time(self):
        """Get timestamp of head block"""
        return self._gdgp()['time']

    def head_block(self):
        """Get head block number"""
        return self._gdgp()['head_block_number']

    def last_irreversible(self):
        """Get last irreversible block"""
        return self._gdgp()['last_irreversible_block_num']

    def gdgp_extended(self):
        """Get dynamic global props without the cruft plus useful bits."""
        dgpo = self._gdgp()

        # remove unused/deprecated keys
        unused = ['total_pow', 'num_pow_witnesses', 'confidential_supply',
                  'confidential_sbd_supply', 'total_reward_fund_steem',
                  'total_reward_shares2']
        for key in unused:
            del dgpo[key]

        return {
            'dgpo': dgpo,
            'usd_per_steem': self._get_feed_price(),
            'sbd_per_steem': self._get_steem_price(),
            'steem_per_mvest': SteemClient._get_steem_per_mvest(dgpo)}

    @staticmethod
    def _get_steem_per_mvest(dgpo):
        steem = steem_amount(dgpo['total_vesting_fund_steem'])
        mvests = vests_amount(dgpo['total_vesting_shares']) / Decimal(1e6)
        return "%.6f" % (steem / mvests)

    def _get_feed_price(self):
        # TODO: add latest feed price: get_feed_history.price_history[0]
        feed = self.__exec('get_feed_history')['current_median_history']
        units = dict([parse_amount(feed[k])[::-1] for k in ['base', 'quote']])
        price = units['SBD'] / units['STEEM']
        return "%.6f" % price

    def _get_steem_price(self):
        orders = self.__exec('get_order_book', [1])
        ask = Decimal(orders['asks'][0]['real_price'])
        bid = Decimal(orders['bids'][0]['real_price'])
        price = (ask + bid) / 2
        return "%.6f" % price

    def get_blocks_range(self, lbound, ubound):
        """Retrieves blocks in the range of [lbound, ubound)."""
        block_nums = range(lbound, ubound)
        blocks = {}

        batch_params = [{'block_num': i} for i in block_nums]
        for result in self.__exec_batch('get_block', batch_params):
            assert 'block' in result, "result w/o block key: %s" % result
            block = result['block']
            num = int(block['block_id'][:8], base=16)
            blocks[num] = block

        return [blocks[x] for x in block_nums]

    def __exec(self, method, params=None):
        """Perform a single steemd call."""
        time_start = time.perf_counter()
        result = self._client.exec(method, params)
        total_time = (time.perf_counter() - time_start) * 1000

        batch_size = len(params[0]) if method == 'get_accounts' else 1
        Stats.log_steem(method, total_time, batch_size)
        return result

    def __exec_batch(self, method, params):
        """Perform batch call. Based on config uses either batch or futures."""
        time_start = time.perf_counter()

        result = []
        for part in self._client.exec_multi(
                method,
                params,
                max_workers=self._max_workers,
                batch_size=self._max_batch):
            result.extend(part)

        total_time = (time.perf_counter() - time_start) * 1000
        Stats.log_steem(method, total_time, len(params))
        return result
