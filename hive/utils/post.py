"""Methods for normalizing steemd post metadata."""
#pylint: disable=line-too-long

import math
import ujson as json
from funcy.seqs import first

from hive.utils.normalize import sbd_amount, rep_log10, safe_img_url, parse_time, utc_timestamp


def post_basic(post):
    """Basic post normalization: json-md, tags, and flags."""
    md = {}
    try:
        md = json.loads(post['json_metadata'])
        if not isinstance(md, dict):
            md = {}
    except Exception:
        pass

    thumb_url = ''
    if md and 'image' in md:
        if md['image']:
            if not isinstance(md['image'], list):
                md['image'] = [md['image']]
            md['image'] = list(filter(None, map(safe_img_url, md['image'])))
        if md['image']:
            thumb_url = md['image'][0]
        else:
            del md['image']

    # clean up tags, check if nsfw
    tags = [post['category']]
    if md and 'tags' in md and isinstance(md['tags'], list):
        tags = tags + md['tags']
    tags = set(list(map(lambda tag: (str(tag) or '').strip('# ').lower()[:32], tags))[0:5])
    tags.discard('')
    is_nsfw = 'nsfw' in tags

    body = post['body']
    if body.find('\x00') > -1:
        #url = post['author'] + '/' + post['permlink']
        body = body.replace('\x00', '[NUL]')

    # payout date is last_payout if paid, and cashout_time if pending.
    is_paidout = (post['cashout_time'][0:4] == '1969')
    payout_at = post['last_payout'] if is_paidout else post['cashout_time']

    # payout is declined if max_payout = 0, or if 100% is burned
    is_payout_declined = False
    if sbd_amount(post['max_accepted_payout']) == 0:
        is_payout_declined = True
    elif len(post['beneficiaries']) == 1:
        benny = first(post['beneficiaries'])
        if benny['account'] == 'null' and int(benny['weight']) == 10000:
            is_payout_declined = True

    # payout entirely in SP
    is_full_power = int(post['percent_steem_dollars']) == 0

    return {
        'json_metadata': md,
        'image': thumb_url,
        'tags': tags,
        'is_nsfw': is_nsfw,
        'body': body,
        'preview': body[0:1024],

        'payout_at': payout_at,
        'is_paidout': is_paidout,
        'is_payout_declined': is_payout_declined,
        'is_full_power': is_full_power,
    }

def post_legacy(post):
    """Return legacy fields which may be useful to save.

    Some UI's may want to leverage these, but no point in indexing.
    """
    _legacy = ['id', 'url', 'root_comment', 'root_author', 'root_permlink',
               'root_title', 'parent_author', 'parent_permlink',
               'max_accepted_payout', 'percent_steem_dollars',
               'curator_payout_value', 'allow_replies', 'allow_votes',
               'allow_curation_rewards', 'beneficiaries']
    return {k: v for k, v in post.items() if k in _legacy}

def post_payout(post):
    """Get current vote/payout data and recalculate trend/hot score."""
    # total payout (completed and/or pending)
    payout = sum([
        sbd_amount(post['total_payout_value']),
        sbd_amount(post['curator_payout_value']),
        sbd_amount(post['pending_payout_value']),
    ])

    # `active_votes` was temporarily missing in dev -- ensure this condition
    # is caught ASAP. if no active_votes then rshares MUST be 0. ref: steem#2568
    assert post['active_votes'] or int(post['net_rshares']) == 0

    # get total rshares, and create comma-separated vote data blob
    rshares = sum(int(v['rshares']) for v in post['active_votes'])
    csvotes = "\n".join(map(_vote_csv_row, post['active_votes']))

    # trending scores
    _timestamp = utc_timestamp(parse_time(post['created']))
    sc_trend = _score(rshares, _timestamp, 480000)
    sc_hot = _score(rshares, _timestamp, 10000)

    return {
        'payout': payout,
        'rshares': rshares,
        'csvotes': csvotes,
        'sc_trend': sc_trend,
        'sc_hot': sc_hot
    }

def _vote_csv_row(vote):
    """Convert a vote object into minimal CSV line."""
    rep = rep_log10(vote['reputation'])
    return "%s,%s,%s,%s" % (vote['voter'], vote['rshares'], vote['percent'], rep)

def _score(rshares, created_timestamp, timescale=480000):
    """Calculate trending/hot score.

    Source: calculate_score - https://github.com/steemit/steem/blob/8cd5f688d75092298bcffaa48a543ed9b01447a6/libraries/plugins/tags/tags_plugin.cpp#L239
    """
    mod_score = rshares / 10000000.0
    order = math.log10(max((abs(mod_score), 1)))
    sign = 1 if mod_score > 0 else -1
    return sign * order + created_timestamp / timescale

def post_stats(post):
    """Get post statistics and derived properties.

    Source: contentStats - https://github.com/steemit/condenser/blob/master/src/app/utils/StateFunctions.js#L109
    """
    net_rshares_adj = 0
    neg_rshares = 0
    total_votes = 0
    up_votes = 0
    for vote in post['active_votes']:
        if vote['percent'] == 0:
            continue

        total_votes += 1
        rshares = int(vote['rshares'])
        sign = 1 if vote['percent'] > 0 else -1
        if sign > 0:
            up_votes += 1
        if sign < 0:
            neg_rshares += rshares

        # For graying: sum rshares, but ignore neg rep users and dust downvotes
        neg_rep = str(vote['reputation'])[0] == '-'
        if not (neg_rep and sign < 0 and len(str(rshares)) < 11):
            net_rshares_adj += rshares

    # take negative rshares, divide by 2, truncate 10 digits (plus neg sign),
    #   and count digits. creates a cheap log10, stake-based flag weight.
    #   result: 1 = approx $400 of downvoting stake; 2 = $4,000; etc
    flag_weight = max((len(str(neg_rshares / 2)) - 11, 0))

    author_rep = rep_log10(post['author_reputation'])
    is_low_value = net_rshares_adj < -9999999999
    has_pending_payout = sbd_amount(post['pending_payout_value']) >= 0.02

    return {
        'hide': not has_pending_payout and (author_rep < 0),
        'gray': not has_pending_payout and (author_rep < 1 or is_low_value),
        'author_rep': author_rep,
        'flag_weight': flag_weight,
        'total_votes': total_votes,
        'up_votes': up_votes
    }
