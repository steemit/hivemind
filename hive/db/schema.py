"""Db schema definitions and setup routines."""

import sqlalchemy as sa
from sqlalchemy.sql import text as sql_text
from sqlalchemy.types import SMALLINT
from sqlalchemy.types import CHAR
from sqlalchemy.types import VARCHAR
from sqlalchemy.types import TEXT
from sqlalchemy.types import BOOLEAN

#pylint: disable=line-too-long, too-many-lines, bad-whitespace

DB_VERSION = 14

def build_metadata():
    """Build schema def with SqlAlchemy"""
    metadata = sa.MetaData()

    sa.Table(
        'hive_blocks', metadata,
        sa.Column('num', sa.Integer, primary_key=True, autoincrement=False),
        sa.Column('hash', CHAR(40), nullable=False),
        sa.Column('prev', CHAR(40)),
        sa.Column('txs', SMALLINT, server_default='0', nullable=False),
        sa.Column('ops', SMALLINT, server_default='0', nullable=False),
        sa.Column('created_at', sa.DateTime, nullable=False),

        sa.UniqueConstraint('hash', name='hive_blocks_ux1'),
        sa.ForeignKeyConstraint(['prev'], ['hive_blocks.hash'], name='hive_blocks_fk1'),
    )

    sa.Table(
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
        sa.Column('kb_used', sa.Integer, nullable=False, server_default='0'), # deprecated
        sa.Column('rank', sa.Integer, nullable=False, server_default='0'),

        sa.Column('lr_notif_id', sa.Integer, nullable=True),
        sa.Column('active_at', sa.DateTime, nullable=False, server_default='1970-01-01 00:00:00'),
        sa.Column('cached_at', sa.DateTime, nullable=False, server_default='1970-01-01 00:00:00'),
        sa.Column('raw_json', sa.Text),


        sa.UniqueConstraint('name', name='hive_accounts_ux1'),
        sa.Index('hive_accounts_ix1', 'vote_weight', 'id'), # core: quick ranks
        sa.Index('hive_accounts_ix2', 'name', 'id'), # core: quick id map
        sa.Index('hive_accounts_ix3', 'vote_weight', 'name', postgresql_ops=dict(name='varchar_pattern_ops')), # API: lookup
        sa.Index('hive_accounts_ix4', 'id', 'name'), # API: quick filter/sort
        sa.Index('hive_accounts_ix5', 'cached_at', 'name'), # core/listen sweep
    )

    sa.Table(
        'hive_posts', metadata,
        sa.Column('id', sa.Integer, primary_key=True),
        sa.Column('parent_id', sa.Integer),
        sa.Column('author', VARCHAR(16), nullable=False),
        sa.Column('permlink', VARCHAR(255), nullable=False),
        sa.Column('category', VARCHAR(255), nullable=False, server_default=''),
        sa.Column('community_id', sa.Integer, nullable=True),
        sa.Column('created_at', sa.DateTime, nullable=False),
        sa.Column('depth', SMALLINT, nullable=False),
        sa.Column('is_deleted', BOOLEAN, nullable=False, server_default='0'),
        sa.Column('is_pinned', BOOLEAN, nullable=False, server_default='0'),
        sa.Column('is_muted', BOOLEAN, nullable=False, server_default='0'),
        sa.Column('is_valid', BOOLEAN, nullable=False, server_default='1'),
        sa.Column('promoted', sa.types.DECIMAL(10, 3), nullable=False, server_default='0'),

        sa.ForeignKeyConstraint(['author'], ['hive_accounts.name'], name='hive_posts_fk1'),
        sa.ForeignKeyConstraint(['parent_id'], ['hive_posts.id'], name='hive_posts_fk3'),
        sa.UniqueConstraint('author', 'permlink', name='hive_posts_ux1'),
        sa.Index('hive_posts_ix3', 'author', 'depth', 'id', postgresql_where=sql_text("is_deleted = '0'")), # API: author blog/comments
        sa.Index('hive_posts_ix4', 'parent_id', 'id', postgresql_where=sql_text("is_deleted = '0'")), # API: fetching children
        sa.Index('hive_posts_ix5', 'id', postgresql_where=sql_text("is_pinned = '1' AND is_deleted = '0'")), # API: pinned post status
        sa.Index('hive_posts_ix6', 'community_id', 'id', postgresql_where=sql_text("community_id IS NOT NULL AND is_pinned = '1' AND is_deleted = '0'")), # API: community pinned
    )

    sa.Table(
        'hive_post_tags', metadata,
        sa.Column('post_id', sa.Integer, nullable=False),
        sa.Column('tag', sa.String(32), nullable=False),
        sa.UniqueConstraint('tag', 'post_id', name='hive_post_tags_ux1'), # core
        sa.Index('hive_post_tags_ix1', 'post_id'), # core
    )

    sa.Table(
        'hive_follows', metadata,
        sa.Column('follower', sa.Integer, nullable=False),
        sa.Column('following', sa.Integer, nullable=False),
        sa.Column('state', SMALLINT, nullable=False, server_default='1'),
        sa.Column('created_at', sa.DateTime, nullable=False),

        sa.UniqueConstraint('following', 'follower', name='hive_follows_ux3'), # core
        sa.Index('hive_follows_ix5a', 'following', 'state', 'created_at', 'follower'),
        sa.Index('hive_follows_ix5b', 'follower', 'state', 'created_at', 'following'),
    )

    sa.Table(
        'hive_reblogs', metadata,
        sa.Column('account', VARCHAR(16), nullable=False),
        sa.Column('post_id', sa.Integer, nullable=False),
        sa.Column('created_at', sa.DateTime, nullable=False),

        sa.ForeignKeyConstraint(['account'], ['hive_accounts.name'], name='hive_reblogs_fk1'),
        sa.ForeignKeyConstraint(['post_id'], ['hive_posts.id'], name='hive_reblogs_fk2'),
        sa.UniqueConstraint('account', 'post_id', name='hive_reblogs_ux1'), # core
        sa.Index('hive_reblogs_ix1', 'post_id', 'account', 'created_at'), # API -- not yet used
    )

    sa.Table(
        'hive_payments', metadata,
        sa.Column('id', sa.Integer, primary_key=True),
        sa.Column('block_num', sa.Integer, nullable=False),
        sa.Column('tx_idx', SMALLINT, nullable=False),
        sa.Column('post_id', sa.Integer, nullable=False),
        sa.Column('from_account', sa.Integer, nullable=False),
        sa.Column('to_account', sa.Integer, nullable=False),
        sa.Column('amount', sa.types.DECIMAL(10, 3), nullable=False),
        sa.Column('token', VARCHAR(5), nullable=False),

        sa.ForeignKeyConstraint(['from_account'], ['hive_accounts.id'], name='hive_payments_fk1'),
        sa.ForeignKeyConstraint(['to_account'], ['hive_accounts.id'], name='hive_payments_fk2'),
        sa.ForeignKeyConstraint(['post_id'], ['hive_posts.id'], name='hive_payments_fk3'),
    )

    sa.Table(
        'hive_feed_cache', metadata,
        sa.Column('post_id', sa.Integer, nullable=False),
        sa.Column('account_id', sa.Integer, nullable=False),
        sa.Column('created_at', sa.DateTime, nullable=False),
        sa.UniqueConstraint('post_id', 'account_id', name='hive_feed_cache_ux1'), # core
        sa.Index('hive_feed_cache_ix1', 'account_id', 'post_id', 'created_at'), # API (and rebuild?)
    )

    sa.Table(
        'hive_posts_cache', metadata,
        sa.Column('post_id', sa.Integer, primary_key=True, autoincrement=False),
        sa.Column('author', VARCHAR(16), nullable=False),
        sa.Column('permlink', VARCHAR(255), nullable=False),
        sa.Column('category', VARCHAR(255), nullable=False, server_default=''),

        # important/index
        sa.Column('community_id', sa.Integer, nullable=True),
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

        # index: misc
        sa.Index('hive_posts_cache_ix3',  'payout_at', 'post_id',           postgresql_where=sql_text("is_paidout = '0'")),         # core: payout sweep
        sa.Index('hive_posts_cache_ix8',  'category', 'payout', 'depth',    postgresql_where=sql_text("is_paidout = '0'")),         # API: tag stats

        # index: ranked posts
        sa.Index('hive_posts_cache_ix2',  'promoted',             postgresql_where=sql_text("is_paidout = '0' AND promoted > 0")),  # API: promoted

        sa.Index('hive_posts_cache_ix6a', 'sc_trend', 'post_id',  postgresql_where=sql_text("is_paidout = '0'")),                   # API: trending             todo: depth=0
        sa.Index('hive_posts_cache_ix7a', 'sc_hot',   'post_id',  postgresql_where=sql_text("is_paidout = '0'")),                   # API: hot                  todo: depth=0
        sa.Index('hive_posts_cache_ix6b', 'post_id',  'sc_trend', postgresql_where=sql_text("is_paidout = '0'")),                   # API: trending, filtered   todo: depth=0
        sa.Index('hive_posts_cache_ix7b', 'post_id',  'sc_hot',   postgresql_where=sql_text("is_paidout = '0'")),                   # API: hot, filtered        todo: depth=0

        sa.Index('hive_posts_cache_ix9a',             'depth', 'payout', 'post_id', postgresql_where=sql_text("is_paidout = '0'")), # API: payout               todo: rem depth
        sa.Index('hive_posts_cache_ix9b', 'category', 'depth', 'payout', 'post_id', postgresql_where=sql_text("is_paidout = '0'")), # API: payout, filtered     todo: rem depth

        sa.Index('hive_posts_cache_ix10', 'post_id', 'payout',                      postgresql_where=sql_text("is_grayed = '1' AND payout > 0")), # API: muted, by filter/date/payout

        # index: stats
        sa.Index('hive_posts_cache_ix20', 'community_id', 'author', 'payout', 'post_id', postgresql_where=sql_text("is_paidout = '0'")), # API: pending distribution; author payout

        # index: community ranked posts
        sa.Index('hive_posts_cache_ix30', 'community_id', 'sc_trend',   'post_id',  postgresql_where=sql_text("community_id IS NOT NULL AND is_grayed = '0' AND depth = 0")),        # API: community trend
        sa.Index('hive_posts_cache_ix31', 'community_id', 'sc_hot',     'post_id',  postgresql_where=sql_text("community_id IS NOT NULL AND is_grayed = '0' AND depth = 0")),        # API: community hot
        sa.Index('hive_posts_cache_ix32', 'community_id', 'created_at', 'post_id',  postgresql_where=sql_text("community_id IS NOT NULL AND is_grayed = '0' AND depth = 0")),        # API: community created
        sa.Index('hive_posts_cache_ix33', 'community_id', 'payout',     'post_id',  postgresql_where=sql_text("community_id IS NOT NULL AND is_grayed = '0' AND is_paidout = '0'")), # API: community payout
        sa.Index('hive_posts_cache_ix34', 'community_id', 'payout',     'post_id',  postgresql_where=sql_text("community_id IS NOT NULL AND is_grayed = '1' AND is_paidout = '0'")), # API: community muted
    )

    sa.Table(
        'hive_state', metadata,
        sa.Column('block_num', sa.Integer, primary_key=True, autoincrement=False),
        sa.Column('db_version', sa.Integer, nullable=False),
        sa.Column('steem_per_mvest', sa.types.DECIMAL(8, 3), nullable=False),
        sa.Column('usd_per_steem', sa.types.DECIMAL(8, 3), nullable=False),
        sa.Column('sbd_per_steem', sa.types.DECIMAL(8, 3), nullable=False),
        sa.Column('dgpo', sa.Text, nullable=False),
    )

    metadata = build_metadata_community(metadata)

    return metadata

