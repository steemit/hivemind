import json
import logging
import math
import collections
import time
import re

from funcy.seqs import first
from hive.db.methods import query, query_all, query_col
from hive.indexer.utils import amount, parse_time, get_adapter

logger = logging.getLogger(__name__)

def get_accounts_follow_stats(accounts):
    sql = """SELECT follower, COUNT(*) FROM hive_follows
            WHERE follower IN :lst GROUP BY follower"""
    following = dict(query(sql, lst=accounts).fetchall())
    for name in accounts:
        if name not in following:
            following[name] = 0

    sql = """SELECT following, COUNT(*) FROM hive_follows
            WHERE following IN :lst GROUP BY following"""
    followers = dict(query(sql, lst=accounts).fetchall())
    for name in accounts:
        if name not in followers:
            followers[name] = 0

    return {'followers': followers, 'following': following}


def trunc(string, maxlen):
    if string:
        string = string.strip()
        if len(string) > maxlen:
            string = string[0:(maxlen-3)] + '...'
    return string


def normalize_account_metadata(account):
    prof = {}
    try:
        prof = json.loads(account['json_metadata'])['profile']
        if not isinstance(prof, dict):
            prof = {}
    except:
        pass

    name = str(prof['name']) if 'name' in prof else None
    about = str(prof['about']) if 'about' in prof else None
    location = str(prof['location']) if 'location' in prof else None
    website = str(prof['website']) if 'website' in prof else None
    profile_image = str(prof['profile_image']) if 'profile_image' in prof else None
    cover_image = str(prof['cover_image']) if 'cover_image' in prof else None

    name = trunc(name, 20)
    about = trunc(about, 160)
    location = trunc(location, 30)

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
    if profile_image and len(profile_image) > 1024:
        profile_image = None
    if cover_image and len(cover_image) > 1024:
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

        update = ', '.join([k+" = :"+k for k in values.keys()][1:])
        sql = "UPDATE hive_accounts SET %s WHERE name = :name" % (update)
        sqls.append([(sql, values)])
    return sqls


def get_img_url(url, max_size=1024):
    if url and not isinstance(url, str):
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

    allow_delete = post['children'] == 0 and int(post['net_rshares']) <= 0
    has_pending_payout = amount(post['pending_payout_value']) >= 0.02
    author_rep = rep_log10(post['author_reputation'])

    gray_threshold = -9999999999
    low_value_post = net_rshares_adj < gray_threshold and author_rep < 65

    gray = not has_pending_payout and (author_rep < 1 or low_value_post)
    hide = not has_pending_payout and (author_rep < 0)

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
    def log10(string):
        leading_digits = int(string[0:4])
        log = math.log10(leading_digits) + 0.00000001
        num = len(string) - 1
        return num + (log - int(log))

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
    return ','.join((vote['voter'], str(vote['rshares']), str(vote['percent']),
                     str(rep_log10(vote['reputation']))))


def generate_cached_post_sql(pid, post, updated_at):
    if not post['author']:
        raise Exception("ERROR: post id {} has no chain state.".format(pid))

    md = None
    try:
        md = json.loads(post['json_metadata'])
        if not isinstance(md, dict):
            md = {}
    except json.decoder.JSONDecodeError:
        pass

    thumb_url = ''
    if md and 'image' in md:
        thumb_url = get_img_url(first(md['image'])) or ''
        md['image'] = [thumb_url]

    # clean up tags, check if nsfw
    tags = [post['category']]
    if md and 'tags' in md and isinstance(md['tags'], list):
        tags = tags + md['tags']
    tags = set(list(map(lambda str: (str or '').strip('# ').lower()[:32], tags))[0:5])
    tags.discard('')
    is_nsfw = int('nsfw' in tags)

    # payout date is last_payout if paid, and cashout_time if pending.
    is_paidout = (post['cashout_time'][0:4] == '1969')
    payout_at = post['last_payout'] if is_paidout else post['cashout_time']

    # get total rshares, and create comma-separated vote data blob
    rshares = sum(int(v['rshares']) for v in post['active_votes'])
    csvotes = "\n".join(map(vote_csv_row, post['active_votes']))

    payout_declined = False
    if amount(post['max_accepted_payout']) == 0:
        payout_declined = True
    elif len(post['beneficiaries']) == 1:
        benny = first(post['beneficiaries'])
        if benny['account'] == 'null' and int(benny['weight']) == 10000:
            payout_declined = True

    full_power = int(post['percent_steem_dollars']) == 0

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
        ('post_id', '%d' % pid),
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
        ('sc_hot', "%f" % hot_score),
        #('payout_declined', "%d" % int(payout_declined)),
        #('full_power', "%d" % int(full_power)),
    ])
    fields = values.keys()

    # Multiple SQL statements are generated for each post
    sqls = []

    # Update main metadata in the hive_posts_cache table
    cols = ', '.join(fields)
    params = ', '.join([':'+k for k in fields])
    update = ', '.join([k+" = :"+k for k in fields][1:])
    sql = "INSERT INTO hive_posts_cache (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s"
    sqls.append((sql % (cols, params, update), values))

    # update tag metadata only for top-level posts
    if post['depth'] == 0:
        sql = "DELETE FROM hive_post_tags WHERE post_id = :id"
        sqls.append((sql, {'id': pid}))

        if tags:
            sql = "INSERT IGNORE INTO hive_post_tags (post_id, tag) VALUES "
            params = {}
            vals = []
            for i, tag in enumerate(tags):
                vals.append("(:id, :t%d)" % i)
                params["t%d"%i] = tag
            sqls.append((sql + ','.join(vals), {'id': pid, **params}))
        else:
            print("post {} no tags: {}".format(pid, post))

    return sqls


