#pylint: disable=missing-docstring
import pytest

from datetime import datetime
from decimal import Decimal

from hive.utils.normalize import (
    block_num,
    block_date,
    vests_amount,
    steem_amount,
    sbd_amount,
    parse_amount,
    amount,
    legacy_amount,
    parse_time,
    utc_timestamp,
    load_json_key,
    trunc,
    rep_log10,
    safe_img_url,
    secs_to_str,
    strtobool,
    int_log_level,
)

def test_secs_to_str():
    assert secs_to_str(0) == '00s'
    assert secs_to_str(8979) == '02h 29m 39s'
    assert secs_to_str(12345678) == '20w 02d 21h 21m 18s'

def test_block_num():
    block = dict(block_id='013c33f88c643c92a7352b52efde7237f4d4ee0b')
    assert block_num(block) == 20722680

def test_block_date():
    block = dict(timestamp='2018-03-16T10:08:42')
    assert block_date(block) == datetime(2018, 3, 16, 10, 8, 42)

def test_vests_amount():
    assert vests_amount('4.549292 VESTS') == Decimal('4.549292')

def test_steem_amount():
    assert steem_amount('1.234567 STEEM') == Decimal('1.234567')

def test_sbd_amount():
    assert sbd_amount('1.001 SBD') == Decimal('1.001')

def test_parse_amount():
    nai = [1231121, 6, '@@000000037']
    assert parse_amount(nai, 'VESTS') == Decimal('1.231121')

def test_amount():
    assert amount('3.432 FOO') == Decimal('3.432')

def test_legacy_amount():
    nai = [1231121, 6, '@@000000037']
    assert legacy_amount(nai) == '1.231121 VESTS'

def test_parse_time():
    block_time = '2018-06-22T20:34:30'
    assert parse_time(block_time) == datetime(2018, 6, 22, 20, 34, 30)

def test_utc_timestamp():
    assert utc_timestamp(parse_time('1970-01-01T00:00:00')) == 0
    assert utc_timestamp(parse_time('1970-01-01T00:00:01')) == 1

    block_time = '2018-06-22T20:34:30'
    date = parse_time(block_time)
    timestamp = utc_timestamp(date)
    assert timestamp == 1529699670

def test_load_json_key():
    obj = {'profile':'{"foo":"bar"}'}
    loaded = load_json_key(obj, 'profile')
    assert loaded
    print(loaded, "===============SSSSSSSSSSS")
    assert loaded['foo'] == 'bar'

def test_trunc():
    assert trunc('string too long', 5) == 'st...'

def test_rep_log10():
    assert rep_log10(0) == 25
    assert rep_log10('2321387987213') == 55.29

def test_safe_img_url():
    url = 'https://example.com/a.jpg'
    max_size = len(url) + 1
    assert safe_img_url(url, max_size) == url
    assert safe_img_url(url + 'x', max_size) is None

def test_strtobool():
    assert strtobool('t') == True
    assert strtobool('T') == True
    assert strtobool('1') == True
    assert strtobool('true') == True
    assert strtobool('yes') == True

    assert strtobool('f') == False
    assert strtobool('F') == False
    assert strtobool('0') == False
    assert strtobool('false') == False
    assert strtobool('n') == False
    assert strtobool('no') == False

    with pytest.raises(ValueError):
        strtobool('foo')

def test_int_log_level():
    assert int_log_level('debug') == 10
    assert int_log_level('DEBUG') == 10
    assert int_log_level('info') == 20
    assert int_log_level('warning') == 30
    with pytest.raises(ValueError):
        int_log_level('foo')
    with pytest.raises(ValueError):
        int_log_level(None)
    with pytest.raises(ValueError):
        int_log_level('')
