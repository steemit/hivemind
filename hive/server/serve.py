# -*- coding: utf-8 -*-
import json
import logging
import os
from datetime import datetime

import bottle
import hive.server.methods as rpcmethods
from bottle import abort
from bottle_errorsrest import ErrorsRestPlugin
from bottle_sqlalchemy import Plugin
from hive.db.schema import metadata as hive_metadata
from hive.indexer.core import db_last_block, head_state
from hive.sbds.jsonrpc import register_endpoint
from hive.sbds.sbds_json import ToStringJSONEncoder
from sqlalchemy import create_engine
from steem.steemd import Steemd



from hive.db.methods import (
    get_followers,
    get_following,
    following_count,
    follower_count,
    get_user_feed,
    get_discussions_by_trending
)


logger = logging.getLogger(__name__)

app = bottle.Bottle()
app.config['hive.MAX_BLOCK_NUM_DIFF'] = 10
app.config['hive.MAX_DB_ROW_RESULTS'] = 100000
app.config['hive.DB_QUERY_LIMIT'] = app.config['hive.MAX_DB_ROW_RESULTS'] + 1
app.config['hive.logger'] = logger

app.install(
    bottle.JSONPlugin(json_dumps=lambda s: json.dumps(s, cls=ToStringJSONEncoder)))
app.install(ErrorsRestPlugin())


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

@app.get('/feed/<user>/<skip>')
def callback(user, skip):
    return dict(user = user, posts = get_user_feed(user, int(skip), 10))

@app.get('/followers/<user>')
def callback(user):
    return dict(user = user, followers = get_followers(user))

@app.get('/followers/<user>/<skip>/<limit>')
def callback(user, skip, limit):
    return dict(user = user, followers = get_followers(user, skip, limit))

@app.get('/head_state')
def callback():
    return head_state()



# JSON-RPC route
# --------------
jsonrpc = register_endpoint(path='/', app=app, namespace='hive')

json_rpc_methods = {
    'head_state': head_state,
    'get_followers': rpcmethods.get_followers,
    'get_following': rpcmethods.get_following,
}
for method_name, fn_call in json_rpc_methods.items():
    jsonrpc.register_method(method=fn_call, method_name=method_name)

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
