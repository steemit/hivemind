import json
import logging
import glob
import time
import re
import os

from funcy.seqs import drop
from toolz import partition_all

from hive.db.db_state import DbState
from hive.db.methods import query_one, query

from hive.indexer.accounts import Accounts
from hive.indexer.posts import Posts
from hive.indexer.cached_post import CachedPost
from hive.indexer.feed_cache import FeedCache
from hive.indexer.custom_op import CustomOp

from hive.indexer.steem_client import get_adapter
from hive.indexer.jobs import select_missing_posts, select_paidout_posts

log = logging.getLogger(__name__)

# block-level routines
# --------------------

def db_last_block():
    return query_one("SELECT MAX(num) FROM hive_blocks") or 0

# process a single block. always wrap in a transaction!
def process_block(block, is_initial_sync=False):
    date = block['timestamp']
    block_id = block['block_id']
    prev = block['previous']
    num = int(block_id[:8], base=16)
    txs = block['transactions']
    ops = sum([len(tx['operations']) for tx in txs])

    query("INSERT INTO hive_blocks (num, hash, prev, txs, ops, created_at) "
          "VALUES (:num, :hash, :prev, :txs, :ops, :date)",
          num=num, hash=block_id, prev=prev, txs=len(txs), ops=ops, date=date)

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
                Accounts.dirty(op['author'])
                if op['parent_author']:
                    Accounts.dirty(op['parent_author'])
            elif op_type == 'delete_comment':
                deleted.append(op)
            elif op_type == 'custom_json':
                json_ops.append(op)
            elif op_type == 'vote':
                dirty.add(op['author']+'/'+op['permlink'])
                Accounts.dirty(op['author'])
                Accounts.dirty(op['voter'])

    Accounts.register(accounts, date)  # if an account does not exist, mark it as created in this block
    Posts.register(comments, date)  # if this is a new post, add the entry and validate community param
    Posts.delete(deleted)  # mark hive_posts record as deleted
    CustomOp.process_ops(json_ops, num, date)  # take care of follows, reblogs, community actions

    # on initial sync, don't bother returning touched posts
    if is_initial_sync:
        return set()

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
    if ubound < lbound:
        return

    print("[SYNC] from %d +%d blocks to sync" % (lbound, ubound - lbound + 1))

    while lbound < ubound:
        to = min(lbound + 1000, ubound)

        lap_0 = time.perf_counter()
        blocks = steemd.get_blocks_range(lbound, to)
        lap_1 = time.perf_counter()
        dirty |= process_blocks(blocks, is_initial_sync)
        lap_2 = time.perf_counter()

        rate = (to - lbound) / (lap_2 - lap_0)
        rps = int((to - lbound) / (lap_1 - lap_0))
        wps = int((to - lbound) / (lap_2 - lap_1))
        print("[SYNC] Got block {} ({}/s, {}rps {}wps) -- {}m remaining".format(
            to-1, round(rate, 1), rps, wps, round((ubound-to) / rate / 60, 2)))

        lbound = to

    # batch update post cache after catching up to head block
    if not DbState.is_initial_sync():

        print("[PREP] Update {} edited posts".format(len(dirty)))
        CachedPost.update_batch(Posts.urls_to_tuples(dirty), steemd, None, True)

        date = steemd.head_time()
        paidout = select_paidout_posts(date)
        print("[PREP] Process {} payouts since {}".format(len(paidout), date))
        CachedPost.update_batch(paidout, steemd, date, True)

        Accounts.cache_dirty()
        Accounts.cache_dirty_follows()


def listen_steemd(trail_blocks=2):
    steemd = get_adapter()
    curr_block = db_last_block()
    last_hash = False

    while True:
        curr_block = curr_block + 1

        # if trailing too close, take a pause
        while trail_blocks > 0:
            gap = steemd.head_block() - curr_block
            if gap >= 25:
                print("[HIVE] gap too large: %d -- switch to fast sync" % gap)
                return
            if gap >= trail_blocks:
                break
            time.sleep(0.5)

        # get the target block; if DNE, pause and retry
        block = steemd.get_block(curr_block)
        while not block:
            time.sleep(0.5)
            block = steemd.get_block(curr_block)

        # ensure the block we received links to our last
        if last_hash and last_hash != block['previous']:
            # this condition is very rare unless trail_blocks is 0 and fork is
            # encountered; to handle gracefully, implement a pop_block method
            raise Exception("Unlinkable block: have {}, got {} -> {})".format(
                last_hash, block['previous'], block['block_id']))
        last_hash = block['block_id']

        start_time = time.perf_counter()
        query("START TRANSACTION")

        dirty = process_block(block)
        CachedPost.update_batch(Posts.urls_to_tuples(dirty), steemd, block['timestamp'], False)

        paidout = select_paidout_posts(block['timestamp'])
        CachedPost.update_batch(paidout, steemd, block['timestamp'], False)

        Accounts.cache_dirty()
        Accounts.cache_dirty_follows()

        query("COMMIT")

        num = int(block['block_id'][:8], base=16)
        print("[LIVE] Got block {} at {} with {} txs -- ".format(
            num, block['timestamp'], len(block['transactions'])), end='')
        print("{} edits, {} payouts".format(len(dirty), len(paidout)), end='')
        secs = time.perf_counter() - start_time
        print(" -- {}ms{}".format(int(secs * 1e3), ' SLOW' if secs > 1 else ''))

        # once a minute, update chain props
        if num % 20 == 0:
            update_chain_state()

        # approx once per hour, update accounts
        if num % 1200 == 0:
            print("Performing account maintenance...")
            Accounts.cache_oldest(50000)
            Accounts.update_ranks()


def cache_missing_posts(slow_mode=False):
    # cached posts inserted sequentially, so just compare MAX(id)'s
    sql = ("SELECT (SELECT COALESCE(MAX(id), 0) FROM hive_posts) - "
           "(SELECT COALESCE(MAX(post_id), 0) FROM hive_posts_cache)")
    missing_count = query_one(sql)
    print("[INIT] Found {} missing post cache entries".format(missing_count))

    if not missing_count and not slow_mode:
        return

    # process in batches of 1m posts
    missing = select_missing_posts(1e6, slow_mode)
    while missing:
        CachedPost.update_batch(missing, get_adapter())
        missing = select_missing_posts(1e6)


# refetch dynamic_global_properties, feed price, etc
def update_chain_state():
    state = get_adapter().gdgp_extended()
    query("""UPDATE hive_state SET block_num = :block_num,
             steem_per_mvest = :spm, usd_per_steem = :ups,
             sbd_per_steem = :sps, dgpo = :dgpo""",
          block_num=state['dgpo']['head_block_number'],
          spm=state['steem_per_mvest'],
          ups=state['usd_per_steem'],
          sps=state['sbd_per_steem'],
          dgpo=json.dumps(state['dgpo']))


def run():
    DbState.initialize()

    #TODO: if initial sync is interrupted, cache never rebuilt
    #TODO: do not build partial feed_cache during init_sync
    # if this is the initial sync, batch updates until very end
    is_initial_sync = DbState.is_initial_sync()

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
        FeedCache.rebuild()
        State.initial_sync_finished()

    # initialization complete. follow head blocks
    while True:
        listen_steemd()
        sync_from_steemd(False)


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = get_adapter().head_block()
    hive_head = db_last_block()
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    run()
