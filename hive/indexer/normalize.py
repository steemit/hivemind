import json
import math

from datetime import datetime


def amount(string):
    return float(string.split(' ')[0])

def parse_time(block_time):
    return datetime.strptime(block_time, '%Y-%m-%dT%H:%M:%S')

def load_json_key(obj, key):
    if not obj[key]:
        return {}
    ret = {}
    try:
        ret = json.loads(obj[key])
    except json.decoder.JSONDecodeError:
        return {}
    return ret

def trunc(string, maxlen):
    if string:
        string = string.strip()
        if len(string) > maxlen:
            string = string[0:(maxlen-3)] + '...'
    return string


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


def safe_img_url(url, max_size=1024):
    if url and not isinstance(url, str):
        url = None
    if url:
        url = url.strip()
    if url and len(url) < max_size and url[0:4] == 'http':
        return url


def safe_account_metadata(account):
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


def get_post_stats(post):
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
