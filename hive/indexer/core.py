import json
import logging
import glob
import time

from funcy.seqs import first, second, drop, flatten
from hive.community.roles import get_user_role, privacy_map, permissions, is_permitted
from hive.db.methods import query_one, query, db_last_block
from steem.blockchain import Blockchain
from steem.steemd import Steemd
from steem.utils import parse_time, is_valid_account_name, json_expand
from toolz import partition_all

log = logging.getLogger(__name__)

from cache import generate_cached_post_sql, cache_missing_posts, rebuild_feed_cache

# core
# ----
def get_account_id(name):
    if is_valid_account_name(name):
        return query_one("SELECT id FROM hive_accounts WHERE name = '%s' LIMIT 1" % name)


def get_post_id_and_depth(author, permlink):
    res = None
    if author:
        res = first(query(
            "SELECT id, depth FROM hive_posts WHERE author = '%s' AND permlink = '%s'" % (author, permlink)))
    return res or (None, -1)


def register_accounts(accounts, date):
    for account in set(accounts):
        if not get_account_id(account):
            query("INSERT INTO hive_accounts (name, created_at) VALUES ('%s', '%s')" % (account, date))


# marks posts as deleted and removes them from feed cache
def delete_posts(ops):
    for op in ops:
        query("UPDATE hive_posts SET is_deleted = 1 WHERE author = '%s' AND permlink = '%s'" % (
            op['author'], op['permlink']))
        post_id, depth = get_post_id_and_depth(op['author'], op['permlink'])
        sql = "DELETE FROM hive_feed_cache WHERE account = :account and id = :id"
        query(sql, account=op['author'], id=post_id)


# updates cache entry for posts (saves latest title, body, trending/hot score, payout, etc)
def update_posts(posts, date):
    for url in posts:
        author, permlink = url.split('/')
        id = query_one("SELECT id FROM hive_posts WHERE author = '%s' AND permlink = '%s'" % (author, permlink))
        post = Steemd().get_content(author, permlink)
        sql, params = generate_cached_post_sql(id, post, date)
        query(sql, **params)


# registers new posts (not edits), inserts into feed cache
def register_posts(ops, date):
    for op in ops:
        is_edit = query_one(
            "SELECT 1 FROM hive_posts WHERE author = '%s' AND permlink = '%s'" % (op['author'], op['permlink']))
        if is_edit:
            continue  # ignore edits to posts

        # this method needs to perform auth checking e.g. is op.author authorized to post in op.community?
        community_or_blog = create_post_as(op) or op['author']

        if op['parent_author'] == '':
            parent_id = None
            depth = 0
            category = op['parent_permlink']
        else:
            parent_data = first(query("SELECT id, depth, category FROM hive_posts WHERE author = '%s' "
                                      "AND permlink = '%s'" % (op['parent_author'], op['parent_permlink'])))
            parent_id, parent_depth, category = parent_data
            depth = parent_depth + 1

        query("INSERT INTO hive_posts (parent_id, author, permlink, category, community, depth, created_at) "
              "VALUES (%s, '%s', '%s', '%s', '%s', %d, '%s')" % (
                  parent_id or 'NULL', op['author'], op['permlink'], category, community_or_blog, depth, date))
        if depth is 0:
            id = query_one("SELECT id FROM hive_posts WHERE author = '%s' AND permlink = '%s'" % (op['author'], op['permlink']))
            sql = "INSERT INTO hive_feed_cache (account, id, created_at) VALUES (:account, :id, :created_at)"
            query(sql, account=op['author'], id=id, created_at=date)



