import json
import logging
import glob
import time
import os
import traceback
import resource

from funcy.seqs import drop
from toolz import partition_all

from hive.db.methods import query
from hive.db.db_state import DbState

from hive.indexer.timer import Timer
from hive.indexer.steem_client import get_adapter

from hive.indexer.blocks import Blocks
from hive.indexer.accounts import Accounts
from hive.indexer.cached_post import CachedPost
from hive.indexer.feed_cache import FeedCache
from hive.indexer.follow import Follow

log = logging.getLogger(__name__)

# sync routines
# -------------

def mem_stats(): #72
    max_mem = int(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss) / (1024 * 1024)
    print("[MEM] peak memory usage: %.2fMB" % max_mem)

def sync_from_checkpoints():
    last_block = Blocks.last()['num']

    _fn = lambda f: [int(f.split('/')[-1].split('.')[0]), f]
    mydir = os.path.dirname(os.path.realpath(__file__ + "/../.."))
    files = map(_fn, glob.glob(mydir + "/checkpoints/*.json.lst"))
    files = sorted(files, key=lambda f: f[0])

    last_read = 0
    for (num, path) in files:
        if last_block < num:
            print("[SYNC] Load {} -- last block: {}".format(path, last_block))
            skip_lines = last_block - last_read
            sync_from_file(path, skip_lines, 250)
            last_block = num
        last_read = num


def sync_from_file(file_path, skip_lines, chunk_size=250):
    with open(file_path) as f:
        # each line in file represents one block
        # we can skip the blocks we already have
        remaining = drop(skip_lines, f)
        for batch in partition_all(chunk_size, remaining):
            Blocks.process_multi(map(json.loads, batch), True)


def sync_from_steemd():
    is_initial_sync = DbState.is_initial_sync()
    steemd = get_adapter()

    lbound = Blocks.last()['num'] + 1
    ubound = steemd.last_irreversible_block_num()

    if ubound > lbound:
        print("[SYNC] start block %d, +%d to sync" % (lbound, ubound-lbound+1))

    timer = Timer(ubound - lbound, entity='block', laps=['rps', 'wps'])
    while lbound < ubound:
        to = min(lbound + 1000, ubound)

        timer.batch_start()
        blocks = steemd.get_blocks_range(lbound, to)
        timer.batch_lap()
        Blocks.process_multi(blocks, is_initial_sync)
        timer.batch_finish(len(blocks))
        date = blocks[-1]['timestamp']
        print(timer.batch_status("[SYNC] Got block %d @ %s" % (to - 1, date)))

        lbound = to

    # batch update post cache after catching up to head block
    if not is_initial_sync:
        Follow.flush(trx=True)
        Accounts.flush(trx=True)
        CachedPost.dirty_missing()
        CachedPost.dirty_paidouts(Blocks.last()['date'])
        CachedPost.flush(trx=True)


def listen_steemd(trail_blocks=0, max_gap=50):
    assert trail_blocks >= 0
    assert trail_blocks < 25

    # db state
    db_last = Blocks.last()
    last_block = db_last['num']
    last_hash = db_last['hash']

    # chain state
    steemd = get_adapter()
    head_block = steemd.head_block()
    next_expected = time.time()

    # loop state
    tries = 0
    queue = []

    # TODO: detect missed blocks by looking at block timestamps.
    #       this would be an even more efficient way to track slots.
    while True:
        assert not last_block > head_block

        # fast fwd head block if slots missed
        curr_time = time.time()
        while curr_time >= next_expected:
            head_block += 1
            next_expected += 3

        # if gap too large, abort. if caught up, wait.
        gap = head_block - last_block
        if gap > max_gap:
            print("[LIVE] gap too large: %d -- abort listen mode" % gap)
            return
        elif gap > 0:
            print("[LIVE] %d blocks behind..." % gap)
        elif gap == 0:
            time.sleep(next_expected - curr_time)
            head_block += 1
            next_expected += 3

        # get the target block; if DNE, pause and retry
        block_num = last_block + 1
        block = steemd.get_block(block_num)
        if not block:
            tries += 1
            print("[LIVE] block %d unavailable (try %d). delay 1s. head: %d/%d."
                  % (block_num, tries, head_block, steemd.head_block()))
            #assert tries < 12, "could not fetch block %s" % block_num
            assert tries < 240, "could not fetch block %s" % block_num #74
            time.sleep(1)      # pause for 1s; and,
            next_expected += 1 # delay schedule 1s
            continue
        last_block = block_num
        tries = 0

        # ensure this block links to our last; otherwise, blow up. see #59
        if last_hash != block['previous']:
            if queue:
                print("[FORK] Fork encountered. Emptying queue to retry!")
                return
            raise Exception("Unlinkable block: have %s, got %s -> %s)"
                            % (last_hash, block['previous'], block['block_id']))
        last_hash = block['block_id']

        # buffer until queue full
        queue.append(block)
        if len(queue) <= trail_blocks:
            continue


        # buffer primed; process head of queue
        # ------------------------------------

        block = queue.pop(0)

        start_time = time.perf_counter()
        query("START TRANSACTION")
        num = Blocks.process(block)
        follows = Follow.flush(trx=False)
        accts = Accounts.flush(trx=False, period=8)
        posts = CachedPost.dirty_missing() # no longer needed?
        paids = CachedPost.dirty_paidouts(block['timestamp'])
        edits = CachedPost.flush(trx=False)
        query("COMMIT")
        secs = time.perf_counter() - start_time

        print("[LIVE] Got block %d at %s --% 4d txs,% 3d posts,% 3d edits,"
              "% 3d payouts,% 3d accounts,% 3d follows --% 5dms%s"
              % (num, block['timestamp'], len(block['transactions']),
                 posts, edits - posts - paids, paids, accts, follows,
                 int(secs * 1e3), ' SLOW' if secs > 1 else ''))

        # once per hour, update accounts
        if num % 1200 == 0:
            Accounts.dirty_oldest(10000)
            Accounts.flush(trx=True)
            #Accounts.update_ranks()

        # once a minute, update chain props
        if num % 20 == 0:
            update_chain_state()


def cache_missing_posts():
    gap = CachedPost.dirty_missing()
    print("[INIT] {} missing post cache entries".format(gap))
    while CachedPost.flush(trx=True):
        CachedPost.dirty_missing()

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
    return state['dgpo']['head_block_number']


def run():

    print("[HIVE] Welcome to hivemind")

    # make sure db schema is up to date, perform checks
    DbState.initialize()

    # prefetch id->name memory map
    Accounts.load_ids()

    if DbState.is_initial_sync():
        print("[INIT] *** Initial fast sync ***")
        sync_from_checkpoints()
        sync_from_steemd()

        print("[INIT] *** Initial cache build ***")
        # todo: disable indexes during this process
        cache_missing_posts()
        FeedCache.rebuild()

        DbState.finish_initial_sync()

    else:
        # perform cleanup in case process did not exit cleanly
        cache_missing_posts()

    try:
        while True:
            mem_stats()
            sync_from_steemd()
            DbState.start_listen()
            listen_steemd()
            DbState.stop_listen()

    except KeyboardInterrupt:
        mem_stats()

        # cleanup/flush
        print("Aborted.. cleaning up..")
        # TODO: currently, this only works if a trx is not open, otherwise
        #       an Assertion error is thrown (which is good but not ideal)
        Follow.flush(trx=True)
        Accounts.flush(trx=True)
        CachedPost.flush(trx=True)

        traceback.print_exc()
        print("\nCTRL-C detected, goodbye.")


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = get_adapter().head_block()
    hive_head = Blocks.last()['num']
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    run()
