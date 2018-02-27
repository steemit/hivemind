#!/usr/local/bin/python3

from hive.conf import Conf
from hive.db.db_state import DbState
from hive.indexer.core import run_sync
from hive.server.serve import run_server

def run():
    """Main CLI service router"""

    Conf.init_argparse()
    mode = Conf.run_mode()

    if mode == 'server':
        run_server()

    elif mode == 'sync':
        run_sync()

    elif mode == 'status':
        print(DbState.status())

    else:
        raise Exception("unknown run mode %s" % mode)

if __name__ == '__main__':
    run()
