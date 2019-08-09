# -*- coding: utf-8 -*-
"""Hive JSON-RPC API server."""
import os
import sys
import logging
import time

from datetime import datetime
from sqlalchemy.exc import OperationalError
from aiohttp import web
from jsonrpcserver.methods import Methods
from jsonrpcserver import async_dispatch as dispatch

from hive.server.condenser_api import methods as condenser_api
from hive.server.condenser_api.tags import get_trending_tags as condenser_api_get_trending_tags
from hive.server.condenser_api.get_state import get_state as condenser_api_get_state
from hive.server.condenser_api.call import call as condenser_api_call
from hive.server.common.mutes import Mutes

from hive.server.bridge_api import methods as bridge_api
from hive.server.bridge_api.get_state import get_state as bridge_api_get_state

from hive.server.db import Db

async def db_head_state(context):
    """Status/health check."""
    db = context['db']
    sql = ("SELECT num, created_at, extract(epoch from created_at) ts "
           "FROM hive_blocks ORDER BY num DESC LIMIT 1")
    row = await db.query_row(sql)
    return dict(db_head_block=row['num'],
                db_head_time=str(row['created_at']),
                db_head_age=int(time.time() - row['ts']))

def build_methods():
    """Register all supported hive_api/condenser_api.calls."""
    # pylint: disable=expression-not-assigned, line-too-long
    methods = Methods()

    methods.add(**{'hive.' + method.__name__: method for method in (
        db_head_state,
    )})

    methods.add(**{'condenser_api.' + method.__name__: method for method in (
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
    )})

    # dummy methods -- serve informational error
    methods.add(**{
        'condenser_api.get_account_votes': condenser_api.get_account_votes,
        'tags_api.get_account_votes': condenser_api.get_account_votes,
    })

    # follow_api aliases
    methods.add(**{
        'follow_api.get_followers': condenser_api.get_followers,
        'follow_api.get_following': condenser_api.get_following,
        'follow_api.get_follow_count': condenser_api.get_follow_count,
        'follow_api.get_account_reputations': condenser_api.get_account_reputations,
        'follow_api.get_blog': condenser_api.get_blog,
        'follow_api.get_blog_entries': condenser_api.get_blog_entries,
        'follow_api.get_reblogged_by': condenser_api.get_reblogged_by,
    })

    # tags_api aliases
    methods.add(**{
        'tags_api.get_discussion': condenser_api.get_content,
        'tags_api.get_content_replies': condenser_api.get_content_replies,
        'tags_api.get_discussions_by_trending': condenser_api.get_discussions_by_trending,
        'tags_api.get_discussions_by_hot': condenser_api.get_discussions_by_hot,
        'tags_api.get_discussions_by_promoted': condenser_api.get_discussions_by_promoted,
        'tags_api.get_discussions_by_created': condenser_api.get_discussions_by_created,
        'tags_api.get_discussions_by_blog': condenser_api.get_discussions_by_blog,
        'tags_api.get_discussions_by_comments': condenser_api.get_discussions_by_comments,
        'tags_api.get_discussions_by_author_before_date': condenser_api.get_discussions_by_author_before_date,
        'tags_api.get_post_discussions_by_payout': condenser_api.get_post_discussions_by_payout,
        'tags_api.get_comment_discussions_by_payout': condenser_api.get_comment_discussions_by_payout,
    })

    # legacy `call` style adapter
    methods.add(**{
        'call': condenser_api_call
    })

    # bridge_api methods
    methods.add(**{'bridge_api.' + method.__name__: method for method in (
        bridge_api_get_state,

        bridge_api.get_discussions_by_trending,
        bridge_api.get_discussions_by_hot,
        bridge_api.get_discussions_by_promoted,
        bridge_api.get_discussions_by_created,
        bridge_api.get_post_discussions_by_payout,
        bridge_api.get_comment_discussions_by_payout,

        bridge_api.get_discussions_by_blog,
        bridge_api.get_discussions_by_feed,
        bridge_api.get_discussions_by_comments,
        bridge_api.get_replies_by_last_update,
    )})

    return methods

def truncate_response_log(logger):
    """Overwrite jsonrpcserver resp logger to truncate output.

    https://github.com/bcb/jsonrpcserver/issues/65 was one native
    attempt but helps little for more complex response structs.

    See also https://github.com/bcb/jsonrpcserver/issues/73.
    """
    formatter = logging.Formatter('%(levelname)s:%(name)s:%(message).512s')
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(formatter)

    logger.propagate = False
    logger.addHandler(handler)

def run_server(conf):
    """Configure and launch the API server."""

    # configure jsonrpcserver logging
    log_level = conf.log_level()
    logging.getLogger('aiohttp.access').setLevel(logging.WARNING)
    logging.getLogger('jsonrpcserver.dispatcher.response').setLevel(log_level)
    truncate_response_log(logging.getLogger('jsonrpcserver.dispatcher.response'))

    # init
    log = logging.getLogger(__name__)
    methods = build_methods()

    mutes = Mutes(conf.get('muted_accounts_url'))
    Mutes.set_shared_instance(mutes)

    app = web.Application()
    app['config'] = dict()
    app['config']['args'] = conf.args()
    app['config']['hive.MAX_DB_ROW_RESULTS'] = 100000
    #app['config']['hive.logger'] = logger

    async def init_db(app):
        """Initialize db adapter."""
        args = app['config']['args']
        app['db'] = await Db.create(args['database_url'])

    async def close_db(app):
        """Teardown db adapter."""
        app['db'].close()
        await app['db'].wait_closed()

    app.on_startup.append(init_db)
    app.on_cleanup.append(close_db)

    async def head_age(request):
        """Get hive head block age in seconds. 500 status if age > 15s."""
        #pylint: disable=unused-argument
        healthy_age = 15 # hive is synced if head block within 15s
        try:
            state = await db_head_state(app)
            curr_age = state['db_head_age']
        except Exception as e:
            log.info("could not get head state (%s)", e)
            curr_age = 31e6
        status = 500 if curr_age > healthy_age else 200
        return web.Response(status=status, text=str(curr_age))

    async def health(request):
        """Get hive health state. 500 if db unavailable or too far behind."""
        #pylint: disable=unused-argument
        is_syncer = conf.get('sync_to_s3')

        # while 1 hr is a bit stale, such a condition is a symptom of a
        # writer issue, *not* a reader node issue. Discussion in #174.
        max_head_age = 3600 # 1hr

        try:
            state = await db_head_state(app)
        except OperationalError as e:
            state = None
            log.warning("could not get head state (%s)", e)

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
        response = await dispatch(request, methods=methods, debug=True, context=app)
        if response.wanted:
            headers = {'Access-Control-Allow-Origin': '*'}
            return web.json_response(response.deserialized(), status=200, headers=headers)
        return web.Response()

    if conf.get('sync_to_s3'):
        app.router.add_get('/head_age', head_age)
    app.router.add_get('/.well-known/healthcheck.json', health)
    app.router.add_get('/health', health)
    app.router.add_post('/', jsonrpc_handler)

    web.run_app(app, port=app['config']['args']['http_server_port'])
