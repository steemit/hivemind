"""Hive sync manager."""

import logging
import glob
from time import perf_counter as perf
import os
import ujson as json

from funcy.seqs import drop
from toolz import partition_all

from hive.db.db_state import DbState

from hive.utils.timer import Timer
from hive.steem.block.stream import MicroForkException

from hive.indexer.blocks import Blocks
from hive.indexer.accounts import Accounts
from hive.indexer.cached_post import CachedPost
from hive.indexer.feed_cache import FeedCache
from hive.indexer.follow import Follow

log = logging.getLogger(__name__)

class Sync:
    """Manages the sync/index process.

    Responsible for initial sync, fast sync, and listen (block-follow).
    """

    def __init__(self, conf):
        self._conf = conf
        self._db = conf.db()
        self._steem = conf.steem()

    def run(self):
        """Initialize state; setup/recovery checks; sync and runloop."""

        # ensure db schema up to date, check app status
        DbState.initialize()

        # prefetch id->name and id->rank memory maps
        Accounts.load_ids()
        Accounts.fetch_ranks()

        if DbState.is_initial_sync():
            # resume initial sync
            self.initial()
            DbState.finish_initial_sync()

        else:
            # recover from fork
            Blocks.verify_head(self._steem)

            # perform cleanup if process did not exit cleanly
            CachedPost.recover_missing_posts(self._steem)

        self._update_chain_state()

        if self._conf.get('test_max_block'):
            # debug mode: partial sync
            return self.from_steemd()
        if self._conf.get('test_disable_sync'):
            # debug mode: no sync, just stream
            return self.listen()

        while True:
            # sync up to irreversible block
            self.from_steemd()

            # take care of payout backlog
            CachedPost.dirty_paidouts(Blocks.head_date())
            CachedPost.flush(self._steem, trx=True)

            try:
                # listen for new blocks
                self.listen()
            except MicroForkException as e:
                # attempt to recover by restarting stream
                log.error("micro fork: %s", repr(e))

    def initial(self):
        """Initial sync routine."""
        assert DbState.is_initial_sync(), "already synced"

        log.info("[INIT] *** Initial fast sync ***")
        self.from_checkpoints()
        self.from_steemd(is_initial_sync=True)

        log.info("[INIT] *** Initial cache build ***")
        CachedPost.recover_missing_posts(self._steem)
        FeedCache.rebuild()
        Follow.force_recount()

    def from_checkpoints(self, chunk_size=1000):
        """Initial sync strategy: read from blocks on disk.

        This methods scans for files matching ./checkpoints/*.json.lst
        and uses them for hive's initial sync. Each line must contain
        exactly one block in JSON format.
        """
        # pylint: disable=no-self-use
        last_block = Blocks.head_num()

        tuplize = lambda path: [int(path.split('/')[-1].split('.')[0]), path]
        basedir = os.path.dirname(os.path.realpath(__file__ + "/../.."))
        files = glob.glob(basedir + "/checkpoints/*.json.lst")
        tuples = sorted(map(tuplize, files), key=lambda f: f[0])

        last_read = 0
        for (num, path) in tuples:
            if last_block < num:
                log.info("[SYNC] Load %s. Last block: %d", path, last_block)
                with open(path) as f:
                    # each line in file represents one block
                    # we can skip the blocks we already have
                    skip_lines = last_block - last_read
                    remaining = drop(skip_lines, f)
                    for lines in partition_all(chunk_size, remaining):
                        Blocks.process_multi(map(json.loads, lines), True)
                last_block = num
            last_read = num

    def from_steemd(self, is_initial_sync=False, chunk_size=1000):
        """Fast sync strategy: read/process blocks in batches."""
        steemd = self._steem
        lbound = Blocks.head_num() + 1
        ubound = self._conf.get('test_max_block') or steemd.last_irreversible()

        count = ubound - lbound
        if count < 1:
            return

        log.info("[SYNC] start block %d, +%d to sync", lbound, count)
        timer = Timer(count, entity='block', laps=['rps', 'wps'])
        while lbound < ubound:
            timer.batch_start()

            # fetch blocks
            to = min(lbound + chunk_size, ubound)
            blocks = steemd.get_blocks_range(lbound, to)
            lbound = to
            timer.batch_lap()

            # process blocks
            Blocks.process_multi(blocks, is_initial_sync)
            timer.batch_finish(len(blocks))

            _prefix = ("[SYNC] Got block %d @ %s" % (
                to - 1, blocks[-1]['timestamp']))
            log.info(timer.batch_status(_prefix))

        if not is_initial_sync:
            # This flush is low importance; accounts are swept regularly.
            Accounts.flush(steemd, trx=True)

            # If this flush fails, all that could potentially be lost here is
            # edits and pre-payout votes. If the post has not been paid out yet,
            # then the worst case is it will be synced upon payout. If the post
            # is already paid out, worst case is to lose an edit.
            CachedPost.flush(steemd, trx=True)

    def listen(self):
        """Live (block following) mode."""
        trail_blocks = self._conf.get('trail_blocks')
        assert trail_blocks >= 0
        assert trail_blocks <= 100

        # debug: no max gap if disable_sync in effect
        max_gap = None if self._conf.get('test_disable_sync') else 100

        steemd = self._steem
        hive_head = Blocks.head_num()

        for block in steemd.stream_blocks(hive_head + 1, trail_blocks, max_gap):
            start_time = perf()

            self._db.query("START TRANSACTION")
            num = Blocks.process(block)
            follows = Follow.flush(trx=False)
            accts = Accounts.flush(steemd, trx=False, spread=8)
            CachedPost.dirty_paidouts(block['timestamp'])
            cnt = CachedPost.flush(steemd, trx=False)
            self._db.query("COMMIT")

            ms = (perf() - start_time) * 1000
            log.info("[LIVE] Got block %d at %s --% 4d txs,% 3d posts,% 3d edits,"
                     "% 3d payouts,% 3d votes,% 3d counts,% 3d accts,% 3d follows"
                     " --% 5dms%s", num, block['timestamp'], len(block['transactions']),
                     cnt['insert'], cnt['update'], cnt['payout'], cnt['upvote'],
                     cnt['recount'], accts, follows, ms, ' SLOW' if ms > 1000 else '')

            if num % 1200 == 0: #1hr
                log.info("[LIVE] update account ranks mmap")
                Accounts.fetch_ranks()
            if num % 100 == 0: #5min
                log.info("[LIVE] flag 500 oldest accounts for update")
                Accounts.dirty_oldest(500)
            if num % 20 == 0: #1min
                self._update_chain_state()

    # refetch dynamic_global_properties, feed price, etc
    def _update_chain_state(self):
        """Update basic state props (head block, feed price) in db."""
        state = self._steem.gdgp_extended()
        self._db.query("""UPDATE hive_state SET block_num = :block_num,
                       steem_per_mvest = :spm, usd_per_steem = :ups,
                       sbd_per_steem = :sps, dgpo = :dgpo""",
                       block_num=state['dgpo']['head_block_number'],
                       spm=state['steem_per_mvest'],
                       ups=state['usd_per_steem'],
                       sps=state['sbd_per_steem'],
                       dgpo=json.dumps(state['dgpo']))
        return state['dgpo']['head_block_number']
