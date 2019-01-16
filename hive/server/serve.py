# -*- coding: utf-8 -*-
"""Hive JSON-RPC API server."""
import os
import sys
import logging

from datetime import datetime
#from sqlalchemy.engine.url import make_url
from sqlalchemy.exc import OperationalError
from aiohttp import web
#from aiopg.sa import create_engine
from jsonrpcserver import config
from jsonrpcserver.async_methods import AsyncMethods

from hive.server.condenser_api import methods as condenser_api
from hive.server.condenser_api.tags import get_trending_tags as condenser_api_get_trending_tags
from hive.server.condenser_api.get_state import get_state as condenser_api_get_state
from hive.server.condenser_api.call import call as condenser_api_call
from hive.server import hive_api


def build_methods():
    """Register all supported hive_api/condenser_api.calls."""
    # pylint: disable=expression-not-assigned
    methods = AsyncMethods()

    [methods.add(method, 'hive.' + method.__name__) for method in (
        hive_api.db_head_state,
        #hive_api.payouts_total,
        #hive_api.payouts_last_24h,
        #hive_api.get_accounts,
        #hive_api.get_accounts_ac,
        # --- disabled until #92
        #hive_api.get_followers,
        #hive_api.get_following,
        #hive_api.get_follow_count,
        #hive_api.get_user_feed,
        #hive_api.get_blog_feed,
        #hive_api.get_discussions_by_sort_and_tag,
        #hive_api.get_related_posts,
    )]

    [methods.add(method, 'condenser_api.' + method.__name__) for method in (
        condenser_api.get_followers,
        condenser_api.get_following,
        condenser_api.get_follow_count,
        condenser_api.get_content,
        condenser_api.get_content_replies,
        condenser_api_get_state,
        condenser_api_get_trending_tags,
        condenser_api.get_discussions_by_trending,
        condenser_api.get_discussions_by_hot,
        condenser_api.get_discussions_by_promoted,
        condenser_api.get_discussions_by_created,
        condenser_api.get_discussions_by_blog,
        condenser_api.get_discussions_by_feed,
        condenser_api.get_discussions_by_comments,
        condenser_api.get_replies_by_last_update,

        condenser_api.get_discussions_by_author_before_date,
        condenser_api.get_post_discussions_by_payout,
        condenser_api.get_comment_discussions_by_payout,
        condenser_api.get_blog,
        condenser_api.get_blog_entries,
        condenser_api.get_account_reputations,
        condenser_api.get_reblogged_by,
    )]

    # dummy methods -- serve informational error
    methods.add(condenser_api.get_account_votes, 'condenser_api.get_account_votes')
    methods.add(condenser_api.get_account_votes, 'tags_api.get_account_votes')

    # follow_api aliases
    methods.add(condenser_api.get_followers, 'follow_api.get_followers')
    methods.add(condenser_api.get_following, 'follow_api.get_following')
    methods.add(condenser_api.get_follow_count, 'follow_api.get_follow_count')
    methods.add(condenser_api.get_account_reputations, 'follow_api.get_account_reputations')
    methods.add(condenser_api.get_blog, 'follow_api.get_blog')
    methods.add(condenser_api.get_blog_entries, 'follow_api.get_blog_entries')
    methods.add(condenser_api.get_reblogged_by, 'follow_api.get_reblogged_by')

    # tags_api aliases
    methods.add(condenser_api.get_content, 'tags_api.get_discussion')
    methods.add(condenser_api.get_content_replies, 'tags_api.get_content_replies')
    methods.add(condenser_api.get_discussions_by_trending, 'tags_api.get_discussions_by_trending')
    methods.add(condenser_api.get_discussions_by_hot, 'tags_api.get_discussions_by_hot')
    methods.add(condenser_api.get_discussions_by_promoted, 'tags_api.get_discussions_by_promoted')
    methods.add(condenser_api.get_discussions_by_created, 'tags_api.get_discussions_by_created')
    methods.add(condenser_api.get_discussions_by_blog, 'tags_api.get_discussions_by_blog')
    methods.add(condenser_api.get_discussions_by_comments, 'tags_api.get_discussions_by_comments')
    methods.add(condenser_api.get_discussions_by_author_before_date, 'tags_api.get_discussions_by_author_before_date')
    methods.add(condenser_api.get_post_discussions_by_payout, 'tags_api.get_post_discussions_by_payout')
    methods.add(condenser_api.get_comment_discussions_by_payout, 'tags_api.get_comment_discussions_by_payout')

    methods.add(condenser_api_call)

    return methods

def truncate_response_log(logger):
    """Overwrite jsonrpcserver resp logger to truncate output.

    https://github.com/bcb/jsonrpcserver/issues/65 was one native
    attempt but helps little for more complex response structs.

    See also https://github.com/bcb/jsonrpcserver/issues/73.
    """
    formatter = logging.Formatter('%(levelname)s:%(name)s:%(message).2048s')
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(formatter)

    logger.propagate = False
    logger.addHandler(handler)

def run_server(conf):
    """Configure and launch the API server."""

    # configure jsonrpcserver logging
    log_level = conf.log_level()
    config.debug = (log_level == logging.DEBUG)
    #logging.getLogger('aiohttp.access').setLevel(logging.WARNING)
    logging.getLogger('jsonrpcserver.dispatcher.response').setLevel(log_level)
    truncate_response_log(logging.getLogger('jsonrpcserver.dispatcher.response'))

    # init
    log = logging.getLogger(__name__)
    methods = build_methods()
    #context = dict(db=conf.db())

    app = web.Application()
    app['config'] = dict()
    app['config']['args'] = conf.args()
    app['config']['hive.MAX_DB_ROW_RESULTS'] = 100000
    #app['config']['hive.logger'] = logger

    #async def init_db(app):
    #    args = app['config']['args']
    #    db = make_url(args['database_url'])
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
            if conf.get('sync_to_s3'):
                log.info("could not get head state (%s)", e)
                return None
            raise e

    async def head_age(request):
        """Get hive head block age in seconds. 500 status if age > 15s."""
        #pylint: disable=unused-argument
        healthy_age = 15 # hive is synced if head block within 15s
        state = await _head_state()
        curr_age = state['db_head_age'] if state else 31e6
        status = 500 if curr_age > healthy_age else 200
        return web.Response(status=status, text=str(curr_age))

    async def health(request):
        """Get hive health data. 500 if behind by more than 3 blocks."""
        #pylint: disable=unused-argument
        is_syncer = conf.get('sync_to_s3')
        max_head_age = (conf.get('trail_blocks') + 3) * 3
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
        # debug=True refs https://github.com/bcb/jsonrpcserver/issues/71
        response = await methods.dispatch(request, debug=True)
        headers = {'Access-Control-Allow-Origin': '*'}
        return web.json_response(response, status=200, headers=headers)

    app.router.add_get('/.well-known/healthcheck.json', health)
    app.router.add_get('/head_age', head_age)
    app.router.add_get('/health', health)
    app.router.add_post('/', jsonrpc_handler)

    web.run_app(app, port=app['config']['args']['http_server_port'])
