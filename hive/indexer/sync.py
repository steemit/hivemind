import json
import logging
import glob
import time
import os
import traceback

from funcy.seqs import drop
from toolz import partition_all

from hive.conf import Conf

from hive.db.methods import query

from hive.utils.timer import Timer
from hive.indexer.steem_client import SteemClient

from hive.indexer.blocks import Blocks
from hive.indexer.accounts import Accounts
from hive.indexer.cached_post import CachedPost
from hive.indexer.follow import Follow

log = logging.getLogger(__name__)

class Sync:
    @classmethod
    def from_checkpoints(cls, chunk_size=1000):
        last_block = Blocks.head_num()

        tuplize = lambda path: [int(path.split('/')[-1].split('.')[0]), path]
        basedir = os.path.dirname(os.path.realpath(__file__ + "/../.."))
        files = glob.glob(basedir + "/checkpoints/*.json.lst")
        tuples = sorted(map(tuplize, files), key=lambda f: f[0])

        last_read = 0
        for (num, path) in tuples:
            if last_block < num:
                print("[SYNC] Load {} -- last block: {}".format(path, last_block))
                with open(path) as f:
                    # each line in file represents one block
                    # we can skip the blocks we already have
                    skip_lines = last_block - last_read
                    remaining = drop(skip_lines, f)
                    for lines in partition_all(chunk_size, remaining):
                        Blocks.process_multi(map(json.loads, lines), True)
                last_block = num
            last_read = num

    @classmethod
    def from_steemd(cls, is_initial_sync=False, chunk_size=100, max_blocks=1000):
        steemd = SteemClient.instance()

        lbound = Blocks.head_num() + 1
        ubound = min(steemd.last_irreversible(), lbound + max_blocks - 1)
        if ubound <= lbound:
            return False

        _abort = False
        try:
            print("[SYNC] start block %d, +%d to sync" % (lbound, ubound-lbound+1))
            timer = Timer(ubound - lbound, entity='block', laps=['rps', 'wps'])
            while lbound < ubound:
                to = min(lbound + chunk_size, ubound)
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

        return True


    @classmethod
    def listen(cls):
        trail_blocks = Conf.get('trail_blocks')
        assert trail_blocks >= 0
        assert trail_blocks < 25

        steemd = SteemClient.instance()
        hive_head = Blocks.head_num()
        for block in steemd.stream_blocks(hive_head + 1, trail_blocks, max_gap=4e7):
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
                cls._update_chain_state(steemd)


    # refetch dynamic_global_properties, feed price, etc
    @classmethod
    def _update_chain_state(cls, adapter):
        state = adapter.gdgp_extended()
        query("""UPDATE hive_state SET block_num = :block_num,
                 steem_per_mvest = :spm, usd_per_steem = :ups,
                 sbd_per_steem = :sps, dgpo = :dgpo""",
              block_num=state['dgpo']['head_block_number'],
              spm=state['steem_per_mvest'],
              ups=state['usd_per_steem'],
              sps=state['sbd_per_steem'],
              dgpo=json.dumps(state['dgpo']))
        return state['dgpo']['head_block_number']
