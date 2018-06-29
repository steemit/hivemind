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
    load_json_key,
    trunc,
    rep_log10,
    safe_img_url,
)

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
