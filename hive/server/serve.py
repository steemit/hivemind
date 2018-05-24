# -*- coding: utf-8 -*-
"""Hive JSON-RPC API server."""
import os
import logging

from datetime import datetime
#from sqlalchemy.engine.url import make_url
from sqlalchemy.exc import OperationalError
from aiohttp import web
#from aiopg.sa import create_engine
from jsonrpcserver import config
from jsonrpcserver.async_methods import AsyncMethods

from hive.conf import Conf

from hive.server import condenser_api
from hive.server import hive_api


def build_methods():
    """Build a map of all supported hive_api/condenser_api calls."""
    methods = AsyncMethods()

    hive_methods = (
        hive_api.db_head_state,
        # --- disabled until #92
        #hive_api.get_followers,
        #hive_api.get_following,
        #hive_api.get_follow_count,
        #hive_api.get_user_feed,
        #hive_api.get_blog_feed,
        #hive_api.get_discussions_by_sort_and_tag,
        #hive_api.get_related_posts,
        # ---
        hive_api.payouts_total,
        hive_api.payouts_last_24h,
    )
    for method in hive_methods:
        methods.add(method)
        #methods.add(method, 'hive.' + method.__name__) #test jussi-ns w/o jussi

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
        condenser_api.get_state,
    )
    for method in condenser_methods:
        # todo: unclear if appbase uses condenser_api.call vs call.condenser_api
        methods.add(method, 'condenser_api.' + method.__name__)

        # todo: temporary non-appbase endpoint (remove once appbase is in prod)
        methods.add(method, method.__name__)

    return methods


def run_server():
    """Configure and launch the API server."""

    log_level = Conf.log_level()
    config.debug = (log_level == logging.DEBUG)
    logging.basicConfig(level=log_level)
    logger = logging.getLogger(__name__)
    logging.getLogger('jsonrpcserver.dispatcher.response').setLevel(log_level)

    methods = build_methods()

    app = web.Application()
    app['config'] = dict()
    app['config']['args'] = Conf.args()
    app['config']['hive.MAX_DB_ROW_RESULTS'] = 100000
    app['config']['hive.DB_QUERY_LIMIT'] = app['config']['hive.MAX_DB_ROW_RESULTS'] + 1
    app['config']['hive.logger'] = logger

    #async def init_db(app):
    #    args = app['config']['args']
    #    db = make_url(args.database_url)
    #    engine = await create_engine(user=db.username,
    #                                 database=db.database,
    #                                 password=db.password,
    #                                 host=db.host,
    #                                 port=db.port,
    #                                 **db.query)
    #    app['db'] = engine
    #
    #async def close_db(app):
    #    app['db'].close()
    #    await app['db'].wait_closed()
    #
    #app.on_startup.append(init_db)
    #app.on_cleanup.append(close_db)

    async def _head_state():
        try:
            return await hive_api.db_head_state()
        except OperationalError as e:
            if 'could not connect to server: Connection refused' in str(e):
                logging.warning("could not connect to db for head state")
                return None
            raise e

    async def head_age(request):
        """Get hive head block age in seconds. 500 if greater than 15s."""
        #pylint: disable=unused-argument
        healthy_age = 15 # hive is synced if head block within 15s
        state = await _head_state()
        curr_age = state['db_head_age'] if state else 31e6
        status = 500 if curr_age > healthy_age else 200
        return web.Response(status=status, text=str(curr_age))

    async def health(request):
        """Get hive health data. 500 if behind by more than a few secs."""
        #pylint: disable=unused-argument
        is_syncer = Conf.get('sync_to_s3')
        max_head_age = (Conf.get('trail_blocks') + 1) * 3
        state = await _head_state()

        if not state:
            status = 500
            result = 'db not available'
        elif not is_syncer and state['db_head_age'] > max_head_age:
            status = 500
            result = 'head block age (%s) > max (%s); head block num: %s' % (
                state['db_head_age'], max_head_age, state['db_head_block'])
        else:
            status = 200
            result = 'head block age is %d, head block num is %d' % (
                state['db_head_age'], state['db_head_block'])

        return web.json_response(status=status, data=dict(
            state=state,
            result=result,
            status='OK' if status == 200 else 'WARN',
            sync_service=is_syncer,
            source_commit=os.environ.get('SOURCE_COMMIT'),
            schema_hash=os.environ.get('SCHEMA_HASH'),
            docker_tag=os.environ.get('DOCKER_TAG'),
            timestamp=datetime.utcnow().isoformat()))

    async def jsonrpc_handler(request):
        """Handles all hive jsonrpc API requests."""
        request = await request.text()
        response = await methods.dispatch(request)
        return web.json_response(response, status=200, headers={'Access-Control-Allow-Origin': '*'})

    app.router.add_get('/.well-known/healthcheck.json', health)
    app.router.add_get('/head_age', head_age)
    app.router.add_get('/health', health)
    app.router.add_post('/', jsonrpc_handler)

    web.run_app(app, port=app['config']['args'].http_server_port)


if __name__ == '__main__':
    Conf.init_argparse()
    run_server()
