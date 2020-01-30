#pylint: disable=missing-docstring, invalid-name
from hive.utils.unique_fifo import UniqueFIFO

def test_unique_queue():
    q = UniqueFIFO()
    assert q.extend(set(['tim', 'bob'])) == 2
    assert len(q) == 2

    assert q.extend(set(['tim', 'foo'])) == 1
    assert len(q) == 3

    pop1 = q.shift_portion(3)
    assert pop1 == ['tim']
    assert len(q) == 2

    assert q.extend(set()) == 0
    assert len(q) == 2

    assert q.extend(set(['foo'])) == 0
    assert len(q) == 2

    pop2 = q.shift_portion(1)
    assert pop2 == ['bob', 'foo']
    assert not q

    assert q.shift_portion(500) == []

    assert q.extend(set(['tim', 'bob'])) == 2
    assert q.extend(set(['tim', 'tom'])) == 1
    assert q.extend(set(['tim', 'foo'])) == 1

    assert set(q.shift_count(2)) == set(['tim', 'bob'])
    assert q.shift_count(1) == ['tom']
    assert q.shift_count(400) == ['foo']
    assert q.shift_count(400) == []

    q.extend(set(['foo', 'bar']))
    q.add('foo')
    q.add('cat')
    assert len(q) == 3
    pop3 = q.shift_portion(1)
    assert pop3 == ['foo', 'bar', 'cat']
