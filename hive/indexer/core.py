import json
import logging
import glob
import time
import re
import os

from funcy.seqs import first, second, drop, flatten
from hive.db.schema import setup, teardown
from hive.db.methods import query_one, query, query_row, db_last_block
from toolz import partition_all

from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts
from hive.indexer.steem_client import get_adapter
from hive.indexer.cache import select_missing_posts, rebuild_feed_cache, select_paidout_posts, update_posts_batch
from hive.indexer.community import process_json_community_op
from hive.indexer.normalize import load_json_key

log = logging.getLogger(__name__)

# block-level routines
# --------------------


def process_json_follow_op(account, op_json, block_date):
    """ Process legacy 'follow' plugin ops (follow/mute/clear, reblog) """
    if type(op_json) != list:
        return
    if len(op_json) != 2:
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
            return  # impersonation
        if not all(filter(Accounts.exists, [follower, following])):
            return  # invalid input

        sql = """
        INSERT IGNORE INTO hive_follows (follower, following, created_at, state)
        VALUES (:fr, :fg, :at, :state) ON DUPLICATE KEY UPDATE state = :state
        """
        state = {'clear': 0, 'blog': 1, 'ignore': 2}[what]
        query(sql, fr=follower, fg=following, at=block_date, state=state)

    elif cmd == 'reblog':
        blogger = op_json['account']
        author = op_json['author']
        permlink = op_json['permlink']

        if blogger != account:
            return  # impersonation
        if not all(filter(Accounts.exists, [author, blogger])):
            return

        post_id, depth = Posts.get_id_and_depth(author, permlink)

        if depth > 0:
            return  # prevent comment reblogs

        if not post_id:
            print("reblog: post not found: {}/{}".format(author, permlink))
            return

        if 'delete' in op_json and op_json['delete'] == 'delete':
            query("DELETE FROM hive_reblogs WHERE account = :a AND post_id = :pid LIMIT 1", a=blogger, pid=post_id)
            sql = "DELETE FROM hive_feed_cache WHERE account = :account AND post_id = :id"
            query(sql, account=blogger, id=post_id)
        else:
            query("INSERT IGNORE INTO hive_reblogs (account, post_id, created_at) "
                  "VALUES (:a, :pid, :date)", a=blogger, pid=post_id, date=block_date)
            sql = "INSERT IGNORE INTO hive_feed_cache (account, post_id, created_at) VALUES (:account, :id, :created_at)"
            query(sql, account=blogger, id=post_id, created_at=block_date)


# process a single block. always wrap in a transaction!
def process_block(block, is_initial_sync=False):
    date = block['timestamp']
    block_id = block['block_id']
    prev = block['previous']
    block_num = int(block_id[:8], base=16)
    txs = block['transactions']

    query("INSERT INTO hive_blocks (num, hash, prev, txs, created_at) "
          "VALUES (:num, :hash, :prev, :txs, :date)",
          num=block_num, hash=block_id, prev=prev, txs=len(txs), date=date)

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

    Accounts.register(accounts, date)  # if an account does not exist, mark it as created in this block
    Posts.register(comments, date)  # if this is a new post, add the entry and validate community param
    Posts.delete(deleted)  # mark hive_posts.is_deleted = 1

    for op in json_ops:
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
        op_json = load_json_key(op, 'json')

        if op['id'] == 'follow':
            if block_num < 6000000 and type(op_json) != list:
                op_json = ['follow', op_json]  # legacy compat
            process_json_follow_op(account, op_json, date)
        elif op['id'] == 'com.steemit.community':
            if block_num > 13e6:
                process_json_community_op(account, op_json, date)

    # return all posts modified this block
    return dirty


