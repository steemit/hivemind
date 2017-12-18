# -*- coding: utf-8 -*-
import os
import logging

from datetime import datetime
from sqlalchemy.engine.url import make_url
from aiohttp import web
from aiopg.sa import create_engine
from jsonrpcserver import config
from jsonrpcserver.async_methods import AsyncMethods

from hive.server import legacy_api as condenser_api
from hive.server import hive_api

str_log_level = os.environ.get('LOG_LEVEL') or 'DEBUG'
log_level = getattr(logging, str_log_level.upper(), None)
if not isinstance(log_level, int):
    raise ValueError('Invalid log level: %s' % str_log_level)

config.debug = (log_level == logging.DEBUG)
logging.basicConfig(level=log_level)
logger = logging.getLogger(__name__)


jrpc_methods = (
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

jrpc_condenser = (
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

methods = AsyncMethods()
legacy_methods = AsyncMethods()
legacy_methods.add(condenser_api.call, 'call')
methods.add(condenser_api.call, 'call')
for m in jrpc_methods:
    methods.add(m)
for m in jrpc_condenser:
    methods.add(m, 'condenser_api.' + m.__name__)
    legacy_methods.add(m)


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
        docker_tag=os.environ.get('DOCKER_TAG'),
        state=state,
        timestamp=datetime.utcnow().isoformat()))


async def jsonrpc_handler(request):
    request = await request.text()
    response = await methods.dispatch(request)
    return web.json_response(response, status=200)

async def legacy_handler(request):
    request = await request.text()
    response = await legacy_methods.dispatch(request)
    return web.json_response(response, status=200, headers={'Access-Control-Allow-Origin': '*'})


app.on_startup.append(init_db)
app.on_cleanup.append(close_db)
app.router.add_get('/health', health)
app.router.add_post('/', jsonrpc_handler)
app.router.add_post('/legacy', legacy_handler)


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description="hivemind jsonrpc server")
    parser.add_argument('--database_url', type=str, default='postgresql://root:root_password@127.0.0.1:5432/testdb')
    parser.add_argument('--port', type=int, default=8080)
    args = parser.parse_args()
    app['config']['args'] = args
    web.run_app(app, port=args.port)
