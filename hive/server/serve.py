# -*- coding: utf-8 -*-
import json
import logging
import os
from datetime import datetime

import bottle
from bottle import abort
from bottle_errorsrest import ErrorsRestPlugin
from bottle_sqlalchemy import Plugin
from hive.db.schema import metadata as hive_metadata
from hive.indexer.core import db_last_block, head_state
from sqlalchemy import create_engine
from steem.steemd import Steemd

from sbds.sbds_json import ToStringJSONEncoder
from sbds.server.jsonrpc import register_endpoint

logger = logging.getLogger(__name__)

app = bottle.Bottle()
app.config['hive.DATABASE_URL'] = os.environ.get('DATABASE_URL', 'missing ENV DATABASE_URL')
app.config['hive.MAX_BLOCK_NUM_DIFF'] = 10
app.config['hive.MAX_DB_ROW_RESULTS'] = 100000
app.config['hive.DB_QUERY_LIMIT'] = app.config['hive.MAX_DB_ROW_RESULTS'] + 1
app.config['sbds.logger'] = logger


def get_db_plugin(database_url):
    sa_engine = create_engine(database_url)

    # pylint: disable=undefined-variable
    return Plugin(
        # SQLAlchemy engine created with create_engine function.
        sa_engine,
        # SQLAlchemy metadata, required only if create=True.
        hive_metadata,
        # Keyword used to inject session database in a route (default 'db').
        keyword='db',
        # If it is true, execute `metadata.create_all(engine)` when plugin is applied (default False).
        create=False,
        # If it is true, plugin commit changes after route is executed (default True).
        commit=False,
        # If True and keyword is not defined, plugin uses **kwargs argument to inject session database (default False).
        use_kwargs=False,
    )


app.install(
    bottle.JSONPlugin(json_dumps=lambda s: json.dumps(s, cls=ToStringJSONEncoder)))
app.install(ErrorsRestPlugin())
db_plugin = get_db_plugin(app.config['hive.DATABASE_URL'])
app.install(db_plugin)


# Non JSON-RPC routes
# -------------------
@app.get('/health')
def health():
    steemd = Steemd()
    last_db_block = db_last_block()
    last_irreversible_block = steemd.last_irreversible_block_num
    diff = last_irreversible_block - last_db_block
    if diff > app.config['hive.MAX_BLOCK_NUM_DIFF']:
        abort(
            500,
            'last irreversible block (%s) - highest db block (%s) = %s, > max allowable difference (%s)'
            % (last_irreversible_block, last_db_block, diff,
               app.config['hive.MAX_BLOCK_NUM_DIFF']))
    else:
        return dict(
            last_db_block=last_db_block,
            last_irreversible_block=last_irreversible_block,
            diff=diff,
            timestamp=datetime.utcnow().isoformat())


# JSON-RPC route
# --------------
jsonrpc = register_endpoint(path='/', app=app, namespace='hive')

json_rpc_methods = {
    'head_state': head_state,
}
for method_name, fn_call in json_rpc_methods.items():
    jsonrpc.register_method(method=fn_call, method_name=method_name)

# TODO: add to documentation
#  In [9]: from jsonrpcclient.http_client import HTTPClient
#
# In [10]: HTTPClient('http://localhost:1234').request('hive.status')
# --> {"jsonrpc": "2.0", "method": "hive.status", "id": 8}
# <-- {"result": {"diff": 6396850, "hive": 5042966, "steemd": 11439816}, "id": 8, "jsonrpc": "2.0"} (200 OK)
# Out[10]: {'diff': 6396850, 'hive': 5042966, 'steemd': 11439816}


# WSGI application
# ----------------
application = app


# dev/debug server
# ----------------
def _dev_server(port=8081, debug=True):
    # pylint: disable=bare-except
    try:
        app.run(port=port, debug=debug)
    except:
        logger.exception('HTTP Server Exception')
    finally:
        app.close()


# For pdb debug only
if __name__ == '__main__':
    _dev_server()
