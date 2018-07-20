#pylint: disable=missing-docstring,expression-not-assigned
from hive.utils.profiler import Profiler

def test_profiler():
    p = Profiler('.tmp.test-prof')
    with p:
        [i for i in range(100000)]
    p.save()
    p.echo()

def test_profiler_passthru():
    p = Profiler(None)
    with p:
        [i for i in range(100000)]
    p.echo()