def build_metadata_community(metadata=None):
    """Build community schema defs"""
    if not metadata:
        metadata = sa.MetaData()

    sa.Table(
        'hive_communities', metadata,
        sa.Column('id',          sa.Integer,      primary_key=True, autoincrement=False),
        sa.Column('type_id',     SMALLINT,        nullable=False),
        sa.Column('lang',        CHAR(2),         nullable=False, server_default='en'),
        sa.Column('name',        VARCHAR(16),     nullable=False),
        sa.Column('title',       sa.String(32),   nullable=False, server_default=''),
        sa.Column('created_at',  sa.DateTime,     nullable=False),
        sa.Column('sum_pending', sa.Integer,      nullable=False, server_default='0'),
        sa.Column('num_pending', sa.Integer,      nullable=False, server_default='0'),
        sa.Column('rank',        sa.Integer,      nullable=False, server_default='0'),
        sa.Column('subscribers', sa.Integer,      nullable=False, server_default='0'),
        sa.Column('is_nsfw',     BOOLEAN,         nullable=False, server_default='0'),
        sa.Column('about',       sa.String(120),  nullable=False, server_default=''),
        sa.Column('primary_tag', sa.String(32),   nullable=False, server_default=''),
        sa.Column('avatar_url',  sa.String(1024), nullable=False, server_default=''),
        sa.Column('description', sa.String(5000), nullable=False, server_default=''),
        sa.Column('flag_text',   sa.String(5000), nullable=False, server_default=''),
        sa.Column('settings',    TEXT,            nullable=False, server_default='{}'),

        sa.UniqueConstraint('name', name='hive_communities_ux1'),
        sa.Index('hive_communities_ix1', 'rank', 'id')
    )

    sa.Table(
        'hive_roles', metadata,
        sa.Column('account_id',   sa.Integer,     nullable=False),
        sa.Column('community_id', sa.Integer,     nullable=False),
        sa.Column('created_at',   sa.DateTime,    nullable=False),
        sa.Column('role_id',      SMALLINT,       nullable=False, server_default='0'),
        sa.Column('title',        sa.String(140), nullable=False, server_default=''),

        sa.UniqueConstraint('account_id', 'community_id', name='hive_roles_ux1'),
        sa.Index('hive_roles_ix1', 'community_id', 'account_id', 'role_id'),
    )

    sa.Table(
        'hive_subscriptions', metadata,
        sa.Column('account_id',   sa.Integer,  nullable=False),
        sa.Column('community_id', sa.Integer,  nullable=False),
        sa.Column('created_at',   sa.DateTime, nullable=False),

        sa.UniqueConstraint('account_id', 'community_id', name='hive_subscriptions_ux1'),
        sa.Index('hive_subscriptions_ix1', 'community_id', 'account_id', 'created_at'),
    )

    sa.Table(
        'hive_notifs', metadata,
        sa.Column('id',           sa.Integer,  primary_key=True),
        sa.Column('type_id',      SMALLINT,    nullable=False),
        sa.Column('score',        SMALLINT,    nullable=False),
        sa.Column('created_at',   sa.DateTime, nullable=False),
        sa.Column('src_id',       sa.Integer,  nullable=True),
        sa.Column('dst_id',       sa.Integer,  nullable=True),
        sa.Column('post_id',      sa.Integer,  nullable=True),
        sa.Column('community_id', sa.Integer,  nullable=True),
        sa.Column('block_num',    sa.Integer,  nullable=True),
        sa.Column('payload',      sa.Text,     nullable=True),

        sa.Index('hive_notifs_ix1', 'dst_id',                  'id', postgresql_where=sql_text("dst_id IS NOT NULL")),
        sa.Index('hive_notifs_ix2', 'community_id',            'id', postgresql_where=sql_text("community_id IS NOT NULL")),
        sa.Index('hive_notifs_ix3', 'community_id', 'type_id', 'id', postgresql_where=sql_text("community_id IS NOT NULL")),
        sa.Index('hive_notifs_ix4', 'community_id', 'post_id', 'type_id', 'id', postgresql_where=sql_text("community_id IS NOT NULL AND post_id IS NOT NULL")),
        sa.Index('hive_notifs_ix5', 'post_id', 'type_id', 'dst_id', 'src_id', postgresql_where=sql_text("post_id IS NOT NULL AND type_id IN (16,17)")), # filter: dedupe
    )

    return metadata


