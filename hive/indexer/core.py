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

from hive.utils.timer import Timer
from hive.utils.normalize import parse_time
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
    last_block = Blocks.head_num()

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

    lbound = Blocks.head_num() + 1
    ubound = steemd.last_irreversible()
    if ubound <= lbound:
        return

    _abort = False
    try:
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
            print(timer.batch_status("[SYNC] Got block %d @ %s" % (to-1, date)))
            lbound = to

    except KeyboardInterrupt:
        traceback.print_exc()
        print("\n\n[SYNC] Aborted.. cleaning up..")
        _abort = True

    if not is_initial_sync:
        # Follows flushing may need to be moved closer to core (i.e. moved
        # into main block transactions). Important to keep in sync since
        # we need to prevent expensive recounts. This will fail if we aborted
        # in the middle of a transaction, meaning data loss. Better than
        # forcing it, however, since in-memory cache will be out of sync
        # with db state.
        Follow.flush(trx=True)

        # This flush is low importance; accounts are swept regularly.
        if not _abort:
            Accounts.flush(trx=True)

        # If this flush fails, all that could potentially be lost here is
        # edits and pre-payout votes. If the post has not been paid out yet,
        # then the worst case is it will be synced upon payout. If the post
        # is already paid out, worst case is to lose an edit.
        CachedPost.flush(trx=True)

    if _abort:
        print("[SYNC] Aborted")
        exit()


def _stream_blocks(last_num, trail_blocks=0, max_gap=40):
    assert trail_blocks >= 0
    assert trail_blocks < 25

    last = Blocks.get(last_num)
    steemd = get_adapter()
    head_num = steemd.head_block()
    next_expected = time.time() + 1.5

    start_head = head_num
    lag_secs = 0
    queue = []
    while True:
        assert not last['num'] > head_num

        # if slots missed, advance head block
        time_now = time.time()
        while time_now >= next_expected + lag_secs:
            head_num += 1
            next_expected += 3

            # check we're not too far behind
            gap = head_num - last['num']
            print("[LIVE] %d blocks behind..." % gap)
            assert gap < max_gap, "gap too large: %d" % gap

        # if caught up, await head advance.
        if head_num == last['num']:
            time.sleep(next_expected + lag_secs - time_now)
            head_num += 1
            next_expected += 3

        # get the target block; if DNE, pause and retry
        block_num = last['num'] + 1
        block = steemd.get_block(block_num)
        if not block:
            lag_secs = (lag_secs + 0.5) % 3 # tune inter-slot timing
            print("[LIVE] block %d failed. delay 1/2s. head: %d/%d."
                  % (block_num, head_num, steemd.head_block()))
            time.sleep(0.5)
            continue
        last['num'] = block_num

        # if block doesn't link, we're forked
        if last['hash'] != block['previous']:
            if queue: # using trail_blocks, fork might not be in db
                print("[FORK] Fork in queue; emptying to retry.")
                return
            raise Exception("[FORK] Fork in db: from %s, %s->%s" % (
                last['hash'], block['previous'], block['block_id']))
        last['hash'] = block['block_id']

        # detect missed blocks, adjust schedule
        block_date = parse_time(block['timestamp'])
        miss_secs = (block_date - last['date']).seconds - 3
        if miss_secs and block_num >= start_head:
            print("[LIVE] %d missed blocks"
                  % (miss_secs / 3))
            next_expected += miss_secs
        last['date'] = block_date

        # buffer block yield
        queue.append(block)
        if len(queue) > trail_blocks:
            yield queue.pop(0)


def listen_steemd(trail_blocks=0, max_gap=40):
    assert trail_blocks >= 0
    assert trail_blocks < 25

    hive_head = Blocks.head_num()
    for block in _stream_blocks(hive_head, trail_blocks, max_gap):
        start_time = time.perf_counter()

        query("START TRANSACTION")
        num = Blocks.process(block)
        follows = Follow.flush(trx=False)
        accts = Accounts.flush(trx=False, period=8)
        CachedPost.dirty_paidouts(block['timestamp'])
        cnt = CachedPost.flush(trx=False)
        query("COMMIT")

        ms = (time.perf_counter() - start_time) * 1000
        print("[LIVE] Got block %d at %s --% 4d txs,% 3d posts,% 3d edits,"
              "% 3d payouts,% 3d votes,% 3d accounts,% 3d follows --% 5dms%s"
              % (num, block['timestamp'], len(block['transactions']),
                 cnt['insert'], cnt['update'], cnt['payout'], cnt['upvote'],
                 accts, follows, int(ms), ' SLOW' if ms > 1000 else ''))

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
    while CachedPost.flush(trx=True)['insert']:
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
        # recover from fork
        Blocks.verify_head()

        # perform cleanup in case process did not exit cleanly
        cache_missing_posts()

    while True:
        # sync up to irreversible block
        sync_from_steemd()

        # take care of payout backlog
        CachedPost.dirty_paidouts(Blocks.head_date())
        CachedPost.flush(trx=True)

        # start listening
        listen_steemd()


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = get_adapter().head_block()
    hive_head = Blocks.head_num()
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    run()
