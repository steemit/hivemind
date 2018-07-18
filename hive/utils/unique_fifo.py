"""Performant FIFO queue which ignores duplicates."""
from math import ceil

class UniqueFIFO:
    """FIFO queue which ignores duplicates and shifts efficiently."""

    def __init__(self):
        self._queue = []
        self._set = set()

    def extend(self, items):
        """Push multiple items onto the queue.

        Returns number of accepted items."""
        if not items:
            return 0

        assert isinstance(items, set)
        items = items - self._set

        if not items:
            return 0

        self._queue.extend(items)
        self._set |= set(items)
        return len(items)

    def shift_count(self, count=1):
        """Shift a number of items from the queue."""
        items = len(self._queue)
        if not items:
            return []
        if count >= items:
            return self._take_all()
        return self._shift(count)

    def shift_portion(self, total_portions):
        """Shift a fraction of items from the queue.

        Returned item count is `ceil(count / total_portions)`.
        """
        count = len(self._queue)
        if not count:
            return []
        if total_portions == 1 or count == 1:
            return self._take_all()

        count = ceil(count / total_portions)
        return self._shift(count)

    def _take_all(self):
        ret = self._queue
        self._queue = []
        self._set = set()
        return ret

    def _shift(self, count):
        # select relevant portion
        ret = self._queue[0:count]

        # prune queue and remove from set
        self._queue = self._queue[count:None]
        for item in ret:
            self._set.remove(item)

        return ret

    def __len__(self):
        return len(self._queue)
