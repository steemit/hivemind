"""Hive API: post and comment object retrieval"""
import logging
from hive.server.hive_api.account import find_accounts
log = logging.getLogger(__name__)

async def comments_by_id(db, ids, observer=None):
    """Given an array of post ids, returns comment objects keyed by id."""
    assert ids, 'no ids passed to comments_by_id'

    sql = """SELECT post_id, author, permlink, body, depth,
                    payout, payout_at, is_paidout, created_at, updated_at,
                    rshares, is_hidden, is_grayed, votes
               FROM hive_posts_cache WHERE post_id IN :ids""" #votes
    result = await db.query_all(sql, ids=tuple(ids))

    by_id = {}
    for row in result:
        top_votes, observer_vote = _top_votes(row, 5, observer)
        post = {
            'id': row['post_id'],
            'author': row['author'],
            'url': row['author'] + '/' + row['permlink'],
            'depth': row['depth'],
            'body': row['body'],
            'payout': str(row['payout']),
            'created_at': str(row['created_at']),
            'updated_at': str(row['updated_at']),
            'payout_at': str(row['payout_at']),
            'is_paidout': row['is_paidout'],
            'rshares': row['rshares'],
            'hide': row['is_hidden'] or row['is_grayed'],
            'top_votes': top_votes,
        }

        if observer:
            post['context'] = {'vote_rshares': observer_vote}
        by_id[post['id']] = post

    return by_id


def posts_by_id(db, ids, observer=None, lite=True):
    """Given a list of post ids, returns lite post objects in the same order."""

    # pylint: disable=too-many-locals

    sql = """SELECT post_id, author, permlink, title, img_url, payout, promoted,
                    created_at, payout_at, is_nsfw, rshares, votes,
                    is_muted, is_invalid, %s
               FROM hive_posts_cache WHERE post_id IN :ids"""
    fields = ['preview'] if lite else ['body', 'updated_at', 'json']
    sql = sql % (', '.join(fields))

    reblogged_ids = _reblogged_ids(db, observer, ids) if observer else []

    # TODO: filter out observer's mutes?

    # key by id.. returns sorted by input order
    authors = set()
    by_id = {}
    for row in db.query_all(sql, ids=tuple(ids)):
        assert not row['is_muted']
        assert not row['is_invalid']
        pid = row['post_id']
        top_votes, observer_vote = _top_votes(row, 5, observer)

        obj = {
            'id': pid,
            'author': row['author'],
            'url': row['author'] + '/' + row['permlink'],
            'title': row['title'],
            'payout': float(row['payout']),
            'promoted': float(row['promoted']),
            'created_at': str(row['created_at']),
            'payout_at': str(row['payout_at']),
            'is_paidout': row['is_paidout'],
            'rshares' : row['rshares'],
            'hide': False, # TODO
            'top_votes' : top_votes,
            'thumb_url': row['img_url'],
            'is_nsfw' : row['is_nsfw'],
        }

        if lite:
            obj['preview'] = row['preview']
        else:
            obj['body'] = row['body']
            obj['updated_at'] = str(row['updated_at'])
            obj['json_metadata'] = row['json']

        if observer:
            obj['context'] = {
                'reblogged': obj['id'] in reblogged_ids,
                'vote_rshares': observer_vote
            }

        authors.add(obj['author'])
        by_id[row['post_id']] = obj

    # in rare cases of cache inconsistency, recover and warn
    missed = set(ids) - by_id.keys()
    if missed:
        log.warning("by_id do not exist in cache: %s", repr(missed))
        for _id in missed:
            ids.remove(_id)

    return {'posts': [by_id[_id] for _id in ids],
            'accounts': find_accounts(db, authors, observer)}

def _reblogged_ids(db, observer, post_ids):
    ids = db.query_col("""SELECT post_id FROM hive_reblogs
                           WHERE account = :observer
                             AND post_id IN :ids""",
                       observer=observer, ids=tuple(post_ids))
    return ids

def _top_votes(obj, limit, observer):
    observer_vote = None
    votes = []
    if obj['votes']:
        for csa in obj['votes'].split("\n"):
            print(">>>"+csa+"<<<<")
            voter, rshares = csa.split(",")[0:2]
            rshares = int(rshares)
            votes.append((voter, rshares))

            if observer == voter:
                observer_vote = rshares

    top = sorted(votes, key=lambda row: abs(int(row[1])), reverse=True)[:limit]

    return (top, observer_vote)


async def ranked_pids(db, sort, start_id, limit, communities, include_muted=False):
    """Get a list of post_ids for a given posts query.

    `sort` can be trending, hot, created, promoted, payout, or payout_comments.
    """
    # pylint: disable=too-many-arguments
    assert sort in ['trending', 'hot', 'created', 'promoted',
                    'payout', 'payout_comments']

    table = 'hive_posts_cache'
    field = ''
    where = []

    if sort == 'trending':
        field = 'sc_trend'
        where.append("is_paidout = '0'")
    elif sort == 'hot':
        field = 'sc_hot'
        where.append("is_paidout = '0'")
    elif sort == 'created':
        field = 'post_id'
        where.append('depth = 0')
    elif sort == 'promoted':
        field = 'promoted'
        where.append("is_paidout = '0'")
        where.append('promoted > 0')
    elif sort == 'payout':
        field = 'payout'
        where.append("is_paidout = '0'")
        where.append('depth = 0')
    elif sort == 'payout_comments':
        field = 'payout'
        where.append("is_paidout = '0'")
        where.append('depth > 0')

    if communities:
        where.append('community IN :communities')

    # TODO
    if not include_muted:
        where.append("is_muted = '0'")
        where.append("is_invalid = '0'")

    if start_id:
        sql = "%s <= (SELECT %s FROM %s WHERE post_id = :start_id)"
        where.append(sql % (field, field, table))

    sql = ("SELECT post_id FROM %s WHERE %s ORDER BY %s DESC LIMIT :limit"
           % (table, ' AND '.join(where), field))

    return await db.query_col(sql, communities=tuple(communities),
                              start_id=start_id, limit=limit)
