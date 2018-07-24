#pylint: disable=missing-docstring
from hive.utils.unique_fifo import UniqueFIFO

def test_unique_queue():
    q = UniqueFIFO()
    assert q.extend(set(['tim', 'bob'])) == 2
    assert len(q) == 2

    assert q.extend(set(['tim', 'foo'])) == 1
    assert len(q) == 3

    pop1 = q.shift_portion(3)
    assert pop1 == ['tim'] or pop1 == ['bob']
    assert len(q) == 2

    assert q.extend(set()) == 0
    assert len(q) == 2

    assert q.extend(set(['foo'])) == 0
    assert len(q) == 2

    pop2 = q.shift_portion(1)
    assert pop2 == ['bob', 'foo'] or pop2 == ['tim', 'foo']
    assert len(q) == 0

    assert q.shift_portion(500) == []

    assert q.extend(set(['tim', 'bob'])) == 2
    assert q.extend(set(['tim', 'tom'])) == 1
    assert q.extend(set(['tim', 'foo'])) == 1

    assert set(q.shift_count(2)) == set(['tim', 'bob'])
    assert q.shift_count(1) == ['tom']
    assert q.shift_count(400) == ['foo']
    assert q.shift_count(400) == []
