import atexit
import resource

class ClientStats:
    # Assumed HTTP overhead (ms); subtract prior to par check
    PAR_HTTP_OVERHEAD = 75

    # Reporting threshold (x * par)
    PAR_THRESHOLD = 1.1

    # Thresholds for critical call timing (ms)
    PAR_STEEMD = {
        'get_dynamic_global_properties': 20,
        'get_block': 50,
        'get_blocks_batch': 5,
        'get_accounts': 3,
        'get_content': 4,
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
        if method == 'get_block' and batch_size > 1:
            method = 'get_blocks_batch'
        per = int((ms - cls.PAR_HTTP_OVERHEAD) / batch_size)
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
        print("[STATS] sampled steem time: {}s".format(int(ttl / 1000)))
        for arr in sorted(cls.stats.items(), key=lambda x: -x[1][0])[0:40]:
            sql, vals = arr
            ms, calls = vals
            print("% 5.1f%% % 9sms % 7.2favg % 8dx -- %s"
                  % (100 * ms/ttl, "{:,}".format(int(ms)),
                     ms/calls, calls, sql[0:180]))
        print("[STATS] fastest steem call was %.3fms" % cls.fastest)
        max_mem = int(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss)
        print("[STATS] peak memory usage: %.2fMB" % (max_mem / (1024 * 1024)))
        cls.clear()

    @classmethod
    def clear(cls):
        cls.stats = {}
        cls.ttltime = 0

atexit.register(ClientStats.print)
