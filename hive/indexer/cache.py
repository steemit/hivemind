import json
import logging
import math

from funcy.seqs import first
from hive.db.methods import query
from steem.amount import Amount
from steem.steemd import Steemd
from steem.utils import parse_time

log = logging.getLogger(__name__)


def get_img_url(url, max_size=1024):
    url = url.strip()
    if url and len(url) < max_size and url[0:4] is not 'http':
        return url


def score(rshares, created_timestamp, timescale=480000):
    mod_score = rshares / 10000000.0
    order = math.log10(max((abs(mod_score), 1)))
    sign = 1 if mod_score > 0 else -1
    return sign * order + created_timestamp / timescale


# not yet in use. need to get these fields into cache table.
def get_stats(post):
    net_rshares_adj = 0
    neg_rshares = 0
    total_votes = 0
    up_votes = 0
    for v in post['active_votes']:
        if v['percent'] == 0:
            continue

        total_votes += 1
        rshares = int(v['rshares'])
        sign = 1 if v['percent'] > 0 else -1
        if sign > 0:
            up_votes += 1
        if sign < 0:
            neg_rshares += rshares

            # For graying: sum up total rshares, but ignore neg rep users and tiny downvotes
        if str(v['reputation'])[0] != '-' and not (sign < 0 and len(str(rshares)) < 11):
            net_rshares_adj += rshares

    # take negative rshares, divide by 2, truncate 10 digits (plus neg sign), count digits.
    # creates a cheap log10, stake-based flag weight. 1 = approx $400 of downvoting stake; 2 = $4,000; etc
    flag_weight = max((len(str(neg_rshares / 2)) - 11, 0))

    allow_delete = post['children'] == 0 and int(post['net_rshares']) <= 0
    has_pending_payout = Amount(post['pending_payout_value']).amount >= 0.02
    author_rep = rep_log10(post['author_reputation'])

    gray_threshold = -9999999999
    low_value_post = net_rshares_adj < gray_threshold and author_rep < 65

    gray = not has_pending_payout and (authorRepLog10 < 1 or low_value_post)
    hide = not has_pending_payout and (authorRepLog10 < 0)

    return {
        'hide': hide,
        'gray': gray,
        'allow_delete': allow_delete,
        'author_rep': author_rep,
        'flag_weight': flag_weight,
        'total_votes': total_votes,
        'up_votes': up_votes
    }


def batch_queries(queries):
    query("START TRANSACTION")
    for sql in queries:
        query(sql)
    query("COMMIT")


# TODO: escape strings for mysql
def escape(str):
    return str


# TODO: calculate rep score
def rep_log10(raw):
    return 25


def vote_csv_row(vote):
    return ','.join((vote['voter'], str(vote['rshares']), str(vote['percent']), str(rep_log10(vote['reputation']))))


def generate_cached_post_sql(id, post, updated_at):
    md = json.loads(post['json_metadata']) or {}

    thumb_url = ''
    if md and md['image']:
        thumb_url = get_img_url(first(md['image'])) or ''

    # clean up tags, check if nsfw
    tags = (post['category'],)
    if md and md['tags'] and type(md['tags']) == list:
        tags += md['tags']
    tags = set(map(lambda str: str.lower(), tags))
    is_nsfw = int('nsfw' in tags)

    # payout date is last_payout if paid, and cashout_time if pending.
    payout_at = post['last_payout'] if post['cashout_time'][0:4] == '1969' else post['cashout_time']

    # get total rshares, and create comma-separated vote data blob
    rshares = sum(int(v['rshares']) for v in post['active_votes'])
    csvotes = "\n".join(map(vote_csv_row, post['active_votes']))

    # these are rshares which are PENDING
    payout_declined = False
    if Amount(post['max_accepted_payout']).amount == 0:
        payout_declined = True
    elif len(post['beneficiaries']) == 1:
        benny = first(post['beneficiaries'])
        if benny['account'] == 'null' and int(benny['weight']) == 10000:
            payout_declined = True

    # total payout (completed and/or pending)
    payout = sum([
        Amount(post['total_payout_value']).amount,
        Amount(post['curator_payout_value']).amount,
        Amount(post['pending_payout_value']).amount,
    ])

    # total promotion cost
    promoted = Amount(post['promoted']).amount

    # trending scores
    timestamp = parse_time(post['created']).timestamp()
    hot_score = score(rshares, timestamp, 10000)
    trend_score = score(rshares, timestamp, 480000)

    fields = [
        ['post_id', '%d' % id],
        ['title', "'%s'" % escape(post['title'])],
        ['preview', "'%s'" % escape(post['body'][0:1024])],
        ['img_url', "'%s'" % escape(thumb_url)],
        ['payout', "%f" % payout],
        ['promoted', "%f" % promoted],
        ['payout_at', "'%s'" % payout_at],
        ['updated_at', "'%s'" % updated_at],
        ['children', "%d" % post['children']],
        ['rshares', "%d" % rshares],
        ['votes', "'%s'" % escape(csvotes)],
        ['json', "'%s'" % escape(json.dumps(md))],
        ['is_nsfw', "%d" % is_nsfw],
        ['sc_trend', "%f" % trend_score],
        ['sc_hot', "%f" % hot_score]
    ]

    cols   = ', '.join( [v[0] for v in fields] )
    params = ', '.join( [v[1] for v in fields] )
    update = ', '.join( [v[0]+" = "+v[1] for v in fields] )
    sql = "INSERT INTO hive_posts_cache (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s"
    return sql % (cols, params, update)


# testing
# -------
def run():
    post = Steemd().get_content('roadscape', 'script-check')
    print(post)
    print(generate_cached_post_sql(1, post, '1970-01-01T00:00:00'))


if __name__ == '__main__':
    # setup()
    run()
