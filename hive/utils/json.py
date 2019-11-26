"""Methods to parse steemd values and clean strings."""

import re
import logging
import ujson as json

log = logging.getLogger(__name__)

# Value validation
# ----------------

def valid_command(val, valid=[]):
    """Validate given command among accepted set."""
    #pylint: disable=dangerous-default-value
    assert val in valid, 'invalid command: %s' % val
    return val

def valid_keys(obj, required=[], optional=[]):
    """Compare a set of input keys to expected and optional keys."""
    #pylint: disable=dangerous-default-value
    keys = obj.keys()
    missing = required - keys
    assert not missing, 'missing required keys: %s' % missing
    extra = keys - required - optional
    assert not extra, 'extraneous keys: %s' % extra
    return keys

VALID_DATE = re.compile(r'^\d\d\d\d\-\d\d-\d\dT\d\d:\d\d:\d\d$')
def valid_date(val):
    """Valid datetime (YYYY-MM-DDTHH:MM:SS)"""
    assert VALID_DATE.match(val), 'invalid date: %s' % val
    return val

VALID_LANG = ("ab,aa,af,ak,sq,am,ar,an,hy,as,av,ae,ay,az,bm,ba,eu,be,bn,bh,bi,"
              "bs,br,bg,my,ca,ch,ce,ny,zh,cv,kw,co,cr,hr,cs,da,dv,nl,dz,en,eo,"
              "et,ee,fo,fj,fi,fr,ff,gl,ka,de,el,gn,gu,ht,ha,he,hz,hi,ho,hu,ia,"
              "id,ie,ga,ig,ik,io,is,it,iu,ja,jv,kl,kn,kr,ks,kk,km,ki,rw,ky,kv,"
              "kg,ko,ku,kj,la,lb,lg,li,ln,lo,lt,lu,lv,gv,mk,mg,ms,ml,mt,mi,mr,"
              "mh,mn,na,nv,nd,ne,ng,nb,nn,no,ii,nr,oc,oj,cu,om,or,os,pa,pi,fa,"
              "pl,ps,pt,qu,rm,rn,ro,ru,sa,sc,sd,se,sm,sg,sr,gd,sn,si,sk,sl,so,"
              "st,es,su,sw,ss,sv,ta,te,tg,th,ti,bo,tk,tl,tn,to,tr,ts,tt,tw,ty,"
              "ug,uk,ur,uz,ve,vi,vo,wa,cy,wo,fy,xh,yi,yo,za").split(',')
def valid_lang(val):
    """Valid ISO-639-1 language (https://en.wikipedia.org/wiki/ISO_639-1)"""
    assert val in VALID_LANG, 'invalid ISO639-1 lang: %s' % val
    return val

# Custom op validation
# --------------------

def parse_op_json(op, block_num):
    """Parse a custom_json op, validating its structure."""
    # read custom json
    assert op['json'], 'input json was empty'
    op_json = {}
    try:
        op_json = json.loads(op['json'])
    except Exception as e:
        log.error("json.loads error: %s in %s", e, op['json'])
    assert op_json, 'parsed json was empty'

    # legacy compat
    if op['id'] == 'follow':
        if block_num < 6000000 and not isinstance(op_json, list):
            op_json = ['follow', op_json]

    return op_json

def valid_op_json(op_json):
    """Asserts object is in the form of `[command, {payload}]`."""
    assert isinstance(op_json, list), 'json must be a list'
    assert len(op_json) == 2, 'json must be a list with 2 elements'
    assert isinstance(op_json[0], str), 'json[0] must be a str (command)'
    assert isinstance(op_json[1], dict), 'json[1] must be dict (payload)'
    return op_json
