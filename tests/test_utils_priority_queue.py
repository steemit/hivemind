#pylint: disable=missing-docstring
from hive.utils.priority_queue import PriorityQueue

def test_priority_queue():
    q = PriorityQueue()

    q.append('a', 0)
    q.append('b', 0)
    q.append('c', 1)
    q.append('d', 2)
    q.append('e', 0)
    q.append('f', 3)

    assert q.items() == ['f', 'd', 'c', 'a', 'b', 'e']
    assert q.items() == ['f', 'd', 'c', 'a', 'b', 'e'] #(branch)

    assert q.shift() == ('f', 3)
    assert q.shift(2) == [('d', 2), ('c', 1)]

    q.extend(['g', 'h', 'i'], 2)
    assert q.shift(0) == []
    assert q.shift(2) == [('g', 2), ('h', 2)]

    q.append('b', 4)
    q.append('e', 0)
    assert q.items() == ['b', 'i', 'a', 'e']


