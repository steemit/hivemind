import logging

import sqlalchemy as sa
from sqlalchemy.sql import text as sql_text
from sqlalchemy.types import SMALLINT
from sqlalchemy.types import CHAR
from sqlalchemy.types import VARCHAR
from sqlalchemy.types import TEXT
from sqlalchemy.types import BOOLEAN

from hive.conf import Conf

#pylint: disable=line-too-long

metadata = sa.MetaData()

hive_blocks = sa.Table(
    'hive_blocks', metadata,
    sa.Column('num', sa.Integer, primary_key=True, autoincrement=False),
    sa.Column('hash', CHAR(40), nullable=False),
    sa.Column('prev', CHAR(40)),
    sa.Column('txs', SMALLINT, server_default='0', nullable=False),
    sa.Column('ops', SMALLINT, server_default='0', nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),

    sa.UniqueConstraint('hash', name='hive_blocks_ux1'),
    sa.ForeignKeyConstraint(['prev'], ['hive_blocks.hash'], name='hive_blocks_fk1'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_accounts = sa.Table(
    'hive_accounts', metadata,
    sa.Column('id', sa.Integer, primary_key=True),
    sa.Column('name', VARCHAR(16), nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    #sa.Column('block_num', sa.Integer, nullable=False),
    sa.Column('reputation', sa.Float(precision=6), nullable=False, server_default='25'),

    sa.Column('display_name', sa.String(20)),
    sa.Column('about', sa.String(160)),
    sa.Column('location', sa.String(30)),
    sa.Column('website', sa.String(100)),
    sa.Column('profile_image', sa.String(1024), nullable=False, server_default=''),
    sa.Column('cover_image', sa.String(1024), nullable=False, server_default=''),

    sa.Column('followers', sa.Integer, nullable=False, server_default='0'),
    sa.Column('following', sa.Integer, nullable=False, server_default='0'),

    sa.Column('proxy', VARCHAR(16), nullable=False, server_default=''),
    sa.Column('post_count', sa.Integer, nullable=False, server_default='0'),
    sa.Column('proxy_weight', sa.Float(precision=6), nullable=False, server_default='0'),
    sa.Column('vote_weight', sa.Float(precision=6), nullable=False, server_default='0'),
    sa.Column('kb_used', sa.Integer, nullable=False, server_default='0'),
    sa.Column('rank', sa.Integer, nullable=False, server_default='0'),

    sa.Column('active_at', sa.DateTime, nullable=False, server_default='1970-01-01 00:00:00'),
    sa.Column('cached_at', sa.DateTime, nullable=False, server_default='1970-01-01 00:00:00'),
    sa.Column('raw_json', sa.Text),

    sa.UniqueConstraint('name', name='hive_accounts_ux1'),
    sa.Index('hive_accounts_ix1', 'vote_weight', 'id'), # core: quick ranks
    sa.Index('hive_accounts_ix2', 'name', 'id'), # core: quick id map
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_posts = sa.Table(
    'hive_posts', metadata,
    sa.Column('id', sa.Integer, primary_key=True),
    sa.Column('parent_id', sa.Integer),
    sa.Column('author', VARCHAR(16), nullable=False),
    sa.Column('permlink', VARCHAR(255), nullable=False),
    sa.Column('community', VARCHAR(16), nullable=False),
    sa.Column('category', VARCHAR(255), nullable=False, server_default=''),
    sa.Column('depth', SMALLINT, nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.Column('is_deleted', BOOLEAN, nullable=False, server_default='0'),
    sa.Column('is_pinned', BOOLEAN, nullable=False, server_default='0'),
    sa.Column('is_muted', BOOLEAN, nullable=False, server_default='0'),
    sa.Column('is_valid', BOOLEAN, nullable=False, server_default='1'),
    sa.Column('promoted', sa.types.DECIMAL(10, 3), nullable=False, server_default='0'),

    sa.ForeignKeyConstraint(['author'], ['hive_accounts.name'], name='hive_posts_fk1'),
    sa.ForeignKeyConstraint(['community'], ['hive_accounts.name'], name='hive_posts_fk2'),
    sa.ForeignKeyConstraint(['parent_id'], ['hive_posts.id'], name='hive_posts_fk3'),
    sa.UniqueConstraint('author', 'permlink', name='hive_posts_ux1'),
    sa.Index('hive_posts_ix1', 'parent_id'), # API
    sa.Index('hive_posts_ix2', 'is_deleted', 'depth'), # API
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

#hive_tags = sa.Table(
#    'hive_tags', metadata,
#    sa.Column('id', sa.Integer, primary_key=True),
#    sa.Column('name', CHAR(64), nullable=False),
#    sa.UniqueConstraint('name', name='hive_tags_ux1'),
#    mysql_engine='InnoDB',
#    mysql_default_charset='utf8mb4'
#)

hive_post_tags = sa.Table(
    'hive_post_tags', metadata,
    sa.Column('post_id', sa.Integer, nullable=False),
    sa.Column('tag', sa.String(32), nullable=False),
    sa.UniqueConstraint('tag', 'post_id', name='hive_post_tags_ux1'), # core
    sa.Index('hive_post_tags_ix1', 'post_id'), # core
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_follows = sa.Table(
    'hive_follows', metadata,
    sa.Column('follower', sa.Integer, nullable=False),
    sa.Column('following', sa.Integer, nullable=False),
    sa.Column('state', SMALLINT, nullable=False, server_default='1'),
    sa.Column('created_at', sa.DateTime, nullable=False),

    sa.UniqueConstraint('following', 'follower', name='hive_follows_ux3'), # core
    sa.Index('hive_follows_ix2', 'following', 'follower', postgresql_where=sql_text("state = 1")), # API
    sa.Index('hive_follows_ix3', 'follower', 'following', postgresql_where=sql_text("state = 1")), # API
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_reblogs = sa.Table(
    'hive_reblogs', metadata,
    sa.Column('account', VARCHAR(16), nullable=False),
    sa.Column('post_id', sa.Integer, nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),

    sa.ForeignKeyConstraint(['account'], ['hive_accounts.name'], name='hive_reblogs_fk1'),
    sa.ForeignKeyConstraint(['post_id'], ['hive_posts.id'], name='hive_reblogs_fk2'),
    sa.UniqueConstraint('account', 'post_id', name='hive_reblogs_ux1'), # core
    sa.Index('hive_reblogs_ix1', 'post_id', 'account', 'created_at'), # API -- TODO: seemingly unused
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_payments = sa.Table(
    'hive_payments', metadata,
    sa.Column('id', sa.Integer, primary_key=True),
    sa.Column('block_num', sa.Integer, nullable=False),
    sa.Column('tx_idx', SMALLINT, nullable=False),
    sa.Column('post_id', sa.Integer, nullable=False),
    sa.Column('from_account', VARCHAR(16), nullable=False),
    sa.Column('to_account', VARCHAR(16), nullable=False),
    sa.Column('amount', sa.types.DECIMAL(10, 3), nullable=False),
    sa.Column('token', VARCHAR(5), nullable=False),

    sa.ForeignKeyConstraint(['from_account'], ['hive_accounts.name'], name='hive_payments_fk1'),
    sa.ForeignKeyConstraint(['to_account'], ['hive_accounts.name'], name='hive_payments_fk2'),
    sa.ForeignKeyConstraint(['post_id'], ['hive_posts.id'], name='hive_payments_fk3'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_communities = sa.Table(
    'hive_communities', metadata,
    sa.Column('name', VARCHAR(16), primary_key=True),
    sa.Column('title', sa.String(32), nullable=False),
    sa.Column('about', sa.String(255), nullable=False, server_default=''),
    sa.Column('description', sa.String(5000), nullable=False, server_default=''),
    sa.Column('lang', CHAR(2), nullable=False, server_default='en'),
    sa.Column('settings', TEXT, nullable=False),
    sa.Column('type_id', SMALLINT, nullable=False, server_default='0'),
    sa.Column('is_nsfw', BOOLEAN, nullable=False, server_default='0'),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.ForeignKeyConstraint(['name'], ['hive_accounts.name'], name='hive_communities_fk1'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_members = sa.Table(
    'hive_members', metadata,
    sa.Column('community', VARCHAR(16), nullable=False),
    sa.Column('account', VARCHAR(16), nullable=False),
    sa.Column('is_admin', BOOLEAN, nullable=False),
    sa.Column('is_mod', BOOLEAN, nullable=False),
    sa.Column('is_approved', BOOLEAN, nullable=False),
    sa.Column('is_muted', BOOLEAN, nullable=False),
    sa.Column('title', sa.String(255), nullable=False, server_default=''),
    sa.ForeignKeyConstraint(['community'], ['hive_communities.name'], name='hive_members_fk1'),
    sa.ForeignKeyConstraint(['account'], ['hive_accounts.name'], name='hive_members_fk2'),
    sa.UniqueConstraint('community', 'account', name='hive_members_ux1'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_flags = sa.Table(
    'hive_flags', metadata,
    sa.Column('account', VARCHAR(16), nullable=False),
    sa.Column('post_id', sa.Integer, nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.Column('notes', sa.String(255), nullable=False),
    sa.ForeignKeyConstraint(['account'], ['hive_accounts.name'], name='hive_flags_fk1'),
    sa.ForeignKeyConstraint(['post_id'], ['hive_posts.id'], name='hive_flags_fk2'),
    sa.UniqueConstraint('account', 'post_id', name='hive_flags_ux1'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_modlog = sa.Table(
    'hive_modlog', metadata,
    sa.Column('id', sa.Integer, primary_key=True),
    sa.Column('community', VARCHAR(16), nullable=False),
    sa.Column('account', VARCHAR(16), nullable=False),
    sa.Column('action', sa.String(32), nullable=False),
    sa.Column('params', sa.String(1000), nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.ForeignKeyConstraint(['community'], ['hive_communities.name'], name='hive_modlog_fk1'),
    sa.ForeignKeyConstraint(['account'], ['hive_accounts.name'], name='hive_modlog_fk2'),
    sa.Index('hive_modlog_ix1', 'community', 'created_at'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_feed_cache = sa.Table(
    'hive_feed_cache', metadata,
    sa.Column('post_id', sa.Integer, nullable=False),
    sa.Column('account_id', sa.Integer, nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.UniqueConstraint('post_id', 'account_id', name='hive_feed_cache_ux1'), # core
    sa.Index('hive_feed_cache_ix1', 'account_id', 'post_id', 'created_at'), # API (and rebuild?)
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_posts_cache = sa.Table(
    'hive_posts_cache', metadata,
    sa.Column('post_id', sa.Integer, primary_key=True),
    sa.Column('author', VARCHAR(16), nullable=False),
    sa.Column('permlink', VARCHAR(255), nullable=False),
    sa.Column('category', VARCHAR(255), nullable=False, server_default=''),

    # important/index
    sa.Column('depth', SMALLINT, nullable=False, server_default='0'),
    sa.Column('children', SMALLINT, nullable=False, server_default='0'),

    # basic/extended-stats
    sa.Column('author_rep', sa.Float(precision=6), nullable=False, server_default='0'),
    sa.Column('flag_weight', sa.Float(precision=6), nullable=False, server_default='0'),
    sa.Column('total_votes', sa.Integer, nullable=False, server_default='0'),
    sa.Column('up_votes', sa.Integer, nullable=False, server_default='0'),

    # basic ui fields
    sa.Column('title', sa.String(255), nullable=False, server_default=''),
    sa.Column('preview', sa.String(1024), nullable=False, server_default=''),
    sa.Column('img_url', sa.String(1024), nullable=False, server_default=''),

    # core stats/indexes
    sa.Column('payout', sa.types.DECIMAL(10, 3), nullable=False, server_default='0'),
    sa.Column('promoted', sa.types.DECIMAL(10, 3), nullable=False, server_default='0'),
    sa.Column('created_at', sa.DateTime, nullable=False, server_default='1990-01-01'),
    sa.Column('payout_at', sa.DateTime, nullable=False, server_default='1990-01-01'),
    sa.Column('updated_at', sa.DateTime, nullable=False, server_default='1990-01-01'),
    sa.Column('is_paidout', BOOLEAN, nullable=False, server_default='0'),

    # ui flags/filters
    sa.Column('is_nsfw', BOOLEAN, nullable=False, server_default='0'),
    sa.Column('is_declined', BOOLEAN, nullable=False, server_default='0'),
    sa.Column('is_full_power', BOOLEAN, nullable=False, server_default='0'),
    sa.Column('is_hidden', BOOLEAN, nullable=False, server_default='0'),
    sa.Column('is_grayed', BOOLEAN, nullable=False, server_default='0'),

    # important indexes
    sa.Column('rshares', sa.BigInteger, nullable=False, server_default='0'),
    sa.Column('sc_trend', sa.Float(precision=6), nullable=False, server_default='0'),
    sa.Column('sc_hot', sa.Float(precision=6), nullable=False, server_default='0'),

    # bulk data
    sa.Column('body', TEXT),
    sa.Column('votes', TEXT),
    sa.Column('json', sa.Text),
    sa.Column('raw_json', sa.Text),

    sa.Index('hive_posts_cache_ix2', 'promoted', postgresql_where=sql_text("is_paidout = '0' AND promoted > 0")), # API
    sa.Index('hive_posts_cache_ix3', 'payout_at', 'post_id', postgresql_where=sql_text("is_paidout = '0'")), # core
    sa.Index('hive_posts_cache_ix6', 'sc_trend', 'post_id'), # API
    sa.Index('hive_posts_cache_ix7', 'sc_hot', 'post_id'), # API
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_state = sa.Table(
    'hive_state', metadata,
    sa.Column('block_num', sa.Integer, primary_key=True, autoincrement=False),
    sa.Column('db_version', sa.Integer, nullable=False),
    sa.Column('steem_per_mvest', sa.types.DECIMAL(8, 3), nullable=False),
    sa.Column('usd_per_steem', sa.types.DECIMAL(8, 3), nullable=False),
    sa.Column('sbd_per_steem', sa.types.DECIMAL(8, 3), nullable=False),
    sa.Column('dgpo', sa.Text, nullable=False),

    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

logging.basicConfig()
#if Conf.get('log_level') == 'INFO': # ultra-verbose
#    logging.getLogger('sqlalchemy.engine').setLevel(logging.INFO)
logging.getLogger('sqlalchemy.engine').setLevel(logging.WARNING)


def _create_engine(echo=False):
    engine = sa.create_engine(
        Conf.get('database_url'),
        isolation_level="READ UNCOMMITTED", # only works in mysql
        pool_recycle=3600,
        echo=echo)
    return engine

def connect(echo=False):
    return _create_engine(echo=echo).connect()

def setup():
    engine = _create_engine(echo=True)
    metadata.create_all(engine)

    conn = engine.connect()

    # Insert hive_blocks data
    insert = hive_blocks.insert().values(num=0, hash='0000000000000000000000000000000000000000', prev=None, created_at='1970-01-01T00:00:00')
    conn.execute(insert)

    # Insert hive_accounts data
    insert = hive_accounts.insert()
    conn.execute(insert, [
        {'name': 'miners', 'created_at': '1970-01-01T00:00:00'},
        {'name': 'null', 'created_at': '1970-01-01T00:00:00'},
        {'name': 'temp', 'created_at': '1970-01-01T00:00:00'},
        {'name': 'initminer', 'created_at': '1970-01-01T00:00:00'}
    ])

    # Insert hive_state data
    insert = hive_state.insert().values(block_num=0, db_version=3, steem_per_mvest=0, usd_per_steem=0, sbd_per_steem=0, dgpo='')
    conn.execute(insert)

def teardown():
    engine = _create_engine(echo=True)
    metadata.drop_all(engine)


if __name__ == '__main__':
    teardown()
    setup()