# batch-process blocks, wrap in a transaction
def process_blocks(blocks, is_initial_sync=False):
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

    fn = lambda f: [int(f.split('/')[-1].split('.')[0]), f]
    mydir = os.path.dirname(os.path.realpath(__file__ + "/../.."))
    files = map(fn, glob.glob(mydir + "/checkpoints/*.json.lst"))
    files = sorted(files, key=lambda f: f[0])

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
    steemd = get_adapter()
    dirty = set()

    lbound = db_last_block() + 1
    ubound = steemd.last_irreversible_block_num()

    print("[SYNC] {} blocks to batch sync".format(ubound - lbound + 1))
    print("[SYNC] start sync from block %d" % lbound)

    while lbound < ubound:
        to = min(lbound + 1000, ubound)

        lap_0 = time.time()
        blocks = steemd.get_blocks_range(lbound, to)
        lap_1 = time.time()
        dirty |= process_blocks(blocks, is_initial_sync)
        lap_2 = time.time()

        rate = (to - lbound) / (lap_2 - lap_0)
        rps = int((to - lbound) / (lap_1 - lap_0))
        wps = int((to - lbound) / (lap_2 - lap_1))
        print("[SYNC] Got block {} ({}/s, {}rps {}wps) -- {}m remaining".format(
            to-1, round(rate, 1), rps, wps, round((ubound-to) / rate / 60, 2)))

        lbound = to

    # batch update post cache after catching up to head block
    if not is_initial_sync:

        print("[PREP] Update {} edited posts".format(len(dirty)))
        update_posts_batch(Posts.urls_to_tuples(dirty), steemd)

        date = steemd.head_time()
        paidout = select_paidout_posts(date)
        print("[PREP] Process {} payouts since {}".format(len(paidout), date))
        update_posts_batch(paidout, steemd, date)


def listen_steemd(trail_blocks=2):
    steemd = get_adapter()
    curr_block = db_last_block()
    last_hash = False

    while True:
        curr_block = curr_block + 1

        # if trailing too close, take a pause
        while trail_blocks > 0:
            if curr_block <= steemd.head_block() - trail_blocks:
                break
            time.sleep(0.5)

        # get the target block; if DNE, pause and retry
        block = steemd.get_block(curr_block)
        while not block:
            time.sleep(0.5)
            block = steemd.get_block(curr_block)

        num = int(block['block_id'][:8], base=16)
        print("[LIVE] Got block {} at {} with {} txs -- ".format(num,
            block['timestamp'], len(block['transactions'])), end='')

        # ensure the block we received links to our last
        if last_hash and last_hash != block['previous']:
            # this condition is very rare unless trail_blocks is 0 and fork is
            # encountered; to handle gracefully, implement a pop_block method
            raise Exception("Unlinkable block: have {}, got {} -> {})".format(
                last_hash, block['previous'], block['block_id']))
        last_hash = block['block_id']

        start_time = time.time()
        query("START TRANSACTION")

        dirty = process_block(block)
        update_posts_batch(Posts.urls_to_tuples(dirty), steemd, block['timestamp'])

        paidout = select_paidout_posts(block['timestamp'])
        update_posts_batch(paidout, steemd, block['timestamp'])

        print("{} edits, {} payouts".format(len(dirty), len(paidout)))
        query("COMMIT")
        secs = time.time() - start_time

        if secs > 1:
            print("WARNING: block {} process took {}s".format(num, secs))


def cache_missing_posts():
    # cached posts inserted sequentially, so just compare MAX(id)'s
    sql = ("SELECT (SELECT IFNULL(MAX(id), 0) FROM hive_posts) - "
           "(SELECT IFNULL(MAX(post_id), 0) FROM hive_posts_cache)")
    missing_count = query_one(sql)
    print("[INIT] Found {} missing post cache entries".format(missing_count))

    if not missing_count:
        return

    # process in batches of 1m posts
    missing = select_missing_posts(1e6)
    while missing:
        update_posts_batch(missing, get_adapter())
        missing = select_missing_posts(1e6)


def run():
    # if tables not created, do so now
    if not query_row('SHOW TABLES'):
        print("[INIT] No tables found. Initializing db...")
        setup()

    if db_last_block() == 0:
        for row in query("SHOW VARIABLES").fetchall():
            print(row)

    #TODO: if initial sync is interrupted, cache never rebuilt
    #TODO: do not build partial feed_cache during init_sync
    # if this is the initial sync, batch updates until very end
    is_initial_sync = not query_one("SELECT 1 FROM hive_posts_cache LIMIT 1")

    if is_initial_sync:
        print("[INIT] *** Initial sync. db_last_block: %d ***" % db_last_block())
    else:
        # perform cleanup in case process did not exit cleanly
        cache_missing_posts()

    # prefetch id->name memory map
    Accounts.load_ids()

    # fast block sync strategies
    sync_from_checkpoints(is_initial_sync)
    sync_from_steemd(is_initial_sync)

    if is_initial_sync:
        print("[INIT] *** Initial sync complete. Rebuilding cache. ***")
        cache_missing_posts()
        rebuild_feed_cache()

    # initialization complete. follow head blocks
    listen_steemd()


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = get_adapter().head_block()
    hive_head = db_last_block()
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    run()
