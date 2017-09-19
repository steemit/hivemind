import json
import logging
import math
import collections
import time
import re

from funcy.seqs import first
from hive.db.methods import query
from hive.indexer.utils import amount, parse_time, get_adapter

log = logging.getLogger(__name__)

def get_accounts_follow_stats(accounts):
    sql = "SELECT follower,COUNT(*) FROM hive_follows WHERE follower IN :a GROUP BY follower"
    following = dict(query(sql, a=accounts).fetchall())
    for a in accounts:
        if a not in following:
            following[a] = 0

    sql = "SELECT following,COUNT(*) FROM hive_follows WHERE following IN :a GROUP BY following"
    followers = dict(query(sql, a=accounts).fetchall())
    for a in accounts:
        if a not in followers:
            followers[a] = 0

    return {'followers': followers, 'following': following}

def truncate(string, maxlen):
    if string:
        string = string.strip()
        if len(string) > maxlen:
            string = string[0:(maxlen-1)] + '...'
    return string

def normalize_account_metadata(account):
    prof = {}
    try:
        prof = json.loads(account['json_metadata'])['profile']
        if type(prof) != dict:
            prof = {}
    except:
        pass

    name = str(prof['name']) if 'name' in prof else None
    about = str(prof['about']) if 'about' in prof else None
    location = str(prof['location']) if 'location' in prof else None
    website = str(prof['website']) if 'website' in prof else None
    profile_image = str(prof['profile_image']) if 'profile_image' in prof else None
    cover_image = str(prof['cover_image']) if 'cover_image' in prof else None

    name = truncate(name, 20)
    about = truncate(about, 160)
    location = truncate(location, 30)

    if name and name[0:1] == '@':
        name = None
    if website and len(website) > 100:
        website = None
    if website and website[0:4] != 'http':
        website = 'http://' + website
    # TODO: regex validate `website`

    if profile_image and not re.match('^https?://', profile_image):
        profile_image = None
    if cover_image and not re.match('^https?://', cover_image):
        cover_image = None

    return dict(
        display_name=name or '',
        about=about or '',
        location=location or '',
        website=website or '',
        profile_image=profile_image or '',
        cover_image=cover_image or '',
    )


def generate_cached_accounts_sql(accounts):
    fstats = get_accounts_follow_stats(accounts)
    sqls = []
    for account in get_adapter().get_accounts(accounts):
        name = account['name']

        values = {
            'name': name,
            'proxy': account['proxy'],
            'post_count': account['post_count'],
            'reputation': rep_log10(account['reputation']),
            'followers': fstats['followers'][name],
            'following': fstats['following'][name],
            'proxy_weight': amount(account['vesting_shares']),
            'vote_weight': amount(account['vesting_shares']),
            'kb_used': int(account['lifetime_bandwidth']) / 1e6 / 1024,
            **normalize_account_metadata(account)
        }

        update = ', '.join( [k+" = :"+k for k in values.keys()][1:] )
        sql = "UPDATE hive_accounts SET %s WHERE name = :name" % (update)
        sqls.append([(sql, values)])
    return sqls


def get_img_url(url, max_size=1024):
    if url and type(url) is not str:
        print("bad url param type: {}".format(url))
        url = None
    if url:
        url = url.strip()
    if url and len(url) < max_size and url[0:4] == 'http':
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

        # For graying: sum rshares, but ignore neg rep users and dust downvotes
        neg_rep = str(v['reputation'])[0] == '-'
        if not (neg_rep and sign < 0 and len(str(rshares)) < 11):
            net_rshares_adj += rshares

    # take negative rshares, divide by 2, truncate 10 digits (plus neg sign),
    #   and count digits. creates a cheap log10, stake-based flag weight.
    #   result: 1 = approx $400 of downvoting stake; 2 = $4,000; etc
    flag_weight = max((len(str(neg_rshares / 2)) - 11, 0))

    allow_delete = post['children'] == 0 and int(post['net_rshares']) <= 0
    has_pending_payout = amount(post['pending_payout_value']) >= 0.02
    author_rep = rep_log10(post['author_reputation'])

    gray_threshold = -9999999999
    low_value_post = net_rshares_adj < gray_threshold and author_rep < 65

    gray = not has_pending_payout and (author_rep < 1 or low_value_post)
    hide = not has_pending_payout and (author_rep < 0)

    # TODO: evaluate adding these columns
    # is_no_payout
    # is_full_power

    return {
        'hide': hide,
        'gray': gray,
        'allow_delete': allow_delete,
        'author_rep': author_rep,
        'flag_weight': flag_weight,
        'total_votes': total_votes,
        'up_votes': up_votes
    }


