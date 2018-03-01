"""Main hive sync/listen routine."""

import logging

from hive.conf import Conf
from hive.db.db_state import DbState

from hive.indexer.sync import Sync
from hive.indexer.blocks import Blocks
from hive.indexer.accounts import Accounts
from hive.indexer.cached_post import CachedPost

log = logging.getLogger(__name__)


def run_sync():
    """Initialize state, perform setup/recovery, then sync and listen."""

    # ensure db schema up to date, check app status
    DbState.initialize()

    # prefetch id->name memory map
    Accounts.load_ids()

    if DbState.is_initial_sync():
        # resume initial sync
        Sync.initial()
        DbState.finish_initial_sync()

    else:
        # recover from fork
        Blocks.verify_head()

        # perform cleanup if process did not exit cleanly
        CachedPost.recover_missing_posts()

    while True:
        # sync up to irreversible block
        Sync.from_steemd()

        # take care of payout backlog
        CachedPost.dirty_paidouts(Blocks.head_date())
        CachedPost.flush(trx=True)

        # listen for new blocks
        Sync.listen()


if __name__ == '__main__':
    Conf.init_argparse()
    run_sync()
