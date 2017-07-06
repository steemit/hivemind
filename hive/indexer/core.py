import json
import logging
import glob
import time

from funcy.seqs import first, second, drop, flatten
from hive.db.methods import query_one, query, db_last_block
from steem.blockchain import Blockchain
from steem.steemd import Steemd
from steem.utils import parse_time, is_valid_account_name, json_expand
from toolz import partition_all

log = logging.getLogger(__name__)

from hive.indexer.cache import generate_cached_post_sql, cache_missing_posts, rebuild_feed_cache, sweep_paidout_posts
from hive.indexer.community import process_json_community_op, create_post_as

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
        query("DELETE FROM hive_posts_cache WHERE post_id = :id", id=post_id)
        sql = "DELETE FROM hive_feed_cache WHERE account = :account and id = :id"
        query(sql, account=op['author'], id=post_id)


# updates cache entry for posts (saves latest title, body, trending/hot score, payout, etc)
def update_posts(steemd, posts, date):
    for url in posts:
        author, permlink = url.split('/')
        id, is_deleted = first(query("SELECT id,is_deleted FROM hive_posts WHERE author = '%s' AND permlink = '%s'" % (author, permlink)))
        if not id:
            raise Exception("Post not found! {}/{}".format(author, permlink))
        if is_deleted:
            continue

        post = steemd.get_content(author, permlink)
        if not post['author']:
            print("WARNING: attemted to cache deleted post (id={}) @{}/{}".format(id, author, permlink))
            continue

        sqls = generate_cached_post_sql(id, post, date)
        for sql, params in sqls:
            query(sql, **params)

# given a comment op, safely read 'community' field from json
def get_op_community(comment):
    if not comment['json_metadata']:
        return None
    md = None
    try:
        md = json.loads(comment['json_metadata'])
    except:
        return None
    if md is not dict or 'community' not in md:
        return None
    return md['community']


# registers new posts (not edits), inserts into feed cache
def register_posts(ops, date):
    for op in ops:
        sql = "SELECT id, is_deleted FROM hive_posts WHERE author = '%s' AND permlink = '%s'"
        ret = first(query(sql % (op['author'], op['permlink'])))
        id = None
        if ret:
            if ret[1] == 0:
                continue  # ignore edits to posts
            else:
                id = ret[0]

        if op['parent_author'] == '':
            parent_id = None
            depth = 0
            category = op['parent_permlink']
            community = get_op_community(op) or op['author']
        else:
            parent_data = first(query("SELECT id, depth, category, community FROM hive_posts WHERE author = '%s' "
                                      "AND permlink = '%s'" % (op['parent_author'], op['parent_permlink'])))
            parent_id, parent_depth, category, community = parent_data
            depth = parent_depth + 1

        # will return None if invalid, defaults to author.
        community = create_post_as(community, op) or op['author']

        # if we're reusing a previously-deleted post (rare!), update it
        if id:
            query("UPDATE hive_posts SET is_deleted = 0, parent_id = %s, category = '%s', community = '%s', depth = %d WHERE id = %d" % (parent_id or 'NULL', category, community, depth, id))
            query("DELETE FROM hive_feed_cache WHERE account = :account AND id = :id", account=op['author'], id=id)
        else:
            query("INSERT INTO hive_posts (parent_id, author, permlink, category, community, depth, created_at) "
                  "VALUES (%s, '%s', '%s', '%s', '%s', %d, '%s')" % (
                      parent_id or 'NULL', op['author'], op['permlink'], category, community, depth, date))
            id = query_one("SELECT id FROM hive_posts WHERE author = '%s' AND permlink = '%s'" % (op['author'], op['permlink']))

        # add top-level posts to feed cache
        if depth is 0:
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
        if not all([key in op_json for key in ['follower', 'following']]):
            print("bad follow op: {} {}".format(block_date, op_json))
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

        if not post_id:
            print("reblog: post not found: {}/{}".format(author, permlink))
            return

        if 'delete' in op_json and op_json['delete'] == 'delete':
            query("DELETE FROM hive_reblogs WHERE account = '%s' AND post_id = %d LIMIT 1" % (blogger, post_id))
            sql = "DELETE FROM hive_feed_cache WHERE account = :account and id = :id"
            query(sql, account=blogger, id=post_id)
        else:
            query("INSERT IGNORE INTO hive_reblogs (account, post_id, created_at) "
                  "VALUES ('%s', %d, '%s')" % (blogger, post_id, block_date))
            sql = "INSERT IGNORE INTO hive_feed_cache (account, id, created_at) VALUES (:account, :id, :created_at)"
            query(sql, account=blogger, id=post_id, created_at=block_date)


# process a single block. always wrap in a transaction!
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

    # return all posts modified this block
    return dirty


# batch-process blocks, wrap in a transaction
def process_blocks(blocks, is_initial_sync = False):
    dirty = set()
    query("START TRANSACTION")
    for block in blocks:
        dirty |= process_block(block, is_initial_sync)
    query("COMMIT")
    return dirty



# sync routines
# -------------

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
    steemd = Steemd()
    dirty = set()

    lbound = db_last_block() + 1
    ubound = steemd.last_irreversible_block_num

    if not is_initial_sync:
        query("START TRANSACTION")

    start_num = lbound
    start_time = time.time()
    while lbound < ubound:
        to = min(lbound + 1000, ubound)
        blocks = steemd.get_blocks_range(lbound, to)
        lbound = to
        dirty |= process_blocks(blocks, is_initial_sync)

        rate = (lbound - start_num) / (time.time() - start_time)
        print("[SYNC] Got block {} ({}/s) {}m remaining".format(
            to - 1, round(rate, 1), round((ubound-lbound) / rate / 60, 2)))

    if not is_initial_sync:
        # batch update post cache after catching up to head block
        date = steemd.get_dynamic_global_properties()['time']
        print("Updating {} edited posts.".format(len(dirty), date))
        update_posts(steemd, dirty, date)
        sweep_paidout_posts()
        query("COMMIT")


def listen_steemd():
    b = Blockchain(mode='head')
    s = Steemd()
    h = b.stream_from(
        start_block=db_last_block() + 1,
        full_blocks=True,
    )
    for block in h:
        if not block or not block['previous']:
            raise Exception("stream_from returned bad/empty block: {}".format(block))

        num = int(block['previous'][:8], base=16) + 1
        print("[LIVE] Got block {} at {} with {} txs".format(num,
            block['timestamp'], len(block['transactions'])), end='')

        query("START TRANSACTION")
        dirty = process_block(block)
        print(" -- {} post edits -- ".format(len(dirty)), end = '')
        update_posts(s, dirty, block['timestamp'])
        sweep_paidout_posts()
        query("COMMIT")


def run():
    # if this is the initial sync, do not waste cycles updating caches.. we'll do it in bulk
    is_initial_sync = query_one("SELECT 1 FROM hive_posts_cache LIMIT 1") is None

    if not is_initial_sync:
        # perform cleanup in case process did not exit cleanly
        cache_missing_posts()

    # fast-load checkpoint files
    sync_from_checkpoints(is_initial_sync)

    # fast-load from steemd
    sync_from_steemd(is_initial_sync)

    # upon completing initial sync, perform some batch processing
    if is_initial_sync:
        print("Initial sync finished. Rebuilding cache...")
        cache_missing_posts()
        rebuild_feed_cache()

    # initialization complete. follow head blocks
    listen_steemd()


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = Steemd().last_irreversible_block_num
    hive_head = db_last_block()
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    run()
