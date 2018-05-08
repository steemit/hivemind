"""Timer for reporting progress on long batch operations."""

import time

class Timer:
    # Name of entity, lap units (e.g. rps, wps), total items in job
    _entity = []
    _lap_units = []
    _total = None

    _start_time = None
    _end_time = None

    # Lap checkpoints, # processed, last # processed
    _laps = []
    _processed = 0
    _last_items = 0

    def __init__(self, total=None, entity='', laps=None):
        self._entity = entity
        self._lap_units = laps or []
        self._total = total

    def batch_start(self):
        self._laps = []
        self.batch_lap()
        if not self._start_time:
            self._start_time = time.perf_counter()

    def batch_lap(self):
        self._laps.append(time.perf_counter())

    def batch_finish(self, ops=None):
        self.batch_lap()
        self._end_time = time.perf_counter()
        self._last_items = ops
        self._processed += ops

    def batch_status(self, prefix=None):

        if prefix:
            out = prefix
        else:
            # " -- post 1 of 10"
            out = " -- %s %d of %d" % (self._entity,
                                       self._processed,
                                       self._total)

        # " (3/s, 4rps, 5wps) -- "
        rates = []
        for i, unit in enumerate(['/s', *self._lap_units]):
            rates.append('%d%s' % (self._rate(i), unit))
        out += " (%s) -- "  % ', '.join(rates)

        if self._processed < self._total:
            # "eta 01:22"
            out += "eta %s" % self._eta()
        else:
            total_time = self._end_time - self._start_time
            out += "done in %s, avg rate: %.1f/s" % (
                self._time(total_time),
                self._total / total_time)

        return out

    def _rate(self, lap_idx=None):
        secs = self._elapsed(lap_idx)
        return self._last_items / secs

    def _eta(self):
        left = self._total - self._processed
        secs = (left / self._rate())
        return self._time(secs)

    def _time(self, secs):
        return "%02d:%02d" % (secs / 60, secs % 60)

    def _elapsed(self, lap_idx=None):
        if not lap_idx:
            return self._laps[-1] - self._laps[0]
        return self._laps[lap_idx] - self._laps[lap_idx-1]
