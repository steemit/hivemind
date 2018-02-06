#pylint: disable=line-too-long

import math

from hive.utils.normalize import amount, rep_log10

# see: calculate_score - https://github.com/steemit/steem/blob/8cd5f688d75092298bcffaa48a543ed9b01447a6/libraries/plugins/tags/tags_plugin.cpp#L239
def score(rshares, created_timestamp, timescale=480000):
    mod_score = rshares / 10000000.0
    order = math.log10(max((abs(mod_score), 1)))
    sign = 1 if mod_score > 0 else -1
    return sign * order + created_timestamp / timescale

# see: contentStats - https://github.com/steemit/condenser/blob/master/src/app/utils/StateFunctions.js#L109
def post_stats(post):
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
    has_pending_payout = amount(post['pending_payout_value']) >= 0.02

    return {
        'hide': not has_pending_payout and (author_rep < 0),
        'gray': not has_pending_payout and (author_rep < 1 or is_low_value),
        'author_rep': author_rep,
        'flag_weight': flag_weight,
        'total_votes': total_votes,
        'up_votes': up_votes
    }
