import os
import time
import atexit
from decimal import Decimal

from .http_client import HttpClient, RPCError

class ClientStats:
    # Assumed HTTP overhead (ms); subtract prior to par check
    PAR_HTTP_OVERHEAD = 60

    # Reporting threshold (x * par)
    PAR_THRESHOLD = 1.1

    # Thresholds for critical call timing (ms)
    PAR_STEEMD = {
        'get_dynamic_global_properties': 50,
        'get_block': 5,
        'get_accounts': 5,
        'get_content': 5,
        'get_order_book': 20,
        'get_feed_history': 20,
    }

    stats = {}
    ttltime = 0.0
    fastest = None

    @classmethod
    def log(cls, method, ms, batch_size=1):
        cls.add_to_stats(method, ms, batch_size)
        cls.check_timing(method, ms, batch_size)
        if cls.fastest is None or ms < cls.fastest:
            cls.fastest = ms
        if cls.ttltime > 30 * 60 * 1000:
            cls.print()

    @classmethod
    def add_to_stats(cls, method, ms, batch_size):
        if method not in cls.stats:
            cls.stats[method] = [ms, batch_size]
        else:
            cls.stats[method][0] += ms
            cls.stats[method][1] += batch_size
        cls.ttltime += ms

    @classmethod
    def check_timing(cls, method, ms, batch_size):
        per = (ms - cls.PAR_HTTP_OVERHEAD) / batch_size
        par = cls.PAR_STEEMD[method]
        over = per / par
        if over >= cls.PAR_THRESHOLD:
            out = ("[STEEM][%dms] %s[%d] -- %.1fx par (%d/%d)"
                   % (ms, method, batch_size, over, per, par))
            print("\033[93m" + out + "\033[0m")

    @classmethod
    def print(cls):
        if not cls.stats:
            return
        ttl = cls.ttltime
        print("[DEBUG] total STEEM time: {}s".format(int(ttl / 1000)))
        for arr in sorted(cls.stats.items(), key=lambda x: -x[1][0])[0:40]:
            sql, vals = arr
            ms, calls = vals
            print("% 5.1f%% % 9sms % 7.2favg % 8dx -- %s"
                  % (100 * ms/ttl, "{:,}".format(int(ms)),
                     ms/calls, calls, sql[0:180]))
        print("Fastest call was %.3fms" % cls.fastest)
        cls.clear()

    @classmethod
    def clear(cls):
        cls.stats = {}
        cls.ttltime = 0

atexit.register(ClientStats.print)

def get_adapter():
    return SteemClient.instance()

class SteemClient:

    _instance = None

    @classmethod
    def instance(cls):
        if not cls._instance:
            steem = os.environ.get('STEEMD_URL')
            jussi = os.environ.get('JUSSI_URL')
            cls._instance = SteemClient(steem, jussi)
        return cls._instance

    def __init__(self, api_endpoint, jussi=None):
        self._jussi = bool(jussi)
        url = jussi or api_endpoint
        assert url, 'steem-API endpoint undefined'
        self._client = HttpClient(nodes=[url], maxsize=50, num_pools=50)

    def get_accounts(self, accounts):
        assert accounts, "no accounts passed to get_accounts"
        ret = self.__exec('get_accounts', accounts)
        assert len(accounts) == len(ret), ("requested %d accounts got %d"
                                           % (len(accounts), len(ret)))
        return ret

    def get_content_batch(self, tuples):
        posts = self.__exec_batch('get_content', tuples)
        for post in posts: # sanity-checking jussi responses
            assert 'author' in post, "invalid post: {}".format(post)
        return posts

    def get_block(self, num):
        #assert num == int(block['block_id'][:8], base=16)
        return self.__exec('get_block', num)

    def _gdgp(self):
        ret = self.__exec('get_dynamic_global_properties')
        assert 'time' in ret, "gdgp invalid resp: {}".format(ret)
        return ret

    def head_time(self):
        return self._gdgp()['time']

    def head_block(self):
        return self._gdgp()['head_block_number']

    def last_irreversible_block_num(self):
        return self._gdgp()['last_irreversible_block_num']

    def gdgp_extended(self):
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
            'steem_per_mvest': self._get_steem_per_mvest(dgpo)}

    def _get_steem_per_mvest(self, dgpo):
        steem = Decimal(dgpo['total_vesting_fund_steem'].split(' ')[0])
        mvests = Decimal(dgpo['total_vesting_shares'].split(' ')[0]) / Decimal(1e6)
        return "%.6f" % (steem / mvests)

    def _get_feed_price(self):
        # TODO: add latest feed price: get_feed_history.price_history[0]
        feed = self.__exec('get_feed_history')['current_median_history']
        units = dict([feed[k].split(' ')[::-1] for k in ['base', 'quote']])
        price = Decimal(units['SBD']) / Decimal(units['STEEM'])
        return "%.6f" % price

    def _get_steem_price(self):
        orders = self.__exec('get_order_book', 1)
        ask = Decimal(orders['asks'][0]['real_price'])
        bid = Decimal(orders['bids'][0]['real_price'])
        price = (ask + bid) / 2
        return "%.6f" % price

    def get_blocks_range(self, lbound, ubound): # [lbound, ubound)
        block_nums = range(lbound, ubound)
        required = set(block_nums)
        available = set()
        missing = required - available
        blocks = {}

        while missing:
            for block in self.__exec_batch('get_block', [[i] for i in missing]):
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


    # perform single steemd call
    def __exec(self, method, *params):
        time_start = time.perf_counter()
        tries = 0
        while True:
            try:
                result = self._client.exec(method, *params)
                if method != 'get_block':
                    assert result, "empty response {}".format(result)
            except (AssertionError, RPCError) as e:
                tries += 1
                print("{} failure, retry in {}s -- {}".format(method, tries, e))
                time.sleep(tries)
                continue
            break

        batch_size = len(params[0]) if method == 'get_accounts' else 1
        total_time = (time.perf_counter() - time_start) * 1000
        ClientStats.log(method, total_time, batch_size)
        return result

    # perform batch call (if jussi is enabled, use batches; otherwise, multi)
    def __exec_batch(self, method, params):
        time_start = time.perf_counter()
        result = None

        if self._jussi:
            result = self.__exec_jussi_batch_with_retry(method, params, 500)
        else:
            result = list(self._client.exec_multi_with_futures(
                method, params, max_workers=50))

        total_time = (time.perf_counter() - time_start) * 1000
        ClientStats.log(method, total_time, len(params))
        return result

    # perform a jussi-style batch request, retrying on error
    def __exec_jussi_batch_with_retry(self, method, params, batch_size=500):
        tries = 0
        while True:
            try:
                return list(self._client.exec_batch(method, params, batch_size))
            except (AssertionError, RPCError) as e:
                tries += 1
                print("batch {} failure, retry in {}s -- {}".format(method, tries / 10, e))
                time.sleep(tries / 10)
                continue