def teardown(db):
    """Drop all tables"""
    build_metadata().drop_all(db.engine())

def setup(db):
    """Creates all tables and seed data"""
    # initialize schema
    build_metadata().create_all(db.engine())

    # tune auto vacuum/analyze
    reset_autovac(db)

    # default rows
    sqls = [
        "INSERT INTO hive_state (block_num, db_version, steem_per_mvest, usd_per_steem, sbd_per_steem, dgpo) VALUES (0, %d, 0, 0, 0, '')" % DB_VERSION,
        "INSERT INTO hive_blocks (num, hash, created_at) VALUES (0, '0000000000000000000000000000000000000000', '2016-03-24 16:04:57')",
        "INSERT INTO hive_accounts (name, created_at) VALUES ('miners',    '2016-03-24 16:05:00')",
        "INSERT INTO hive_accounts (name, created_at) VALUES ('null',      '2016-03-24 16:05:00')",
        "INSERT INTO hive_accounts (name, created_at) VALUES ('temp',      '2016-03-24 16:05:00')",
        "INSERT INTO hive_accounts (name, created_at) VALUES ('initminer', '2016-03-24 16:05:00')"]
    for sql in sqls:
        db.query(sql)

def reset_autovac(db):
    """Initializes/resets per-table autovacuum/autoanalyze params.

    We use a scale factor of 0 and specify exact threshold tuple counts,
    per-table, in the format (autovacuum_threshold, autoanalyze_threshold)."""

    autovac_config = { #    vacuum  analyze
        'hive_accounts':    (50000, 100000),
        'hive_posts_cache': (25000, 25000),
        'hive_posts':       (2500, 10000),
        'hive_post_tags':   (5000, 10000),
        'hive_follows':     (5000, 5000),
        'hive_feed_cache':  (5000, 5000),
        'hive_blocks':      (5000, 25000),
        'hive_reblogs':     (5000, 5000),
        'hive_payments':    (5000, 5000),
    }

    for table, (n_vacuum, n_analyze) in autovac_config.items():
        sql = """ALTER TABLE %s SET (autovacuum_vacuum_scale_factor = 0,
                                     autovacuum_vacuum_threshold = %s,
                                     autovacuum_analyze_scale_factor = 0,
                                     autovacuum_analyze_threshold = %s)"""
        db.query(sql % (table, n_vacuum, n_analyze))