def process_json_follow_op(account, op_json, block_date):
    """ This method processes any legacy 'follow' plugin ops (follow/mute/clear, reblog) """
    if type(op_json) != list:
        return
    if first(op_json) not in ['follow', 'reblog']:
        return
    if not isinstance(second(op_json), dict):
        return

    cmd, op_json = op_json  # ['follow', {data...}]
    if cmd == 'follow':
        if type(op_json['what']) != list:
            return
        what = first(op_json['what']) or 'clear'
        if what not in ['blog', 'clear', 'ignore']:
            return

        follower = op_json['follower']
        following = op_json['following']

        if follower != account:
            return  # impersonation attempt
        if not all(filter(is_valid_account_name, [follower, following])):
            return

        if what == 'clear':
            query("DELETE FROM hive_follows WHERE follower = '%s' "
                  "AND following = '%s' LIMIT 1" % (follower, following))
        else:
            fields = {'follower': follower, 'following': following,
                    'created_at': block_date, 'is_muted': int(what == 'ignore')}
            query("INSERT IGNORE INTO hive_follows (follower, following, created_at, is_muted) "
                    "VALUES (:follower, :following, :created_at, :is_muted) "
                    "ON DUPLICATE KEY UPDATE is_muted = :is_muted", **fields)

    elif cmd == 'reblog':
        blogger = op_json['account']
        author = op_json['author']
        permlink = op_json['permlink']

        if blogger != account:
            return  # impersonation
        if not all(filter(is_valid_account_name, [author, blogger])):
            return

        post_id, depth = get_post_id_and_depth(author, permlink)

        if depth > 0:
            return  # prevent comment reblogs

        if 'delete' in op_json and op_json['delete'] == 'delete':
            query("DELETE FROM hive_reblogs WHERE account = '%s' AND post_id = %d LIMIT 1" % (blogger, post_id))
            sql = "DELETE FROM hive_feed_cache WHERE account = :account and id = :id"
            query(sql, account=blogger, id=post_id)
        else:
            query("INSERT IGNORE INTO hive_reblogs (account, post_id, created_at) "
                  "VALUES ('%s', %d, '%s')" % (blogger, post_id, block_date))
            sql = "INSERT INTO hive_feed_cache (account, id, created_at) VALUES (:account, :id, :created_at)"
            query(sql, account=blogger, id=post_id, created_at=block_date)

# community methods
# -----------------
def process_json_community_op(account, op_json, date):
    cmd_name, cmd_op = op_json  # ['flagPost', {community: '', author: '', ...}]

    commands = list(flatten(permissions.values()))
    if cmd_name not in commands:
        return

    community = cmd_op['community']
    community_exists = is_community(community)

    # special case: community creation. TODO: does this require ACTIVE auth? or POSTING will suffice?
    if cmd_name == 'create' and not community_exists:
        if account != community:  # only the OWNER may create
            return

        ctype = cmd_op['type']  # restricted, open-comment, public
        # INSERT INTO hive_communities (account, name, about, description, lang, is_nsfw, is_private, created_at)
        # VALUES ('%s', '%s', '%s', '%s', '%s', %d, %d, '%s')" % [account, name, about, description, lang, is_nsfw ? 1 : 0, is_private ? 1 : 0, block_date]
        # INSERT ADMINS---

    # validate permissions
    if not community_exists or not is_permitted(account, community, cmd_name):
        return

    # If command references a post, ensure it's valid
    post_id, depth = get_post_id_and_depth(cmd_op.get('author'), cmd_op.get('permlink'))
    if not post_id:
        return

    # If command references an account, ensure it's valid
    account_id = get_account_id(cmd_op.get('account'))

    # If command references a list of accounts, ensure they are valid
    account_ids = list(map(get_account_id, cmd_op.get('accounts')))

    # ADMIN Actions
    # -------------
    if cmd_name == 'add_admins':
        assert account_ids
        # UPDATE hive_members SET is_admin = 1 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'remove_admins':
        assert account_ids
        # todo: validate at least one admin remains!!!
        # UPDATE hive_members SET is_admin = 0 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'add_mods':
        assert account_ids
        # UPDATE hive_members SET is_mod = 1 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'remove_mods':
        assert account_ids
        # UPDATE hive_members SET is_mod = 0 WHERE account IN (%s) AND community = '%s'

    # MOD USER Actions
    # ----------------
    if cmd_name == 'update_settings':
        # name, about, description, lang, is_nsfw
        # settings {bg_color, bg_color2, text_color}
        # UPDATE hive_communities SET .... WHERE community = '%s'
        assert account_id

    if cmd_name == 'add_posters':
        assert account_ids
        # UPDATE hive_members SET is_approved = 1 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'remove_posters':
        assert account_ids
        # UPDATE hive_members SET is_approved = 0 WHERE account IN (%s) AND community = '%s'

    if cmd_name == 'mute_user':
        assert account_id
        # UPDATE hive_members SET is_muted = 1 WHERE account = '%s' AND community = '%s'

    if cmd_name == 'unmute_user':
        assert account_id
        # UPDATE hive_members SET is_muted = 0 WHERE account = '%s' AND community = '%s'

    if cmd_name == 'set_user_title':
        assert account_id
        # UPDATE hive_members SET title = '%s' WHERE account = '%s' AND community = '%s'

    # MOD POST Actions
    # ----------------
    if cmd_name == 'mute_post':
        assert post_id
        # assert all([account_id, post_id])
        # UPDATE hive_posts SET is_muted = 1 WHERE community = '%s' AND author = '%s' AND permlink = '%s'

    if cmd_name == 'unmute_post':
        assert post_id
        # UPDATE hive_posts SET is_muted = 0 WHERE community = '%s' AND author = '%s' AND permlink = '%s'

    if cmd_name == 'pin_post':
        assert post_id
        # UPDATE hive_posts SET is_pinned = 1 WHERE community = '%s' AND author = '%s' AND permlink = '%s'

    if cmd_name == 'unpin_post':
        assert post_id
        # UPDATE hive_posts SET is_pinned = 0 WHERE community = '%s' AND author = '%s' AND permlink = '%s'

    # GUEST POST Actions
    # ------------------
    if cmd_name == 'flag_post':
        assert post_id
        # INSERT INTO hive_flags (account, community, author, permlink, comment, created_at) VALUES ()

    # track success (TODO: failures as well?)
    # INSERT INTO hive_modlog (account, community, action, created_at) VALUES  (account, community, json.inspect, block_date)
    return True


