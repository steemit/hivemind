#pylint: disable=missing-docstring
from hive.utils.system import (
    colorize,
    peak_usage_mb,
)

def test_colorize():
    plain = 'teststr'
    colored = '\x1b[93mteststr\x1b[0m'
    assert colorize(plain, color='93') in [plain, colored]
    assert colorize(plain, color='93', force=True) == colored

def test_peak_usage_mb():
    assert peak_usage_mb() > 1
