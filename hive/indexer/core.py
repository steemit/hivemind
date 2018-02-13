import logging

from hive.conf import Conf
from hive.db.db_state import DbState
from hive.indexer.steem_client import SteemClient

from hive.indexer.sync import Sync
from hive.indexer.blocks import Blocks
from hive.indexer.accounts import Accounts
from hive.indexer.cached_post import CachedPost
from hive.indexer.feed_cache import FeedCache

log = logging.getLogger(__name__)

def run():

    print("[HIVE] Welcome to hivemind")

    # make sure db schema is up to date, perform checks
    DbState.initialize()

    # prefetch id->name memory map
    Accounts.load_ids()

    if DbState.is_initial_sync():
        print("[INIT] *** Initial fast sync ***")
        Sync.from_checkpoints()
        Sync.from_steemd(is_initial_sync=True)

        print("[INIT] *** Initial cache build ***")
        # todo: disable indexes during this process
        CachedPost.recover_missing_posts()
        FeedCache.rebuild()
        DbState.finish_initial_sync()

    else:
        # recover from fork
        Blocks.verify_head()

        # perform cleanup in case process did not exit cleanly
        CachedPost.recover_missing_posts()

    while True:
        # sync up to irreversible block
        Sync.from_steemd()

        # take care of payout backlog
        CachedPost.dirty_paidouts(Blocks.head_date())
        CachedPost.flush(trx=True)

        # start listening
        Sync.listen()


def head_state(*args):
    _ = args  # JSONRPC injects 4 arguments here
    steemd_head = SteemClient.instance().head_block()
    hive_head = Blocks.head_num()
    diff = steemd_head - hive_head
    return dict(steemd=steemd_head, hive=hive_head, diff=diff)


if __name__ == '__main__':
    Conf.read()
    run()
