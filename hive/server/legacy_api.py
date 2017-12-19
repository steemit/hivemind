import json

from hive.db.methods import query, query_one, query_col, query_row, query_all
from hive.indexer.steem_client import get_adapter

#  INFO:jsonrpcserver.dispatcher.request:{"id":0,"jsonrpc":"2.0","method":"call","params":["database_api","get_state",["trending"]]}
async def call(api, method, params):
    # passthrough
    if method == 'get_dynamic_global_properties':
        return get_adapter()._gdgp() # condenser only uses total_vesting_fund_steem, total_vesting_shares
    elif method == 'get_accounts':
        return get_adapter().get_accounts(params[0])
    elif method == 'get_open_orders':
        return get_adapter()._client.exec('get_open_orders', params[0])
    elif method == 'get_block':
        return get_adapter()._client.exec('get_block', params[0])
    elif method == 'broadcast_transaction_synchronous':
        return get_adapter()._client.exec('broadcast_transaction_synchronous', params[0], api='network_broadcast_api')
    elif method == 'get_savings_withdraw_to':
        return get_adapter()._client.exec('get_savings_withdraw_to', params[0])
    elif method == 'get_savings_withdraw_from':
        return get_adapter()._client.exec('get_savings_withdraw_from', params[0])

    # native
    if method == 'get_state':
        return await get_state(params[0])
    elif method == 'get_content':
        return await get_content(params[0], params[1]) # after submit vote/post
    elif method == 'get_discussions_by_trending':
        return await get_discussions_by_trending(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'], params[0]['tag'])
    elif method == 'get_discussions_by_hot':
        return await get_discussions_by_hot(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'], params[0]['tag'])
    elif method == 'get_discussions_by_created':
        return await get_discussions_by_created(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'], params[0]['tag'])
    elif method == 'get_discussions_by_promoted':
        return await get_discussions_by_promoted(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'], params[0]['tag'])
    elif method == 'get_discussions_by_blog':
        return await get_discussions_by_blog(params[0]['tag'], params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'])
    elif method == 'get_discussions_by_feed':
        return await get_discussions_by_feed(params[0]['tag'], params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'])
    elif method == 'get_discussions_by_comments':
        return await get_discussions_by_comments(params[0]['start_author'], params[0]['start_permlink'], params[0]['limit'])
    elif method == 'get_replies_by_last_update':
        return await get_replies_by_last_update(params[0], params[1], params[2])
    elif method == 'get_following':
        return await get_following(params[0], params[1], params[2], params[3])
    elif method == 'get_followers':
        return await get_followers(params[0], params[1], params[2], params[3])
    elif method == 'get_follow_count':
        return await get_follow_count(params[0])
    else:
        raise Exception("not handled: {}/{}/{}".format(api, method, params))


async def get_followers(account: str, start: str, follow_type: str, limit: int):
    limit = _validate_limit(limit, 1000)
    state = _follow_type_to_int(follow_type)
    account_id = _get_account_id(account)
    seek = ''

    if start:
        seek = """
          AND hf.created_at <= (
            SELECT created_at FROM hive_follows
             WHERE following = :account_id AND follower = %d AND state = :state)
        """ % _get_account_id(start)

    sql = """
        SELECT name FROM hive_follows hf
          JOIN hive_accounts ON hf.follower = id
         WHERE hf.following = :account_id AND state = :state %s
      ORDER BY hf.created_at DESC LIMIT :limit
    """ % seek

    res = query_col(sql, account_id=account_id, state=state, limit=int(limit))
    return [dict(follower=r, following=account, what=[follow_type])
            for r in res]


async def get_following(account: str, start: str, follow_type: str, limit: int):
    limit = _validate_limit(limit, 1000)
    state = _follow_type_to_int(follow_type)
    account_id = _get_account_id(account)
    seek = ''

    if start:
        seek = """
          AND hf.created_at <= (
            SELECT created_at FROM hive_follows
             WHERE follower = :account_id AND following = %d AND state = :state)
        """ % _get_account_id(start)

    sql = """
        SELECT name FROM hive_follows hf
          JOIN hive_accounts ON hf.following = id
         WHERE hf.follower = :account_id AND state = :state %s
      ORDER BY hf.created_at DESC LIMIT :limit
    """ % seek

    res = query_col(sql, account_id=account_id, state=state, limit=int(limit))
    return [dict(follower=account, following=r, what=[follow_type])
            for r in res]


async def get_follow_count(account: str):
    sql = """
        SELECT name as account,
               following as following_count,
               followers as follower_count
          FROM hive_accounts WHERE name = :n
    """
    return dict(query_row(sql, n=account))


async def get_discussions_by_trending(start_author: str, start_permlink: str = '', limit: int = 20, tag: str = None):
    return _get_discussions('trending', start_author, start_permlink, limit, tag)

async def get_discussions_by_hot(start_author: str, start_permlink: str = '', limit: int = 20, tag: str = None):
    return _get_discussions('hot', start_author, start_permlink, limit, tag)

async def get_discussions_by_promoted(start_author: str, start_permlink: str = '', limit: int = 20, tag: str = None):
    return _get_discussions('promoted', start_author, start_permlink, limit, tag)

async def get_discussions_by_created(start_author: str, start_permlink: str = '', limit: int = 20, tag: str = None):
    return _get_discussions('created', start_author, start_permlink, limit, tag)


# author blog
async def get_discussions_by_blog(tag: str, start_author: str = '', start_permlink: str = '', limit: int = 20):
    limit = _validate_limit(limit, 20)
    account_id = _get_account_id(tag)
    seek = ''

    if start_permlink:
        start_id = _get_post_id(start_author, start_permlink)
        seek = """
          AND created_at <= (
            SELECT created_at FROM hive_feed_cache
             WHERE account_id = :account_id AND post_id = %d)
        """ % start_id

    sql = """
        SELECT post_id FROM hive_feed_cache WHERE account_id = :account_id %s
      ORDER BY created_at DESC LIMIT :limit
    """ % seek

    ids = query_col(sql, account_id=account_id, limit=limit)
    return _get_posts(ids)


# author feed
async def get_discussions_by_feed(tag: str, start_author: str = '', start_permlink: str = '', limit: int = 20):
    limit = _validate_limit(limit, 20)
    account_id = _get_account_id(tag)
    seek = ''

    if start_permlink:
        start_id = _get_post_id(start_author, start_permlink)
        seek = """
          HAVING MIN(hive_feed_cache.created_at) <= (
            SELECT MIN(created_at) FROM hive_feed_cache WHERE post_id = %d
               AND account_id IN (SELECT following FROM hive_follows
                                  WHERE follower = :account AND state = 1))
        """ % start_id

    sql = """
        SELECT post_id, string_agg(name, ',') accounts
          FROM hive_feed_cache
          JOIN hive_follows ON account_id = hive_follows.following AND state = 1
          JOIN hive_accounts ON hive_follows.following = hive_accounts.id
         WHERE hive_follows.follower = :account
      GROUP BY post_id %s
      ORDER BY MIN(hive_feed_cache.created_at) DESC LIMIT :limit
    """ % seek

    res = query_all(sql, account=account_id, limit=limit)
    posts = _get_posts([r[0] for r in res])

    # Merge reblogged_by data into result set
    accts = dict(res)
    for post in posts:
        rby = set(accts[post['post_id']].split(','))
        rby.discard(post['author'])
        if rby:
            post['reblogged_by'] = list(rby)

    return posts


# author comments
async def get_discussions_by_comments(start_author: str, start_permlink: str = '', limit: int = 20):
    limit = _validate_limit(limit, 20)
    seek = ''

    if start_permlink:
        start_id = _get_post_id(start_author, start_permlink)
        seek = ("AND created_at <= (SELECT created_at FROM hive_posts WHERE id = %d)"
                % start_id)

    sql = """
        SELECT id FROM hive_posts WHERE author = :account %s AND depth > 0
      ORDER BY created_at DESC LIMIT :limit
    """ % seek

    ids = query_col(sql, account=start_author, limit=limit)
    return _get_posts(ids)


# author replies
async def get_replies_by_last_update(start_author: str, start_permlink: str = '', limit: int = 20):
    limit = _validate_limit(limit, 50)
    parent = start_author
    seek = ''

    if start_permlink:
        parent, start_date = query_row("""
          SELECT p.author, c.created_at
            FROM hive_posts c
            JOIN hive_posts p ON c.parent_id = p.id
           WHERE c.author = :a AND c.permlink = :p
        """, a=start_author, p=start_permlink)
        seek = "AND created_at <= '%s'" % start_date

    sql = """
       SELECT id FROM hive_posts
        WHERE parent_id IN (SELECT id FROM hive_posts WHERE author = :parent) %s
     ORDER BY created_at DESC LIMIT :limit
    """ % seek

    ids = query_col(sql, parent=parent, limit=limit)
    return _get_posts(ids)


# https://github.com/steemit/steem/blob/06e67bd4aea73391123eca99e1a22a8612b0c47e/libraries/app/database_api.cpp#L1937
async def get_state(path: str):
    if path[0] == '/':
        path = path[1:]
    if not path:
        path = 'trending'
    part = path.split('/')
    if len(part) > 3:
        raise Exception("invalid path %s" % path)
    while len(part) < 3:
        part.append('')

    state = {}
    state['current_route'] = path
    state['props'] = {'total_vesting_fund_steem': '194924668.034 STEEM', 'total_vesting_shares': '399773972659.129698 VESTS', 'sbd_interest_rate': '0'} # TODO
    state['tags'] = {}
    state['tag_idx'] = {}
    state['tag_idx']['trending'] = [t['name'] for t in _get_trending_tags(50)]
    state['content'] = {}
    state['accounts'] = {}
    state['discussion_idx'] = {"": {}}
    state['feed_price'] = {"base": "1234.000 SBD", "quote": "1.000 STEEM"} # TODO?
    state1 = "{}".format(state)

    if part[0] and part[0][0] == '@':
        account = part[0][1:]
        state['accounts'][account] = get_adapter().get_accounts([account])[0] #_load_accounts([account])[0]

        if not part[1]:
            part[1] = 'blog'
        if part[2]:
            raise Exception("unknown account path part %s" % path)

        if part[1] == 'transfers':
            # TODO: proxy to steemd, or filter get_account_history
            state['accounts'][account]['transfer_history'] = []
            state['accounts'][account]['other_history'] = []

        elif part[1] == 'recent-replies':
            state['accounts'][account]['recent_replies'] = []
            replies = await get_replies_by_last_update(account, "", 20)
            for reply in replies:
                ref = reply['author'] + '/' + reply['permlink']
                state['accounts'][account]['recent_replies'].append(ref)
                state['content'][ref] = reply

        elif part[1] == 'comments':
            state['accounts'][account]['comments'] = []
            replies = await get_discussions_by_comments(account, "", 20)
            for reply in replies:
                ref = reply['author'] + '/' + reply['permlink']
                state['accounts'][account]['comments'].append(ref)
                state['content'][ref] = reply

        elif part[1] == 'blog':
            state['accounts'][account]['blog'] = []
            posts = await get_discussions_by_blog(account, "", "", 20)
            for post in posts:
                ref = post['author'] + '/' + post['permlink']
                state['accounts'][account]['blog'].append(ref)
                state['content'][ref] = post

        elif part[1] == 'feed':
            state['accounts'][account]['feed'] = []
            posts = await get_discussions_by_feed(account, "", "", 20)
            for post in posts:
                ref = post['author'] + '/' + post['permlink']
                state['accounts'][account]['feed'].append(ref)
                state['content'][ref] = post

        else:
            raise Exception("unknown account path %s" % path)

    # complete discussion
    elif part[1] and part[1][0] == '@':
        author = part[1][1:]
        permlink = part[2]
        state['content'] = _load_discussion_recursive(author, permlink)
        accounts = set(map(lambda p: p['author'], state['content'].values()))
        state['accounts'] = {a['name']: a for a in _load_accounts(accounts)}

    # trending pages
    elif part[0] in ['trending', 'promoted', 'hot', 'created']:
        sort = part[0]
        tag = part[1].lower()
        posts = _get_discussions(sort, '', '', 20, tag)
        state['discussion_idx'][tag] = {}
        state['discussion_idx'][tag][sort] = []
        for post in posts:
            ref = post['author'] + '/' + post['permlink']
            state['content'][ref] = post
            state['discussion_idx'][tag][sort].append(ref)

    # witness list
    elif part[0] == 'witnesses':
        raise Exception("not implemented")

    # tag "explorer"
    elif part[0] == "tags":
        state['tag_idx']['trending'] = []
        tags = _get_trending_tags(250)
        for t in tags:
            state['tag_idx']['trending'].append(t['name'])
            state['tags'][t['name']] = t

    else:
        raise Exception("unknown path {}".format(path))

    state2 = "{}".format(state)
    if state1 == state2:
        raise Exception("unrecognized path `{}`" % path)

    return state


async def get_content(author: str, permlink: str):
    post_id = _get_post_id(author, permlink)
    return _get_posts([post_id])[0]


async def get_content_replies(parent: str, parent_permlink: str):
    post_id = _get_post_id(parent, parent_permlink)
    post_ids = query_col("SELECT id FROM hive_posts WHERE parent_id = %d" % post_id)
    return _get_posts(post_ids)


def _get_trending_tags(limit: int):
    sql = """
      SELECT category,
             COUNT(*) AS total_posts,
             SUM(CASE WHEN depth = 0 THEN 1 ELSE 0 END) AS top_posts,
             SUM(payout) AS total_payouts
        FROM hive_posts_cache
       WHERE is_paidout = '0'
    GROUP BY category
    ORDER BY SUM(payout) DESC
       LIMIT :limit
    """
    out = []
    for row in query_all(sql, limit=limit):
        out.append({
            'comments': row['total_posts'] - row['top_posts'],
            'name': row['category'],
            'top_posts': row['top_posts'],
            'total_payouts': "%.3f SBD" % row['total_payouts']})

    return out

def _load_discussion_recursive(author, permlink):
    post_id = _get_post_id(author, permlink)
    if not post_id:
        raise Exception("Post not found: {}/{}".format(author, permlink))
    return _load_posts_recursive([post_id])

def _load_posts_recursive(post_ids):
    posts = _get_posts(post_ids)

    out = {}
    for post, post_id in zip(posts, post_ids):
        out[post['author'] + '/' + post['permlink']] = post

        child_ids = query_col("SELECT id FROM hive_posts WHERE parent_id = %d" % post_id)
        if child_ids:
            children = _load_posts_recursive(child_ids)
            post['replies'] = list(children.keys())
            out = {**out, **children}

    return out

# sort can be trending, hot, new, promoted
def _get_discussions(sort, start_author, start_permlink, limit, tag, context=None):
    limit = _validate_limit(limit, 20)

    col = ''
    where = []
    if sort == 'trending':
        col = 'sc_trend'
    elif sort == 'hot':
        col = 'sc_hot'
    elif sort == 'created':
        col = 'post_id'
        where.append('depth = 0')
    elif sort == 'promoted':
        col = 'promoted'
        where.append("is_paidout = '0'")
        where.append('promoted > 0')
    else:
        raise Exception("unknown sort order {}".format(sort))

    if tag:
        tagged_posts = "SELECT post_id FROM hive_post_tags WHERE tag = :tag"
        where.append("post_id IN (%s)" % tagged_posts)

    start_id = None
    if start_permlink:
        start_id = _get_post_id(start_author, start_permlink)
        sql = ("SELECT %s FROM hive_posts_cache %s ORDER BY %s DESC LIMIT 1"
               % (col, _where([*where, "post_id = :start_id"]), col))
        where.append("%s <= (%s)" % (col, sql))

    sql = ("SELECT post_id FROM hive_posts_cache %s ORDER BY %s DESC LIMIT :limit"
           % (_where(where), col))
    ids = query_col(sql, tag=tag, start_id=start_id, limit=limit)
    return _get_posts(ids, context)

def _load_accounts(names):
    sql = "SELECT id,name,display_name,about,reputation FROM hive_accounts WHERE name IN :names"
    accounts = []
    for row in query_all(sql, names=tuple(names)):
        account = {}
        account['name'] = row['name']
        account['reputation'] = _rep_to_raw(row['reputation'])
        account['json_metadata'] = json.dumps({'profile': {'name': row['display_name'], 'about': row['about']}})
        accounts.append(account)

    return accounts

def _where(conditions):
    if not conditions:
        return ''
    return 'WHERE ' + ' AND '.join(conditions)

def _validate_limit(limit, ubound=100):
    limit = int(limit)
    if limit <= 0:
        raise Exception("invalid limit")
    if limit > ubound:
        raise Exception("limit exceeded")
    return limit

def _follow_type_to_int(follow_type: str):
    if follow_type not in ['blog', 'ignore']:
        raise Exception("Invalid follow_type")
    return 1 if follow_type == 'blog' else 2

def _get_post_id(author, permlink):
    sql = "SELECT id FROM hive_posts WHERE author = :a AND permlink = :p"
    return query_one(sql, a=author, p=permlink)

def _get_account_id(name):
    return query_one("SELECT id FROM hive_accounts WHERE name = :n", n=name)

# given an array of post ids, returns full metadata in the same order
def _get_posts(ids, context=None):
    if not ids:
        raise Exception("no ids provided")

    sql = """
    SELECT post_id, author, permlink, title, body, promoted, payout, created_at,
           payout_at, is_paidout, rshares, raw_json, category, depth, json,
           children, votes, author_rep,

           preview, img_url, is_nsfw
      FROM hive_posts_cache WHERE post_id IN :ids
    """

    # key by id so we can return sorted by input order
    posts_by_id = {}
    for row in query(sql, ids=tuple(ids)).fetchall():
        row = dict(row)
        post = {}

        post['post_id'] = row['post_id']
        post['author'] = row['author']
        post['permlink'] = row['permlink']
        post['category'] = row['category']
        post['parent_permlink'] = ''
        post['parent_author'] = ''

        post['title'] = row['title']
        post['body'] = row['body']
        post['json_metadata'] = row['json']

        post['created'] = _json_date(row['created_at'])
        post['depth'] = row['depth']
        post['children'] = row['children']
        post['net_rshares'] = row['rshares']

        post['cashout_time'] = '1969-12-31T23:59:59' if row['is_paidout'] else _json_date(row['payout_at'])
        post['total_payout_value'] = ("%.3f SBD" % row['payout']) if row['is_paidout'] else '0.000 SBD'
        post['curator_payout_value'] = '0.000 SBD'
        post['pending_payout_value'] = '0.000 SBD' if row['is_paidout'] else ("%.3f SBD" % row['payout'])
        post['promoted'] = "%.3f SBD" % row['promoted']

        post['replies'] = []
        post['body_length'] = len(row['body'])
        post['active_votes'] = _hydrate_active_votes(row['votes'])
        post['author_reputation'] = _rep_to_raw(row['author_rep'])

        raw_json = {} if not row['raw_json'] else json.loads(row['raw_json'])
        if row['depth'] > 0:
            if raw_json:
                post['parent_permlink'] = raw_json['parent_permlink']
                post['parent_author'] = raw_json['parent_author']
            else:
                sql = "SELECT author, permlink FROM hive_posts WHERE id = (SELECT parent_id FROM hive_posts WHERE id = %d)"
                row2 = query_row(sql % row['post_id'])
                post['parent_permlink'] = row2['permlink']
                post['parent_author'] = row2['author']

        if raw_json:
            post['root_title'] = raw_json['root_title']
            post['max_accepted_payout'] = raw_json['max_accepted_payout']
            post['percent_steem_dollars'] = raw_json['percent_steem_dollars']
            post['url'] = raw_json['url']
            #post['net_votes']
            #post['allow_replies']
            #post['allow_votes']
            #post['allow_curation_rewards']
            #post['beneficiaries']
        else:
            post['root_title'] = 'RE: ' + post['title']

        posts_by_id[row['post_id']] = post

    # in rare cases of cache inconsistency, recover and warn
    missed = set(ids) - posts_by_id.keys()
    if missed:
        print("WARNING: get_posts do not exist in cache: {}".format(missed))
        for _id in missed:
            ids.remove(_id)

    return [posts_by_id[_id] for _id in ids]


def _hydrate_active_votes(vote_csv):
    if not vote_csv:
        return []

    votes = []
    for line in vote_csv.split("\n"):
        voter, rshares, percent, reputation = line.split(',')
        votes.append(dict(voter=voter, rshares=rshares, percent=percent, reputation=_rep_to_raw(reputation)))

    return votes

def _json_date(date):
    return 'T'.join(str(date).split(' '))

def _rep_to_raw(rep):
    if not isinstance(rep, (str, float, int)):
        return 0
    rep = float(rep) - 25
    rep = rep / 9
    rep = rep + 9
    sign = 1 if rep >= 0 else -1
    return int(sign * pow(10, rep))


if __name__ == '__main__':
    print(_load_discussion_recursive('roadscape', 'hello-world'))