def batch_queries(batches):
    query("START TRANSACTION")
    for queries in batches:
        for (sql, params) in queries:
            query(sql, **params)
    query("COMMIT")


# calculate UI rep score
def rep_log10(rep):
    def log10(str):
        leading_digits = int(str[0:4])
        log = math.log10(leading_digits) + 0.00000001
        n = len(str) - 1
        return n + (log - int(log))

    rep = str(rep)
    if rep == "0":
        return 25

    sign = -1 if rep[0] == '-' else 1
    if sign < 0:
        rep = rep[1:]

    out = log10(rep)
    out = max(out - 9, 0) * sign  # @ -9, $1 earned is approx magnitude 1
    out = (out * 9) + 25          # 9 points per magnitude. center at 25
    return round(out, 2)


def vote_csv_row(vote):
    return ','.join((vote['voter'], str(vote['rshares']), str(vote['percent']), str(rep_log10(vote['reputation']))))


def generate_cached_post_sql(id, post, updated_at):
    if not post['author']:
        raise Exception("ERROR: post id {} has no chain state.".format(id))

    md = None
    try:
        md = json.loads(post['json_metadata'])
        if type(md) is not dict:
            md = {}
    except json.decoder.JSONDecodeError:
        pass

    thumb_url = ''
    if md and 'image' in md:
        thumb_url = get_img_url(first(md['image'])) or ''
        md['image'] = [thumb_url]

    # clean up tags, check if nsfw
    tags = [post['category']]
    if md and 'tags' in md and type(md['tags']) == list:
        tags = tags + md['tags']
    tags = set(map(lambda str: (str or '').strip('# ').lower()[:32], tags))
    tags.discard('')
    is_nsfw = int('nsfw' in tags)

    # payout date is last_payout if paid, and cashout_time if pending.
    is_paidout = (post['cashout_time'][0:4] == '1969')
    payout_at = post['last_payout'] if is_paidout else post['cashout_time']

    # get total rshares, and create comma-separated vote data blob
    rshares = sum(int(v['rshares']) for v in post['active_votes'])
    csvotes = "\n".join(map(vote_csv_row, post['active_votes']))

    # these are rshares which are PENDING
    payout_declined = False
    if amount(post['max_accepted_payout']) == 0:
        payout_declined = True
    elif len(post['beneficiaries']) == 1:
        benny = first(post['beneficiaries'])
        if benny['account'] == 'null' and int(benny['weight']) == 10000:
            payout_declined = True

    # total payout (completed and/or pending)
    payout = sum([
        amount(post['total_payout_value']),
        amount(post['curator_payout_value']),
        amount(post['pending_payout_value']),
    ])

    # total promotion cost
    promoted = amount(post['promoted'])

    # trending scores
    timestamp = parse_time(post['created']).timestamp()
    hot_score = score(rshares, timestamp, 10000)
    trend_score = score(rshares, timestamp, 480000)

    # TODO: add get_stats fields

    values = collections.OrderedDict([
        ('post_id', '%d' % id),
        ('author', "%s" % post['author']),
        ('permlink', "%s" % post['permlink']),
        ('title', "%s" % post['title']),
        ('preview', "%s" % post['body'][0:1024]),
        ('body', "%s" % post['body']),
        ('img_url', "%s" % thumb_url),
        ('payout', "%f" % payout),
        ('promoted', "%f" % promoted),
        ('payout_at', "%s" % payout_at),
        ('updated_at', "%s" % updated_at),
        ('created_at', "%s" % post['created']),
        ('rshares', "%d" % rshares),
        ('votes', "%s" % csvotes),
        ('json', "%s" % json.dumps(md)),
        ('is_nsfw', "%d" % is_nsfw),
        ('is_paidout', "%d" % is_paidout),
        ('sc_trend', "%f" % trend_score),
        ('sc_hot', "%f" % hot_score)
    ])
    fields = values.keys()

    # Multiple SQL statements are generated for each post
    sqls = []

    # Update main metadata in the hive_posts_cache table
    cols   = ', '.join( fields )
    params = ', '.join( [':'+k for k in fields] )
    update = ', '.join( [k+" = :"+k for k in fields][1:] )
    sql = "INSERT INTO hive_posts_cache (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s"
    sqls.append((sql % (cols, params, update), values))

    # update tag metadata only for top-level posts
    if post['depth'] == 0:
        sql = "DELETE FROM hive_post_tags WHERE post_id = :id"
        sqls.append((sql, {'id': id}))
        for tag in tags:
            sql = "INSERT INTO hive_post_tags (post_id, tag) VALUES (:id, :tag)"
            sqls.append((sql, {'id': id, 'tag': tag}))

    return sqls


