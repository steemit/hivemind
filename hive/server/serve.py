# -*- coding: utf-8 -*-
import os
import logging

from datetime import datetime
from sqlalchemy.engine.url import make_url
from aiohttp import web
from aiopg.sa import create_engine
from jsonrpcserver import config
from jsonrpcserver.async_methods import AsyncMethods

from hive.server import legacy_api as legacy

from hive.db.methods import (
    db_head_state,
    get_followers,
    get_following,
    get_follow_count,
    get_user_feed,
    get_blog_feed,
    get_discussions_by_sort_and_tag,
    get_related_posts,
    payouts_total,
    payouts_last_24h
)

config.debug = True

logging.basicConfig(level=logging.DEBUG)
logger = logging.getLogger(__name__)

jrpc_methods = (
    db_head_state,
    get_followers,
    get_following,
    get_follow_count,
    get_user_feed,
    get_blog_feed,
    get_discussions_by_sort_and_tag,
    get_related_posts,
    payouts_total,
    payouts_last_24h
)

methods = AsyncMethods()
for m in jrpc_methods:
    methods.add(m)

legacy_methods = AsyncMethods()
legacy_methods.add(legacy.get_followers)
legacy_methods.add(legacy.get_following)
legacy_methods.add(legacy.get_follow_count)

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
    state = await db_head_state()
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
    return web.json_response(response, status=200)


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
