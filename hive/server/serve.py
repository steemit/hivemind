# -*- coding: utf-8 -*-
import os
import logging

from datetime import datetime
from sqlalchemy.engine.url import make_url
from aiohttp import web
from aiopg.sa import create_engine
from jsonrpcserver import config
from jsonrpcserver.async_methods import AsyncMethods

from hive.conf import Conf

from hive.server import condenser_api
from hive.server import hive_api

Conf.read()
log_level = Conf.log_level()

config.debug = (log_level == logging.DEBUG)
logging.basicConfig(level=log_level)
logger = logging.getLogger(__name__)
logging.getLogger('jsonrpcserver.dispatcher.response').setLevel(log_level)


hive_methods = (
    hive_api.db_head_state,
    hive_api.get_followers,
    hive_api.get_following,
    hive_api.get_follow_count,
    hive_api.get_user_feed,
    hive_api.get_blog_feed,
    hive_api.get_discussions_by_sort_and_tag,
    hive_api.get_related_posts,
    hive_api.payouts_total,
    hive_api.payouts_last_24h
)

condenser_methods = (
    condenser_api.call,
    condenser_api.get_followers,
    condenser_api.get_following,
    condenser_api.get_follow_count,
    condenser_api.get_discussions_by_trending,
    condenser_api.get_discussions_by_hot,
    condenser_api.get_discussions_by_promoted,
    condenser_api.get_discussions_by_created,
    condenser_api.get_discussions_by_blog,
    condenser_api.get_discussions_by_feed,
    condenser_api.get_discussions_by_comments,
    condenser_api.get_replies_by_last_update,
    condenser_api.get_content,
    condenser_api.get_content_replies,
    condenser_api.get_state
)

# Register hive_api methods and (appbase) condenser_api methods
methods = AsyncMethods()
for m in hive_methods:
    methods.add(m)
    methods.add(m, 'hive_api.' + m.__name__)  # TODO: temp, for testing jussi-style path without jussi
for m in condenser_methods:
    # note: unclear if appbase expects condenser_api.call or call.condenser_api
    methods.add(m, 'condenser_api.' + m.__name__)
    methods.add(m, 'hive_api.condenser_api.' + m.__name__) # TODO: temp, for testing jussi-style path without jussi

# Register non-appbase condenser_api endpoint (remove after appbase in prod)
non_appbase_methods = AsyncMethods()
non_appbase_methods.add(condenser_api.call, 'condenser_api.non_appb.call')
non_appbase_methods.add(condenser_api.call, 'hive_api.condenser_api.non_appb.call') # TODO: temp, for testing jussi-style path without jussi
for m in condenser_methods:
    non_appbase_methods.add(m)

app = web.Application()
app['config'] = dict()

app['config']['hive.MAX_BLOCK_NUM_DIFF'] = 10
app['config']['hive.MAX_DB_ROW_RESULTS'] = 100000
app['config']['hive.DB_QUERY_LIMIT'] = app['config']['hive.MAX_DB_ROW_RESULTS'] + 1
app['config']['hive.logger'] = logger

async def init_db(app):
    args = app['config']['args']
    db = make_url(args.database_url)
    engine = await create_engine(user=db.username,
                                 database=db.database,
                                 password=db.password,
                                 host=db.host,
                                 port=db.port,
                                 **db.query)
    app['db'] = engine

async def close_db(app):
    app['db'].close()
    await app['db'].wait_closed()


# Non JSON-RPC routes
# -------------------
async def health(request):
    state = await hive_api.db_head_state()
    if state['db_head_age'] > app['config']['hive.MAX_BLOCK_NUM_DIFF'] * 3:
        return web.json_response(data=dict(result='head block age (%s) > max allowable (%s); head block num: %s' % (
            state['db_head_age'],
            app['config']['hive.MAX_BLOCK_NUM_DIFF'] * 3,
            state['db_head_block'])), status=500)

    return web.json_response(data=dict(
        status='OK',
        source_commit=os.environ.get('SOURCE_COMMIT'),
        schema_hash=os.environ.get('SCHEMA_HASH'),
        docker_tag=os.environ.get('DOCKER_TAG'),
        state=state,
        timestamp=datetime.utcnow().isoformat()))

async def jsonrpc_handler(request):
    request = await request.text()
    response = await methods.dispatch(request)
    return web.json_response(response, status=200, headers={'Access-Control-Allow-Origin': '*'})

async def non_appbase_handler(request):
    request = await request.text()
    response = await non_appbase_methods.dispatch(request)
    return web.json_response(response, status=200, headers={'Access-Control-Allow-Origin': '*'})


app.on_startup.append(init_db)
app.on_cleanup.append(close_db)
app.router.add_get('/health', health)
app.router.add_post('/', jsonrpc_handler)
app.router.add_post('/legacy', non_appbase_handler)


if __name__ == '__main__':
    app['config']['args'] = Conf.args()
    web.run_app(app, port=app['config']['args'].port)
