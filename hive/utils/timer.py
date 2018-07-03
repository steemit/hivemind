"""Timer for reporting progress on long batch operations."""

import time
from hive.utils.normalize import secs_to_str

class Timer:
    """Times long routines, printing status and ETA.

    Routines are split into batches; each consisting of 1+ laps.

    `total` - total number of items being processed
    `entity` - name of entity being processed
    `laps` - list of labels, for ops/s output per lap
    `full_total` - total items to process, outside of
                   (and including) this invocation. [optional]
    """

    # Name of entity, lap units (e.g. rps, wps), total items in job
    _entity = []
    _lap_units = []
    _total = None
    _full_total = None

    _start_time = None
    _end_time = None

    # Lap checkpoints, # processed, last # processed
    _laps = []
    _processed = 0
    _last_items = 0

    def __init__(self, total=None, entity='', laps=None, full_total=None):
        self._entity = entity
        self._lap_units = laps or []
        self._total = total
        self._full_total = full_total or total

    def batch_start(self):
        """Signal new batch; call at top of loop."""
        self._laps = []
        self.batch_lap()
        if not self._start_time:
            self._start_time = time.perf_counter()

    def batch_lap(self):
        """Signal movement to next task within batch."""
        self._laps.append(time.perf_counter())

    def batch_finish(self, ops=None):
        """Signal end of batch."""
        self.batch_lap()
        self._end_time = time.perf_counter()
        self._last_items = ops
        self._processed += ops

    def batch_status(self, prefix=None):
        """Generate status line."""
        if prefix:
            out = prefix
        else:
            # " -- post 1 of 10"
            out = " -- %s %d of %d" % (self._entity,
                                       self._processed,
                                       self._full_total)

        # " (3/s, 4rps, 5wps) -- "
        rates = []
        for i, unit in enumerate(['/s', *self._lap_units]):
            rates.append('%d%s' % (self._rate(i), unit))
        out += " (%s) -- "  % ', '.join(rates)

        if self._processed < self._total:
            out += "eta %s" % self._eta()
        else:
            total_time = self._end_time - self._start_time
            out += "done in %s, avg rate: %.1f/s" % (
                secs_to_str(total_time),
                self._total / total_time)

        return out

    def _rate(self, lap_idx=None):
        """Get the rate of last batch's lap_idx, pass None for overall."""
        secs = self._elapsed(lap_idx)
        return self._last_items / secs

    def _eta(self):
        """Time to finish, based on most recent batch."""
        left = self._full_total - self._processed
        secs = (left / self._rate())
        return secs_to_str(secs)

    def _elapsed(self, lap_idx=None):
        if not lap_idx:
            return self._laps[-1] - self._laps[0]
        return self._laps[lap_idx] - self._laps[lap_idx-1]
