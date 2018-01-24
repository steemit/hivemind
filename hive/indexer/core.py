import json
import logging
import glob
import time
import os
import traceback

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
        print(timer.batch_status("[SYNC] Got block {}".format(to-1)))

        lbound = to

    # batch update post cache after catching up to head block
    if not is_initial_sync:
        Follow.flush(trx=True)
        Accounts.flush(trx=True)
        CachedPost.dirty_missing()
        CachedPost.dirty_paidouts(Blocks.last()['date'])
        CachedPost.flush(trx=True)


def listen_steemd(trail_blocks=2):
    assert trail_blocks >= 0
    assert trail_blocks < 25
    steemd = get_adapter()
    last_block = Blocks.last()
    curr_block = last_block['num'] + 1
    last_hash = last_block['hash']

    head_block = steemd.head_block()
    next_expected = time.time()
    tries = 0

    while True:

        # measure diff to our ideal following distance
        gap = (head_block - trail_blocks) - curr_block

        # if too far behind, abort.
        if gap > 50:
            print("[LIVE] gap too large: %d -- abort listen mode" % gap)
            return

        # if behind, process asap.
        elif gap >= 0:
            print("[LIVE] %d blocks behind" % (gap + 1))

        # we're 1 block ahead. perfect; sleep until ETA.
        elif gap == -1:
            tta = next_expected - time.time()
            if tta > 0:
                time.sleep(tta)
                head_block += 1
                next_expected += 3
                gap = head_block - curr_block - trail_blocks
            else:
                print("[LIVE] no-pause should not happen here")

        # we're 2 or more blocks ahead of target. it's possible e.g. we were
        # trying to trail by 2 blocks, but we've managed to catch up to the
        # head block. todo: prevent by keeping a fifo block queue w/ a min len.
        else:
            print("[LIVE] too far ahead. current: %d, target: %d. Pausing."
                  % (curr_block - head_block + 1, trail_blocks))
            time.sleep(3)
            next_expected += 3
            head_block += 1
            continue

        # get the target block; if DNE, pause and retry
        block = steemd.get_block(curr_block)
        if not block:
            tries += 1
            print("[LIVE] block %d not available (try %d). delay 1s. gap is %d."
                  % (curr_block, tries, gap))
            if tries > 12:
                # todo: detect if the node we're querying is behind
                raise Exception("could not fetch block %d" % curr_block)
            time.sleep(1)      # pause 1s; and,
            next_expected += 1 # delay schedule 1s
            continue
        tries = 0

        # ensure the block we received links to our last
        if last_hash != block['previous']:
            # this condition is very rare unless trail_blocks is 0 and fork is
            # encountered; to handle gracefully, implement a pop_block method
            raise Exception("Unlinkable block: have {}, got {} -> {})".format(
                last_hash, block['previous'], block['block_id']))
        last_hash = block['block_id']

        start_time = time.perf_counter()
        query("START TRANSACTION")
        Blocks.process(block)
        follows = Follow.flush(trx=False)
        accts = Accounts.flush(trx=False)
        posts = CachedPost.dirty_missing()
        paids = CachedPost.dirty_paidouts(block['timestamp'])
        edits = CachedPost.flush(trx=False)
        query("COMMIT")
        secs = time.perf_counter() - start_time

        print("[LIVE] Got block %d at %s -- % 3d txs,% 3d posts,% 3d edits,"
              "% 3d payouts,% 3d accounts,% 3d follows --% 5dms%s"
              % (curr_block, block['timestamp'], len(block['transactions']),
                 posts, edits - posts - paids, paids, accts, follows,
                 int(secs * 1e3), ' SLOW' if secs > 1 else ''))

        # approx once per hour, update accounts
        if curr_block % 1200 == 0:
            print("[HIVE] Performing account maintenance...")
            Accounts.dirty_oldest(10000)
            Accounts.flush(trx=True)
            #Accounts.update_ranks()

        # fast fwd head block if we missed any slots during processing
        while time.time() > next_expected:
            head_block += 1
            next_expected += 3
            print("[LIVE] FF 1 block.. head: %d" % head_block)

        # once a minute, update chain props (plus, correct any drift)
        if curr_block % 20 == 0:
            old_head = head_block
            head_block = update_chain_state()
            if old_head != head_block:
                print("[LIVE] head correction was %d (%d -> %d)"
                      % (head_block - old_head, old_head, head_block))

        curr_block = curr_block + 1

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
            sync_from_steemd()
            listen_steemd()
    except KeyboardInterrupt:
        traceback.print_exc()
        # TODO: cleanup/flush
        # e.g. CachedPost.flush_edits()
        print("\nCTRL-C detected, goodbye.")


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = get_adapter().head_block()
    hive_head = Blocks.last()['num']
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    run()
