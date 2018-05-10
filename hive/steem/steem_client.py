"""Tight and reliable steem API client for hive indexer."""

import time
from decimal import Decimal

from hive.conf import Conf
from hive.utils.normalize import parse_time, parse_amount, steem_amount, vests_amount
from hive.steem.http_client import HttpClient, RPCError
from hive.steem.client_stats import ClientStats

class SteemClient:
    """Handles upstream calls to jussi/steemd, with batching and retrying."""

    _instance = None

    @classmethod
    def instance(cls):
        if not cls._instance:
            cls._instance = SteemClient(
                url=Conf.get('steemd_url'),
                max_batch=Conf.get('max_batch'),
                max_workers=Conf.get('max_workers'))
        return cls._instance

    def __init__(self, url, max_batch=500, max_workers=1):
        assert url, 'steem-API endpoint undefined'
        assert max_batch > 0 and max_batch <= 5000
        assert max_workers > 0 and max_workers <= 500

        use_appbase = False # until deployed, assume False
        if url[-8:] == '#appbase':
            use_appbase = True
            url = url[:-8]

        self._max_batch = max_batch
        self._max_workers = max_workers
        self._client = HttpClient(nodes=[url],
                                  maxsize=50,
                                  num_pools=50,
                                  use_appbase=use_appbase)

        print("[STEEM] init url:%s batch:%s workers:%d appbase:%s"
              % (url, max_batch, max_workers, use_appbase))

    def get_accounts(self, accounts):
        assert accounts, "no accounts passed to get_accounts"
        ret = self.__exec('get_accounts', accounts)
        assert len(accounts) == len(ret), ("requested %d accounts got %d"
                                           % (len(accounts), len(ret)))
        return ret

    def get_content_batch(self, tuples):
        posts = self.__exec_batch('get_content', tuples)
        # TODO: how are we ensuring sequential results? need to set and sort id.
        for post in posts: # sanity-checking jussi responses
            assert 'author' in post, "invalid post: {}".format(post)
        return posts

    def get_block(self, num):
        #assert num == int(block['block_id'][:8], base=16)
        result = self.__exec('get_block', {'block_num': num})
        assert 'block' in result, "result has no 'block' key: {}".format(result)
        return result['block']

    def get_block_simple(self, block_num):
        block = self.get_block(block_num)
        return {'num': int(block['block_id'][:8], base=16),
                'date': parse_time(block['timestamp']),
                'hash': block['block_id']}

    def stream_blocks(self, start_from, trail_blocks=0, max_gap=40):
        """ETA-based block follower."""
        assert trail_blocks >= 0
        assert trail_blocks <= 100

        last = self.get_block_simple(start_from - 1)
        head_num = self.head_block()
        next_expected = time.time()

        start_head = head_num
        lag_secs = 1
        queue = []
        while True:
            assert not last['num'] > head_num

            # if slots missed, advance head block
            time_now = time.time()
            while time_now >= next_expected + lag_secs:
                head_num += 1
                next_expected += 3

                # check we're not too far behind
                gap = (head_num - last['num']) - trail_blocks
                print("[LIVE] %d blocks behind..." % gap)
                if gap > max_gap:
                    print("[LIVE] gap too large: %d" % gap)
                    return # return to fast-sync

            # if caught up, await head advance.
            if head_num == last['num']:
                time.sleep(next_expected + lag_secs - time_now)
                head_num += 1
                next_expected += 3

            # get the target block; if DNE, pause and retry
            block_num = last['num'] + 1
            block = self.get_block(block_num)
            if not block:
                lag_secs = min(3, lag_secs + 0.1) # tune inter-slot timing
                print("[LIVE] block %d not available. hive:%d steem:%d. lag:%f"
                      % (block_num, head_num, self.head_block(), lag_secs))
                time.sleep(0.5)
                continue
            lag_secs -= 0.001 # timing forward creep
            last['num'] = block_num

            # if block doesn't link, we're forked
            if last['hash'] != block['previous']:
                if queue: # using trail_blocks, fork might not be in db
                    print("[FORK] Fork in queue; emptying to retry.")
                    return
                raise Exception("[FORK] Fork in db: from %s, %s->%s" % (
                    last['hash'], block['previous'], block['block_id']))
            last['hash'] = block['block_id']

            # detect missed blocks, adjust schedule
            block_date = parse_time(block['timestamp'])
            miss_secs = (block_date - last['date']).seconds - 3
            if miss_secs and last['num'] >= start_head:
                print("[LIVE] %d missed blocks"
                      % (miss_secs / 3))
                next_expected += miss_secs
                lag_secs = 1
            last['date'] = block_date

            # buffer block yield
            queue.append(block)
            if len(queue) > trail_blocks:
                yield queue.pop(0)


    def _gdgp(self):
        ret = self.__exec('get_dynamic_global_properties')
        assert 'time' in ret, "gdgp invalid resp: {}".format(ret)
        return ret

    def head_time(self):
        return self._gdgp()['time']

    def head_block(self):
        return self._gdgp()['head_block_number']

    def last_irreversible(self):
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
        orders = self.__exec('get_order_book', 1)
        ask = Decimal(orders['asks'][0]['real_price'])
        bid = Decimal(orders['bids'][0]['real_price'])
        price = (ask + bid) / 2
        return "%.6f" % price

    def get_blocks_range(self, lbound, ubound):
        """Retrieves blocks in the range of [lbound, ubound)."""
        block_nums = range(lbound, ubound)
        required = set(block_nums)
        available = set()
        missing = required - available
        blocks = {}

        while missing:
            for result in self.__exec_batch('get_block', [{'block_num': i} for i in missing]):
                block = result['block']
                if not 'block_id' in block:
                    print("WARNING: invalid block returned: {}".format(block))
                    continue
                num = int(block['block_id'][:8], base=16)
                if num in blocks:
                    print("WARNING: batch get_block returned dupe %d" % num)
                blocks[num] = block
            available = set(blocks.keys())
            missing = required - available
            if missing:
                print("WARNING: API missed blocks {}".format(missing))
                time.sleep(3)

        return [blocks[x] for x in block_nums]


    def __exec(self, method, params=None):
        """Perform a single steemd call."""
        time_start = time.perf_counter()
        tries = 0
        while True:
            try:
                result = self._client.exec(method, params or dict())
                if method != 'get_block':
                    assert result, "empty response {}".format(result)
            except (AssertionError, RPCError) as e:
                tries += 1
                print("{} failure, retry in {}s -- {}".format(method, tries / 10, e))
                time.sleep(tries / 10)
                continue
            break

        batch_size = len(params[0]) if method == 'get_accounts' else 1
        total_time = (time.perf_counter() - time_start) * 1000
        ClientStats.log(method, total_time, batch_size)
        return result

    def __exec_batch(self, method, params):
        """Perform batch call. Based on config uses either batch or futures."""
        time_start = time.perf_counter()
        result = None

        if self._max_workers == 1:
            result = self.__exec_batch_with_retry(method, params, self._max_batch)
        else:
            result = list(self._client.exec_multi_with_futures(
                method, params, max_workers=self._max_workers))

        total_time = (time.perf_counter() - time_start) * 1000
        ClientStats.log(method, total_time, len(params))
        return result

    def __exec_batch_with_retry(self, method, params, batch_size):
        """Perform a json-rpc batch request, retrying on error."""
        tries = 0
        while True:
            try:
                return list(self._client.exec_batch(method, params, batch_size))
            except (AssertionError, RPCError) as e:
                tries += 1
                print("batch {} failure, retry in {}s -- {}".format(method, tries / 10, repr(e)))
                time.sleep(tries / 10)
                continue
