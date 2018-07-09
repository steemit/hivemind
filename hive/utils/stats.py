"""Tracks SQL timing stats and prints results periodically or on exit."""

import re
import atexit
import logging

from time import perf_counter as perf
from hive.utils.system import colorize, peak_usage_mb

log = logging.getLogger(__name__)

def _normalize_sql(sql):
    nsql = ' '.join(sql[0:512].split())[0:256]
    nsql = re.sub(r'VALUES (\s*\([^)]+\),?)+', 'VALUES (...)', nsql)
    return nsql

def log_query_stats(fn):
    """Decorator for hive.db.adapter::query()"""
    def _wrap(*args, **kwargs):
        time_start = perf()
        result = fn(*args, **kwargs)
        time_end = perf()
        Stats.log_db(args[1], (time_end - time_start) * 1000)
        return result
    return _wrap

class StatsAbstract:
    """Tracks service call timings"""
    def __init__(self, service):
        self._calls = {}
        self._ms = 0.0
        self._service = service

    def add(self, call, ms, batch_size=1):
        """Record a call's duration."""
        if call not in self._calls:
            self._calls[call] = [0, 0]
        self._calls[call][0] += ms
        self._calls[call][1] += batch_size
        self.check_timing(call, ms, batch_size)
        self._ms += ms

    def check_timing(self, call, ms, batch_size):
        """Override for service-specific QA"""
        pass

    def ms(self):
        """Get total time spent in service"""
        return self._ms

    def clear(self):
        """Clear accumulators"""
        self._calls = {}
        self._ms = 0.0

    def table(self, count=40):
        """Generate a desc list of (call, total_ms, call_count) tuples."""
        top = sorted(self._calls.items(), key=lambda x: -x[1][0])
        return [(call, *vals) for (call, vals) in top[:count]]

    def report(self, total_ms):
        """Emit a table showing top calls by time spent."""
        if not self._calls:
            return

        log.info("Service: %s -- %ds total (%.1f%%)",
                 self._service,
                 round(self._ms / 1000),
                 100 * (self._ms / total_ms))

        log.info('%7s %9s %9s %9s', '-pct-', '-ttl-', '-avg-', '-cnt-')
        for call, ms, reqs in self.table(40):
            log.info("% 6.1f%% % 7dms % 9.2f % 8dx -- %s",
                     100 * ms/self._ms, ms, ms/reqs, reqs, call[0:150])
        self.clear()


class SteemStats(StatsAbstract):
    """Tracks Steem client call timings."""

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
        """Warn if a request (accounting for batch size) is too slow."""
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
    """Tracks database query timings."""
    SLOW_QUERY_MS = 250

    def __init__(self):
        super().__init__('db')

    def add(self, call, ms, batch_size=1):
        """Record SQL query timing. Incoming SQL is normalized."""
        super().add(_normalize_sql(call), ms, batch_size)

    def check_timing(self, call, ms, batch_size):
        """Warn if any query is slower than defined threshold."""
        if ms > self.SLOW_QUERY_MS:
            out = "[SQL][%dms] %s" % (ms, call[:250])
            log.warning(colorize(out))


class Stats:
    """Container for steemd and db timing data."""
    PRINT_THRESH_MINS = 5

    _db = DbStats()
    _steemd = SteemStats()
    _ms = 0.0
    _idle = 0.0
    _start = perf()

    @classmethod
    def log_db(cls, sql, ms):
        """Log a database query."""
        cls._db.add(sql, ms)
        cls.add_ms(ms)

    @classmethod
    def log_steem(cls, method, ms, batch_size=1):
        """Log a steemd call."""
        cls._steemd.add(method, ms, batch_size)
        cls.add_ms(ms)

    @classmethod
    def log_idle(cls, ms):
        """Track idle time (e.g. sleeping until next block)"""
        cls._idle += ms

    @classmethod
    def add_ms(cls, ms):
        """Add to total ms elapsed; print if threshold reached."""
        cls._ms += ms
        if cls._ms > cls.PRINT_THRESH_MINS * 60 * 1000:
            cls.report()
            cls._ms = 0
            cls._start = perf()

    @classmethod
    def report(cls):
        """Emit a timing report for tracked services."""
        local = cls._ms / 1000
        idle = cls._idle / 1000
        total = (perf() - cls._start)
        non_idle = total - idle
        log.info("cumtime %ds (%.1f%% of %ds). %.1f%% idle. peak %dmb.",
                 local, 100 * local / non_idle, non_idle,
                 100 * idle / total, peak_usage_mb())
        cls._db.report(cls._ms)
        cls._steemd.report(cls._ms)

atexit.register(Stats.report)
