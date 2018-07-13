#!/usr/local/bin/python3

"""CLI service router"""

import logging

from hive.conf import Conf
from hive.db.db_state import DbState
from hive.indexer.sync import Sync
from hive.server.serve import run_server

logging.basicConfig()

def run():
    """Run the proper routine as indicated by hive --mode argument."""

    Conf.init_argparse()
    mode = Conf.run_mode()

    if mode == 'server':
        run_server()

    elif mode == 'sync':
        Sync.run()

    elif mode == 'status':
        print(DbState.status())

    else:
        raise Exception("unknown run mode %s" % mode)

if __name__ == '__main__':
    run()
