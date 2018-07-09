"""Block scheduler."""
import logging
from time import time, sleep
from pytz import utc
from hive.utils.normalize import block_date
from hive.utils.stats import Stats

log = logging.getLogger(__name__)

class StaleHeadException(Exception):
    """Raised when the head block appears to be too old."""
    pass

class BlockSchedule:
    """Maintains a self-adjusting schedule which anticipates new blocks."""

    BLOCK_INTERVAL = 3

    def __init__(self, current_head_block):
        self._start_block = current_head_block
        self._head_num = current_head_block
        self._next_expected = time() + self.BLOCK_INTERVAL / 2
        self._drift = self.BLOCK_INTERVAL / 2
        self._missed = 0
        self._last_date = None

    def wait_for_block(self, num):
        """Sleep until the requested block is expected to be available.

        Returns current head block (which is always gte `num`)"""
        head_time = time() - self._drift

        # if slots missed, advance head block
        while head_time >= self._next_expected:
            self._advance()
            if head_time < self._next_expected:
                log.warning("%d blocks behind",
                            self._head_num - num)

        # if head is behind, sleep until ready
        while self._head_num < num:
            wait_secs = self._next_expected - head_time
            sleep(wait_secs)
            Stats.log_idle(wait_secs * 1000)
            head_time = self._next_expected
            self._advance()

        return self._head_num

    def check_block(self, num, block):
        """Handle a successful or failed block fetch.

        If an expected block was not available, we add a backwards
        drift to the internal schedule. If it was successfully fetched,
        we need to inspect it for missed blocks and adjust our timing
        to account for them."""
        if block:
            self._drift_forward()
            date = block_date(block)
            self._check_missing(num, self._last_date, date)
            self._check_head_date(num, date)
            self._last_date = date
        else:
            self._drift_backward()
            log.warning("block %d not available. head:%s drift:%fs",
                        num, self._head_num, self._drift)

    def _check_head_date(self, num, date):
        """Sanity-checking of head block date.

        It's possible a steemd node could fall behind or stop syncing;
        we can identify this case by comparing current time to latest
        received block time."""
        if num == self._head_num:
            gap = time() - date.replace(tzinfo=utc).timestamp()
            assert gap > -60, 'system clock is %ds behind chain' % gap
            if gap > 60:
                raise StaleHeadException("chain gap is %fs" % gap)

    def _check_missing(self, num, prev_date, next_date):
        """Check missing blocks between previous and next block dates."""
        if num <= self._start_block or not prev_date:
            # if missing prior to start, irrelevant.
            return

        gap_secs = (next_date - prev_date).seconds
        assert gap_secs >= self.BLOCK_INTERVAL
        missed = (gap_secs / self.BLOCK_INTERVAL) - 1
        if missed:
            self._add_missed(missed)
            log.warning("%d missed @ block %d", missed, num)

    def _drift_backward(self, delta=0.1):
        """Delay the schedule by 0.1s when a block fetch failed."""
        self._drift = min(self.BLOCK_INTERVAL, self._drift + delta)

    def _drift_forward(self, delta=0.001):
        """Adjust schedule forward. Default is to slowly creep forward."""
        self._drift -= delta

    def _add_missed(self, missed):
        """Accounts for missed blocks."""
        self._missed += missed
        self._next_expected += missed * self.BLOCK_INTERVAL
        self._drift = 1

    def _advance(self):
        """Advances the schedule by 1 block."""
        self._head_num += 1
        self._next_expected += self.BLOCK_INTERVAL
