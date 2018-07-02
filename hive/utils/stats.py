"""Tracks SQL timing stats and prints results periodically or on exit."""

import re
import atexit
import logging
from time import perf_counter as perf
from hive.utils.system import colorize, peak_usage_mb
# pylint: disable=missing-docstring

log = logging.getLogger(__name__)

def _normalize_sql(sql):
    nsql = ' '.join(sql[0:512].split())[0:256]
    nsql = re.sub(r'VALUES (\s*\([^)]+\),?)+', 'VALUES (...)', nsql)
    return nsql

def log_query_stats(fn):
    def wrap(*args, **kwargs):
        time_start = perf()
        result = fn(*args, **kwargs)
        time_end = perf()
        Stats.log_db(args[1], (time_end - time_start) * 1000)
        return result
    return wrap

class StatsAbstract:
    def __init__(self, service):
        self._calls = {}
        self._ms = 0.0
        self._service = service

    def add(self, call, ms, batch_size=1):
        if call not in self._calls:
            self._calls[call] = [0, 0]
        self._calls[call][0] += ms
        self._calls[call][1] += batch_size
        self.check_timing(call, ms, batch_size)
        self._ms += ms

    def check_timing(self, call, ms, batch_size):
        pass

    def ms(self):
        return self._ms

    def clear(self):
        self._calls = {}
        self._ms = 0.0

    def table(self, count=40):
        top = sorted(self._calls.items(), key=lambda x: -x[1][0])
        return [(call, *vals) for (call, vals) in top[:count]]

    def report(self, total_ms):
        if not self._calls:
            return

        log.warning("%ds in %s (%.1f%%)",
                    round(self._ms / 1000),
                    self._service,
                    100 * (self._ms / total_ms))

        for call, ms, reqs in self.table(40):
            log.warning("% 5.1f%% % 7dms % 9.2favg % 8dx -- %s",
                        100 * ms/self._ms, ms, ms/reqs, reqs, call[0:150])
        self.clear()


class SteemStats(StatsAbstract):
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

    def __init__(self):
        super().__init__('steem')

    def check_timing(self, call, ms, batch_size):
        if call == 'get_block' and batch_size > 1:
            call = 'get_blocks_batch'
        per = int((ms - self.PAR_HTTP_OVERHEAD) / batch_size)
        par = self.PAR_STEEMD[call]
        over = per / par
        if over >= self.PAR_THRESHOLD:
            out = ("[STEEM][%dms] %s[%d] -- %.1fx par (%d/%d)"
                   % (ms, call, batch_size, over, per, par))
            log.warning(colorize(out))


class DbStats(StatsAbstract):
    SLOW_QUERY_MS = 250

    def __init__(self):
        super().__init__('db')

    def add(self, call, ms, batch_size=1):
        super().add(_normalize_sql(call), ms, batch_size)

    def check_timing(self, call, ms, batch_size):
        if ms > self.SLOW_QUERY_MS:
            log.warning(colorize("[SQL][%dms] %s" % (ms, call[:250])))


class Stats:
    """Collects steemd/db timing data."""
    PRINT_THRESH_MINS = 0.5

    _db = DbStats()
    _steemd = SteemStats()
    _ms = 0.0

    @classmethod
    def log_db(cls, sql, ms):
        cls._db.add(sql, ms)
        cls.add_ms(ms)

    @classmethod
    def log_steem(cls, method, ms, batch_size=1):
        cls._steemd.add(method, ms, batch_size)
        cls.add_ms(ms)

    @classmethod
    def add_ms(cls, ms):
        cls._ms += ms
        if cls._ms > cls.PRINT_THRESH_MINS * 60 * 1000:
            cls.report()
            cls._ms = 0

    @classmethod
    def report(cls):
        log.warning("[STATS] cumtime %ds. peak mem %.2fmb.",
                    cls._ms / 1000, peak_usage_mb())
        cls._db.report(cls._ms)
        cls._steemd.report(cls._ms)

atexit.register(Stats.report)