def update_posts_batch(tuples, steemd, updated_at = None):
    # if calling function already has head_time, saves us a call
    if not updated_at:
        updated_at = steemd.head_time()

    total = len(tuples)
    processed = 0
    start_time = time.time()
    for i in range(0, total, 1000):

        buffer = []
        for id, post in steemd.get_content_batch(tuples[i:i+1000]).items():
            if not post['author']:
                # post was deleted; skip.
                continue
            sql = generate_cached_post_sql(id, post, updated_at)
            buffer.append(sql)

        batch_queries(buffer)
        processed += len(buffer)
        if processed >= 1000:
            rem = total - processed
            rate = processed / (time.time() - start_time)
            print(" -- {} of {} ({}/s) -- {}m remaining".format(processed,
                rem, round(rate, 1), round(rem / rate / 60, 2) ))



# called once -- after initial block sync
def rebuild_cache():
    print("*** Initial sync finished. Rebuilding cache. ***")
    cache_missing_posts()
    rebuild_feed_cache()

# the feed cache allows for efficient querying of blogs+reblogs. this method
# efficiently builds the feed cache after the initial sync.
def rebuild_feed_cache(truncate = True):
    print("*** Rebuilding hive_feed_cache ***")
    if truncate:
        query("TRUNCATE TABLE hive_feed_cache")

    query("INSERT IGNORE INTO hive_feed_cache "
            "SELECT author account, id post_id, created_at "
            "FROM hive_posts WHERE depth = 0 AND is_deleted = 0")
    query("INSERT IGNORE INTO hive_feed_cache "
            "SELECT account, post_id, created_at FROM hive_reblogs")


# identify and insert missing cache rows
def cache_missing_posts(fast_mode = True):
    if fast_mode:
        where = "id > (SELECT IFNULL(MAX(post_id), 0) FROM hive_posts_cache)"
    else:
        where = "id NOT IN (SELECT post_id FROM hive_posts_cache)"

    sql = ("SELECT id, author, permlink FROM hive_posts "
           "WHERE is_deleted = 0 AND %s ORDER BY id" % where)
    rows = list(query(sql))
    print("[INIT] Found {} missing cache entries".format(len(rows)))
    update_posts_batch(rows, get_adapter())


# when a post gets paidout ensure we update its final state
def select_paidout_posts(block_date):
    sql = """
    SELECT post_id, author, permlink FROM hive_posts_cache
    WHERE post_id IN (SELECT post_id FROM hive_posts_cache
    WHERE is_paidout = 0 AND payout_at <= :date)
    """
    return list(query(sql, date=block_date))


# remove any rows from cache which belong to a deleted post
def clean_dead_posts():
    sl = ("DELETE FROM hive_posts_cache WHERE post_id IN "
        "(SELECT id FROM hive_posts WHERE is_deleted = 1)")
    query(sql)


# testing
# -------
def run():
    #cache_missing_posts(fast_mode=False)
    sqls = generate_cached_accounts_sql(['roadscape', 'ned', 'sneak', 'test-safari'])
    batch_queries(sqls)


if __name__ == '__main__':
    run()
