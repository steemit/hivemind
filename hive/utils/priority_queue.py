"""Priority queue used by CachedPost for tracking dirty post URLs."""

#from collections import OrderedDict as odict

class PriorityQueue:
    """Priority/FIFO-based queue.

    Items with highest priority are shifted first.
    """

    def __init__(self):
        self._queue = []
        self._prt = {}
        self._sorted = True
        self._i = 0

    def append(self, item, priority: int):
        """Push an item at priority onto the queue."""
        if item not in self._prt:
            self._i += 1
            self._queue.append(item)
            self._prt[item] = [priority, self._i]
            self._sorted = False
        elif self._prt[item][0] < priority:
            self.remove(item)
            self.append(item, priority)
            #self._prt[item][0] = priority
            #self._sorted = False

    #def extend(self, items, priority: int):
    #    """Push multiple items at priority onto queue."""
    #    for item in items:
    #        self.append(item, priority)

    def shift_count(self, count=1):
        """Shifts, sorted by priority(desc) then age."""
        if not self._sorted:
            self._sort()
        if count == 1:
            return self._shift()
        return self._multi_shift(count)

    def shift_portion(self, total_portions=1):
        assert total_portions == 1
        ret = self.items()
        self.clear()
        return ret

    def items(self):
        """Returns all items (without priority)"""
        if not self._sorted:
            self._sort()
        items = self._queue
        priorities = [self._prt[k][0] for k in items]
        return (items, priorities)

    def clear(self):
        self._queue = []
        self._prt = {}
        self._sorted = True

    def __len__(self):
        return len(self._queue)

    def remove(self, item):
        """Removes the item, returns success bool."""
        if item not in self._prt:
            return False

        self._queue.remove(item)
        del self._prt[item]
        return True

    def _multi_shift(self, count):
        items = self._queue[:count]
        self._queue = self._queue[count:]
        priorities = [self._prt.pop(k)[0] for k in items]
        return (items, priorities)

    def _shift(self):
        item = self._queue.pop(0)
        priority = self._prt.pop(item)[0]
        return ([item], [priority])

    def _sort(self):
        self._queue = sorted(
            self._queue,
            key=lambda x: (-self._prt[x][0], self._prt[x][1]))
        self._sorted = True