def create_post_as(comment: dict) -> str:
    """ Given a new Steem post/comment, add it to appropriate community.
    
    For a comment to be valid, these conditions apply:
        - Post must be new (edits don't count)
        - Author is allowed to post in this community (membership & privacy)
        - Author is not muted in this community
        
    
    Args:
        comment (dict): Operation with the post to add.
        
    Returns:
        name (str): If all conditions apply, community name we're posting into.
                    Otherwise, authors own name (blog) is returned.
    """

    if comment['json_metadata'] == "":
        return None

    md = None
    try:
        md = json.loads(comment['json_metadata'])
    except:
        return None

    if md is not dict or 'community' not in md:
        return None

    author = comment['author']
    community = md['community']
    community_props = get_community(community)

    if not community_props:
        return None

    if is_author_muted(author, community):
        return None

    privacy = privacy_map[community_props['privacy']]
    if privacy == 'open':
        pass
    elif privacy == 'restricted':
        # guests cannot create top-level posts in restricted communities
        if comment['parent_author'] == "" and get_user_role(author, community) == 'guest':
            return None
    elif privacy == 'closed':
        # we need at least member permissions to post or comment
        if get_user_role(author, community) == 'guest':
            return None

    return community


def get_community(community_name):
    # sqlalchemy:
    # q = select([hive_communities]).where(hive_communities.c.account == community_name).limit(1)
    # conn.execute(q).fetchall()
    return first(query("SELECT * FROM hive_communities WHERE name = '%s' LIMIT 1" % community_name))


def is_author_muted(author_name: str, community_name: str) -> bool:
    return get_user_role(author_name, community_name) is 'muted'


def is_community(name: str) -> bool:
    """ Given a community name, check if its a valid community."""
    return bool(get_community(name))


