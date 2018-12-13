"""Handles building condenser_api-compatible response objects."""

import logging
import ujson as json

from hive.db.methods import query_all, query_row

log = logging.getLogger(__name__)

# Building of legacy account objects

def load_accounts(names):
    """`get_accounts`-style lookup for `get_state` compat layer."""
    sql = """SELECT id, name, display_name, about, reputation, vote_weight,
                    created_at, post_count, profile_image, location, website,
                    cover_image
               FROM hive_accounts WHERE name IN :names"""
    rows = query_all(sql, names=tuple(names))
    return [_condenser_account_object(row) for row in rows]

def load_posts_reblogs(ids_with_reblogs, truncate_body=0):
    """Given a list of (id, reblogged_by) tuples, return posts w/ reblog key."""
    post_ids = [r[0] for r in ids_with_reblogs]
    reblog_by = dict(ids_with_reblogs)
    posts = load_posts(post_ids, truncate_body=truncate_body)

    # Merge reblogged_by data into result set
    for post in posts:
        rby = set(reblog_by[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts

def load_posts(ids, truncate_body=0):
    """Given an array of post ids, returns full objects in the same order."""
    if not ids:
        return []

    sql = """
    SELECT post_id, author, permlink, title, body, promoted, payout, created_at,
           payout_at, is_paidout, rshares, raw_json, category, depth, json,
           children, votes, author_rep, updated_at,

           preview, img_url, is_nsfw
      FROM hive_posts_cache WHERE post_id IN :ids
    """

    # key by id so we can return sorted by input order
    posts_by_id = {}
    for row in query_all(sql, ids=tuple(ids)):
        row = dict(row)
        post = _condenser_post_object(row, truncate_body=truncate_body)
        posts_by_id[row['post_id']] = post

    # in rare cases of cache inconsistency, recover and warn
    missed = set(ids) - posts_by_id.keys()
    if missed:
        log.warning("get_posts do not exist in cache: %s", repr(missed))
        for _id in missed:
            sql = ("SELECT id, author, permlink, depth, created_at, is_deleted "
                   "FROM hive_posts WHERE id = :id")
            log.warning("missing: %s", dict(query_row(sql, id=_id)))
            ids.remove(_id)

    return [posts_by_id[_id] for _id in ids]


def _condenser_account_object(row):
    """Convert an internal account record into legacy-steemd style."""
    return {
        'name': row['name'],
        'created': str(row['created_at']),
        'post_count': row['post_count'],
        'reputation': _rep_to_raw(row['reputation']),
        'net_vesting_shares': row['vote_weight'],
        'transfer_history': [],
        'json_metadata': json.dumps({
            'profile': {'name': row['display_name'],
                        'about': row['about'],
                        'website': row['website'],
                        'location': row['location'],
                        'cover_image': row['cover_image'],
                        'profile_image': row['profile_image'],
                       }})}

def _condenser_post_object(row, truncate_body=0):
    """Given a hive_posts_cache row, create a legacy-style post object."""
    paid = row['is_paidout']

    post = {}
    post['post_id'] = row['post_id']
    post['author'] = row['author']
    post['permlink'] = row['permlink']
    post['category'] = row['category']

    post['title'] = row['title']
    post['body'] = row['body'][0:truncate_body] if truncate_body else row['body']
    post['json_metadata'] = row['json']

    post['created'] = _json_date(row['created_at'])
    post['last_updated'] = _json_date(row['updated_at'])
    post['depth'] = row['depth']
    post['children'] = row['children']
    post['net_rshares'] = row['rshares']

    post['last_payout'] = _json_date(row['payout_at'] if paid else None)
    post['cashout_time'] = _json_date(None if paid else row['payout_at'])
    post['total_payout_value'] = _amount(row['payout'] if paid else 0)
    post['curator_payout_value'] = _amount(0)
    post['pending_payout_value'] = _amount(0 if paid else row['payout'])
    post['promoted'] = "%.3f SBD" % row['promoted']

    post['replies'] = []
    post['body_length'] = len(row['body'])
    post['active_votes'] = _hydrate_active_votes(row['votes'])
    post['author_reputation'] = _rep_to_raw(row['author_rep'])

    # import fields from legacy object
    assert row['raw_json']
    assert len(row['raw_json']) > 32
    raw_json = json.loads(row['raw_json'])

    if row['depth'] > 0:
        post['parent_author'] = raw_json['parent_author']
        post['parent_permlink'] = raw_json['parent_permlink']
    else:
        post['parent_author'] = ''
        post['parent_permlink'] = row['category']

    post['url'] = raw_json['url']
    post['root_title'] = raw_json['root_title']
    post['beneficiaries'] = raw_json['beneficiaries']
    post['max_accepted_payout'] = raw_json['max_accepted_payout']
    post['percent_steem_dollars'] = raw_json['percent_steem_dollars']

    # not used by condenser, but may be useful
    #post['net_votes'] = post['total_votes'] - row['up_votes']
    #post['allow_replies'] = raw_json['allow_replies']
    #post['allow_votes'] = raw_json['allow_votes']
    #post['allow_curation_rewards'] = raw_json['allow_curation_rewards']
    #post['curator_payout_value'] = raw_json['curator_payout_value'] if paid else _amount(0)
    #curator_payout = amount(raw_json['curator_payout_value'])
    #post['total_payout_value'] = _amount(row['payout'] - curator_payout) if paid else _amount(0)

    return post

def _amount(amount, asset='SBD'):
    """Return a steem-style amount string given a (numeric, asset-str)."""
    assert asset == 'SBD', 'unhandled asset %s' % asset
    return "%.3f SBD" % amount

def _hydrate_active_votes(vote_csv):
    """Convert minimal CSV representation into steemd-style object."""
    if not vote_csv:
        return []
    cols = 'voter,rshares,percent,reputation'.split(',')
    votes = vote_csv.split("\n")
    return [dict(zip(cols, line.split(','))) for line in votes]

def _json_date(date=None):
    """Given a db datetime, return a steemd/json-friendly version."""
    if not date:
        return '1969-12-31T23:59:59'
    return 'T'.join(str(date).split(' '))

def _rep_to_raw(rep):
    """Convert a UI-ready rep score back into its approx raw value."""
    if not isinstance(rep, (str, float, int)):
        return 0
    rep = float(rep) - 25
    rep = rep / 9
    rep = rep + 9
    sign = 1 if rep >= 0 else -1
    return int(sign * pow(10, rep))
