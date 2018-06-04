"""Tracks SQL timing stats and prints results periodically or on exit."""

import time
import re
import atexit
from hive.utils.system import colorize, peak_usage_mb

# pylint: disable=missing-docstring

class QueryStats:
    SLOW_QUERY_MS = 250

    stats = {}
    ttl_time = 0.0

    def __init__(self):
        atexit.register(QueryStats.print)

    def __call__(self, fn):
        def wrap(*args, **kwargs):
            time_start = time.perf_counter()
            result = fn(*args, **kwargs)
            time_end = time.perf_counter()
            QueryStats.log(args[1], (time_end - time_start) * 1000)
            return result
        return wrap

    @classmethod
    def log(cls, sql, ms):
        nsql = cls.normalize_sql(sql)
        cls.add_nsql_ms(nsql, ms)
        cls.check_timing(nsql, ms)
        if cls.ttl_time > 30 * 60 * 1000:
            cls.print()

    @classmethod
    def add_nsql_ms(cls, nsql, ms):
        if nsql not in cls.stats:
            cls.stats[nsql] = [ms, 1]
        else:
            cls.stats[nsql][0] += ms
            cls.stats[nsql][1] += 1
        cls.ttl_time += ms

    @classmethod
    def normalize_sql(cls, sql):
        nsql = re.sub(r'\s+', ' ', sql).strip()[0:256]
        nsql = re.sub(r'VALUES (\s*\([^)]+\),?)+', 'VALUES (...)', nsql)
        return nsql

    @classmethod
    def check_timing(cls, nsql, ms):
        if ms > cls.SLOW_QUERY_MS:
            print(colorize("[SQL-SLOW][%dms] %s" % (ms, nsql[:250])))

    @classmethod
    def print(cls):
        if not cls.stats:
            return
        ttl = cls.ttl_time
        print("[STATS] sampled SQL time: {}s".format(int(ttl / 1000)))
        for arr in sorted(cls.stats.items(), key=lambda x: -x[1][0])[0:40]:
            sql, vals = arr
            ms, calls = vals
            print("% 5.1f%% % 7dms % 9.2favg % 8dx -- %s"
                  % (100 * ms/ttl, ms, ms/calls, calls, sql[0:180]))
        print("[STATS] peak memory usage: %.2fMB" % peak_usage_mb())
        cls.clear()

    @classmethod
    def clear(cls):
        cls.stats = {}
        cls.ttl_time = 0