# run indexer
# -----------
def process_block(block, is_initial_sync = False):
    date = parse_time(block['timestamp'])
    block_id = block['block_id']
    prev = block['previous']
    block_num = int(block_id[:8], base=16)
    txs = block['transactions']

    query("INSERT INTO hive_blocks (num, hash, prev, txs, created_at) "
          "VALUES (%d, '%s', '%s', %d, '%s')" % (block_num, block_id, prev, len(txs), date))

    accounts = set()
    comments = []
    json_ops = []
    deleted = []
    dirty = set()
    for tx in txs:
        for operation in tx['operations']:
            op_type, op = operation

            if op_type == 'pow':
                accounts.add(op['worker_account'])
            elif op_type == 'pow2':
                accounts.add(op['work'][1]['input']['worker_account'])
            elif op_type in ['account_create', 'account_create_with_delegation']:
                accounts.add(op['new_account_name'])
            elif op_type == 'comment':
                comments.append(op)
                dirty.add(op['author']+'/'+op['permlink'])
            elif op_type == 'delete_comment':
                deleted.append(op)
            elif op_type == 'custom_json':
                json_ops.append(op)
            elif op_type == 'vote':
                dirty.add(op['author']+'/'+op['permlink'])

    register_accounts(accounts, date)  # if an account does not exist, mark it as created in this block
    register_posts(comments, date)  # if this is a new post, add the entry and validate community param
    delete_posts(deleted)  # mark hive_posts.is_deleted = 1

    # if we're streaming, update cache each block
    if not is_initial_sync:
        update_posts(dirty, date)

    for op in map(json_expand, json_ops):
        if op['id'] not in ['follow', 'com.steemit.community']:
            continue

        # we are assuming `required_posting_auths` is always used and length 1.
        # it may be that some ops will require `required_active_auths` instead
        # (e.g. if we use that route for admin action of acct creation)
        # if op['required_active_auths']:
        #    log.warning("unexpected active auths: %s" % op)
        if len(op['required_posting_auths']) != 1:
            log.warning("unexpected auths: %s" % op)
            continue

        account = op['required_posting_auths'][0]
        op_json = op['json']

        if op['id'] == 'follow':
            if block_num < 6000000 and type(op_json) != list:
                op_json = ['follow', op_json]  # legacy compat
            process_json_follow_op(account, op_json, date)
        elif op['id'] == 'com.steemit.community':
            process_json_community_op(account, op_json, date)


def process_blocks(blocks, is_initial_sync = False):
    query("START TRANSACTION")
    for block in blocks:
        process_block(block, is_initial_sync)
    query("COMMIT")


def sync_from_checkpoints(is_initial_sync):
    last_block = db_last_block()

    fn = lambda f: [int(f.split('/')[1].split('.')[0]), f]
    files = map(fn, glob.glob("checkpoints/*.json.lst"))
    files = sorted(files, key = lambda f: f[0])

    last_read = 0
    for (num, path) in files:
        if last_block < num:
            print("[SYNC] Load {} -- last block: {}".format(path, last_block))
            skip_lines = last_block - last_read
            sync_from_file(path, skip_lines, 250, is_initial_sync)
            last_block = num
        last_read = num


def sync_from_file(file_path, skip_lines, chunk_size=250, is_initial_sync=False):
    with open(file_path) as f:
        # each line in file represents one block
        # we can skip the blocks we already have
        remaining = drop(skip_lines, f)
        for batch in partition_all(chunk_size, remaining):
            process_blocks(map(json.loads, batch), is_initial_sync)


def sync_from_steemd(is_initial_sync):
    s = Steemd()

    lbound = db_last_block() + 1
    ubound = s.last_irreversible_block_num

    start_num = lbound
    start_time = time.time()
    while lbound < ubound:
        to = min(lbound + 1000, ubound)
        blocks = s.get_blocks_range(lbound, to)
        lbound = to
        process_blocks(blocks, is_initial_sync)

        rate = (lbound - start_num) / (time.time() - start_time)
        print("[SYNC] Got block {} ({}/s) {}m remaining".format(
            to - 1, round(rate, 1), round((ubound-lbound) / rate / 60, 2)))


def listen_steemd():
    b = Blockchain()
    h = b.stream_from(
        start_block=db_last_block() + 1,
        full_blocks=True,
    )
    for block in h:
        num = int(block['previous'][:8], base=16) + 1
        print("[LIVE] Got block {} at {} with {} txs".format(num,
            block['timestamp'], len(block['transactions'])))
        process_blocks([block], False)


# testing
# -------
def run():

    # if this is the initial sync, do not waste cycles updating caches.. we'll do it in bulk
    is_initial_sync = query_one("SELECT COUNT(*) FROM hive_posts_cache") is 0

    # fast-load checkpoint files
    sync_from_checkpoints(is_initial_sync)

    # fast-load from steemd
    sync_from_steemd(is_initial_sync)

    # upon completing initial sync, perform some batch processing
    if is_initial_sync:
        cache_missing_posts()
        rebuild_feed_cache()

    # follow head blocks
    listen_steemd()


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = Steemd().last_irreversible_block_num
    hive_head = db_last_block()
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    # setup()
    run()