def update_posts_batch(tuples, steemd, updated_at=None):
    # if calling function already has head_time, saves us a call
    if not updated_at:
        updated_at = steemd.head_time()

    # build url->id map
    ids = dict([[author+"/"+permlink, id] for (id, author, permlink) in tuples])
    posts = [[author, permlink] for (id, author, permlink) in tuples]

    total = len(posts)
    processed = 0
    for i in range(0, total, 1000):

        lap_0 = time.time()
        buffer = []
        for post in steemd.get_content_batch(posts[i:i+1000]):
            if not post['author']:
                continue # post has been deleted
            url = post['author'] + '/' + post['permlink']
            sql = generate_cached_post_sql(ids[url], post, updated_at)
            buffer.append(sql)

        lap_1 = time.time()
        batch_queries(buffer)
        lap_2 = time.time()

        if total >= 500:
            processed += len(buffer)
            rem = total - processed
            rate = len(buffer) / (lap_2 - lap_0)
            rps = int(len(buffer) / (lap_1 - lap_0))
            wps = int(len(buffer) / (lap_2 - lap_1))
            print(" -- post {} of {} ({}/s, {}rps {}wps) -- {}m remaining".format(
                processed, total, round(rate, 1), rps, wps, round(rem / rate / 60, 2)))


# the feed cache allows for efficient querying of blogs+reblogs. this method
# efficiently builds the feed cache after the initial sync.
def rebuild_feed_cache(truncate=True):
    print("[INIT] Rebuilding hive_feed_cache, this will take a few minutes.")
    if truncate:
        query("TRUNCATE TABLE hive_feed_cache")

    lap_0 = time.time()
    query("INSERT IGNORE INTO hive_feed_cache "
          "SELECT author account, id post_id, created_at "
          "FROM hive_posts WHERE depth = 0 AND is_deleted = 0")
    lap_1 = time.time()
    query("INSERT IGNORE INTO hive_feed_cache "
          "SELECT account, post_id, created_at FROM hive_reblogs")
    lap_2 = time.time()

    print("[INIT] Rebuilt hive_feed_cache in {}s ({}+{})".format(
          int(lap_2-lap_0), int(lap_1-lap_0), int(lap_2-lap_1)))


# identify and insert missing cache rows
def select_missing_posts(limit=None, fast_mode=True):
    if fast_mode:
        where = "id > (SELECT IFNULL(MAX(post_id), 0) FROM hive_posts_cache)"
    else:
        where = "id NOT IN (SELECT post_id FROM hive_posts_cache)"

    if limit:
        limit = "LIMIT %d" % limit
    else:
        limit = ""

    sql = ("SELECT id, author, permlink FROM hive_posts "
           "WHERE is_deleted = 0 AND %s ORDER BY id %s" % (where, limit))
    return list(query(sql))


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
    sql = ("DELETE FROM hive_posts_cache WHERE post_id IN "
           "(SELECT id FROM hive_posts WHERE is_deleted = 1)")
    query(sql)


def cache_all_accounts():
    accounts = query_col("SELECT name FROM hive_accounts")
    processed = 0
    total = len(accounts)

    for i in range(0, total, 1000):
        batch = accounts[i:i+1000]

        lap_0 = time.time()
        sqls = generate_cached_accounts_sql(batch)
        lap_1 = time.time()
        batch_queries(sqls)
        lap_2 = time.time()

        processed += len(batch)
        rem = total - processed
        rate = len(batch) / (lap_2 - lap_0)
        pct_db = int(100 * (lap_2 - lap_1) / (lap_2 - lap_0))
        print(" -- {} of {} ({}/s, {}% db) -- {}m remaining".format(
            processed, total, round(rate, 1), pct_db, round(rem / rate / 60, 2)))

# testing
# -------
def run():
    #sqls = generate_cached_accounts_sql(['roadscape', 'ned', 'sneak', 'test-safari'])
    cache_all_accounts()

if __name__ == '__main__':
    run()
