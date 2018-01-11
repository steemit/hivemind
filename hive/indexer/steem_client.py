import os
import time
import atexit
from decimal import Decimal

from .http_client import HttpClient, RPCError

class ClientStats:
    # Thresholds for critical call timing warnings
    #   int = flat threshold
    #   tuple[0] = timing for single request
    #   tuple[1] = timing for batch of 1000
    PAR = {
        'get_dynamic_global_properties': 120,
        'get_block': (100, 5),
        'get_accounts': (10, 2),
        'get_content': (15, 5),
        'get_order_book': 100,
        'get_current_median_history_price': 80,
    }

    stats = {}
    ttltime = 0.0

    @classmethod
    def log(cls, method, ms, batch_size=1):
        if method not in cls.stats:
            cls.stats[method] = [0, 0]
        cls.stats[method][0] += ms
        cls.stats[method][1] += batch_size
        cls.ttltime += ms
        cls.check_timing(method, ms, batch_size)
        if cls.ttltime > 30 * 60 * 1000:
            cls.print()

    @classmethod
    def check_timing(cls, method, ms, batch_size):
        per = ms / batch_size
        par = cls._par(method, batch_size)

        over = per / par
        if over < 1.25:
            return

        out = ("[STEEM][%dms] %s[%d] -- "
               % (ms, method, batch_size))

        if over > 1:
            out += "%.2fx par (%d/%d)" % (over, per, par)
        else:
            out += "par ok (%d/%d)" % (per, par)

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
            print("% 5.1f%% % 10.2fms % 7.2favg % 8dx -- %s"
                  % (100 * ms/ttl, ms, ms/calls, calls, sql[0:180]))
        cls.clear()

    @classmethod
    def clear(cls):
        cls.stats = {}
        cls.ttltime = 0

    @classmethod
    def _par(cls, method, batch_size):
        par = cls.PAR[method]
        if isinstance(par, tuple):
            x = (batch_size - 1) / (1000 - 1)
            par = round(par[0] + x * (par[1] - par[0]))
        return par

atexit.register(ClientStats.print)

_shared_adapter = None
def get_adapter():
    global _shared_adapter
    if not _shared_adapter:
        steem = os.environ.get('STEEMD_URL')
        jussi = os.environ.get('JUSSI_URL')
        _shared_adapter = SteemClient(steem, jussi)
    return _shared_adapter


class SteemClient:

    def __init__(self, api_endpoint, jussi=None):
        self._jussi = bool(jussi)
        url = jussi or api_endpoint
        assert url, 'steem-API endpoint undefined'
        self._client = HttpClient(nodes=[url])

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
        # TODO: fetch latest feed price in addition to median
        return {
            'dgpo': dgpo,
            'usd_per_steem': self._get_feed_price(),
            'sbd_per_steem': self._get_steem_price(),
            'steem_per_mvest': self._get_steem_per_mvest(dgpo)}

    def _get_steem_per_mvest(self, dgpo):
        steem = Decimal(dgpo['total_vesting_fund_steem'].split(' ')[0])
        mvests = Decimal(dgpo['total_vesting_shares'].split(' ')[0]) / Decimal(1e6)
        return "0.000" # TODO: fix dumb column type
        return "%.6f" % (steem / mvests)

    def _get_feed_price(self):
        feed = self.__exec('get_current_median_history_price')
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
                assert result, "empty response {}".format(result)
            except (AssertionError, RPCError) as e:
                tries += 1
                print("{} failure, retry in {}s -- {}".format(method, tries, e))
                time.sleep(tries)
                continue
            break

        batch_size = len(params[0]) if method == 'get_accounts' else 1
        total_time = int((time.perf_counter() - time_start) * 1000)
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
                method, params, max_workers=10))

        total_time = int((time.perf_counter() - time_start) * 1000)
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
                print("batch {} failure, retry in {}s -- {}".format(method, tries, e))
                time.sleep(tries)
                continue
