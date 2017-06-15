import logging
import os

import sqlalchemy as sa
from sqlalchemy.dialects.mysql import (
    CHAR, SMALLINT, TINYINT,
    TINYTEXT, MEDIUMTEXT, DOUBLE,
)

metadata = sa.MetaData()

hive_blocks = sa.Table(
    'hive_blocks', metadata,
    sa.Column('num', sa.Integer, primary_key=True, autoincrement=False),
    sa.Column('hash', CHAR(40, ascii=True), nullable=False),
    sa.Column('prev', CHAR(40, ascii=True)),
    sa.Column('txs', SMALLINT(unsigned=True), server_default='0', nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.UniqueConstraint('hash', name='hive_blocks_ux1'),
    sa.UniqueConstraint('prev', name='hive_blocks_ux2'),
    sa.ForeignKeyConstraint(['prev'], ['hive_blocks.hash'], name='hive_blocks_fk1'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_accounts = sa.Table(
    'hive_accounts', metadata,
    sa.Column('id', sa.Integer, primary_key=True),
    sa.Column('name', sa.String(16), nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.UniqueConstraint('name', name='hive_accounts_ux1'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_posts = sa.Table(
    'hive_posts', metadata,
    sa.Column('id', sa.Integer, primary_key=True),
    sa.Column('parent_id', sa.Integer),
    sa.Column('author', CHAR(16, ascii=True), nullable=False),
    sa.Column('permlink', CHAR(255, ascii=True), nullable=False),
    sa.Column('community', CHAR(16)),
    sa.Column('category', CHAR(255, ascii=True), nullable=False),
    sa.Column('depth', SMALLINT(unsigned=True), nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.Column('is_deleted', TINYINT(1), nullable=False, server_default='0'),
    sa.Column('is_pinned', TINYINT(1), nullable=False, server_default='0'),
    sa.Column('is_muted', TINYINT(1), nullable=False, server_default='0'),
    sa.ForeignKeyConstraint(['author'], ['hive_accounts.name'], name='hive_posts_fk1'),
    sa.ForeignKeyConstraint(['community'], ['hive_accounts.name'], name='hive_posts_fk2'),
    sa.ForeignKeyConstraint(['parent_id'], ['hive_posts.id'], name='hive_posts_fk3'),
    sa.UniqueConstraint('author', 'permlink', name='hive_posts_ux1'),
    sa.Index('hive_posts_ix1', 'parent_id'),
    sa.Index('hive_posts_ix2', 'is_deleted'),
    sa.Index('hive_posts_ix3', 'created_at', 'author'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_follows = sa.Table(
    'hive_follows', metadata,
    sa.Column('follower', CHAR(16), nullable=False),
    sa.Column('following', CHAR(16), nullable=False),
    sa.Column('is_muted', TINYINT(1), nullable=False, server_default='0'),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.ForeignKeyConstraint(['follower'], ['hive_accounts.name'], name='hive_follows_fk1'),
    sa.ForeignKeyConstraint(['following'], ['hive_accounts.name'], name='hive_follows_fk2'),
    sa.UniqueConstraint('follower', 'following', name='hive_follows_ux1'),
    sa.Index('hive_follows_ix1', 'follower', 'is_muted', 'created_at'),
    sa.Index('hive_follows_ix2', 'following', 'is_muted', 'created_at'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_reblogs = sa.Table(
    'hive_reblogs', metadata,
    sa.Column('account', CHAR(16), nullable=False),
    sa.Column('post_id', sa.Integer, nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.ForeignKeyConstraint(['account'], ['hive_accounts.name'], name='hive_reblogs_fk1'),
    sa.ForeignKeyConstraint(['post_id'], ['hive_posts.id'], name='hive_reblogs_fk2'),
    sa.UniqueConstraint('account', 'post_id', name='hive_reblogs_ux1'),
    sa.Index('hive_reblogs_ix1', 'post_id', 'account', 'created_at'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_communities = sa.Table(
    'hive_communities', metadata,
    sa.Column('name', CHAR(16), primary_key=True),
    sa.Column('title', sa.String(32), nullable=False),
    sa.Column('about', sa.String(255), nullable=False, server_default=''),
    sa.Column('description', sa.String(5000), nullable=False, server_default=''),
    sa.Column('lang', CHAR(2), nullable=False, server_default='en'),
    sa.Column('settings', TINYTEXT, nullable=False),
    sa.Column('type_id', TINYINT(1), nullable=False, server_default='0'),
    sa.Column('is_nsfw', TINYINT(1), nullable=False, server_default='0'),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.ForeignKeyConstraint(['name'], ['hive_accounts.name'], name='hive_communities_fk1'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_members = sa.Table(
    'hive_members', metadata,
    sa.Column('community', CHAR(16), nullable=False),
    sa.Column('account', CHAR(16), nullable=False),
    sa.Column('is_admin', TINYINT(1), nullable=False),
    sa.Column('is_mod', TINYINT(1), nullable=False),
    sa.Column('is_approved', TINYINT(1), nullable=False),
    sa.Column('is_muted', TINYINT(1), nullable=False),
    sa.Column('title', sa.String(255), nullable=False, server_default=''),
    sa.ForeignKeyConstraint(['community'], ['hive_communities.name'], name='hive_members_fk1'),
    sa.ForeignKeyConstraint(['account'], ['hive_accounts.name'], name='hive_members_fk2'),
    sa.UniqueConstraint('community', 'account', name='hive_members_ux1'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_flags = sa.Table(
    'hive_flags', metadata,
    sa.Column('account', CHAR(16), nullable=False),
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
    sa.Column('community', CHAR(16), nullable=False),
    sa.Column('account', CHAR(16), nullable=False),
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
    sa.Column('account', CHAR(16), nullable=False),
    sa.Column('post_id', sa.Integer, primary_key=True),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.ForeignKeyConstraint(['post_id'], ['hive_posts.id'], name='hive_feed_cache_fk1'),
    sa.Index('hive_feed_cache_ix1', 'account', 'post_id', 'created_at'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_posts_cache = sa.Table(
    'hive_posts_cache', metadata,
    sa.Column('post_id', sa.Integer, primary_key=True),
    sa.Column('author', CHAR(16, ascii=True), nullable=False),
    sa.Column('permlink', CHAR(255, ascii=True), nullable=False),
    sa.Column('title', sa.String(255), nullable=False),
    sa.Column('preview', sa.String(1024), nullable=False),
    sa.Column('img_url', sa.String(1024), nullable=False),
    sa.Column('payout', sa.types.DECIMAL(10, 3), nullable=False),
    sa.Column('promoted', sa.types.DECIMAL(10, 3), nullable=False),
    sa.Column('created_at', sa.DateTime, nullable=False),
    sa.Column('payout_at', sa.DateTime, nullable=False),
    sa.Column('updated_at', sa.DateTime, nullable=False),
    sa.Column('is_nsfw', TINYINT(1), nullable=False, server_default='0'),
    sa.Column('rshares', sa.BigInteger, nullable=False),
    sa.Column('sc_trend', DOUBLE, nullable=False),
    sa.Column('sc_hot', DOUBLE, nullable=False),
    sa.Column('body', sa.Text),
    sa.Column('votes', MEDIUMTEXT),
    sa.Column('json', sa.Text),
    sa.ForeignKeyConstraint(['post_id'], ['hive_posts.id'], name='hive_posts_cache_fk1'),
    sa.Index('hive_posts_cache_ix1', 'payout'),
    sa.Index('hive_posts_cache_ix2', 'promoted'),
    sa.Index('hive_posts_cache_ix3', 'payout_at'),
    sa.Index('hive_posts_cache_ix4', 'updated_at'),
    sa.Index('hive_posts_cache_ix5', 'rshares'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

hive_accounts_cache = sa.Table(
    'hive_accounts_cache', metadata,
    sa.Column('account', CHAR(16), primary_key=True),
    sa.Column('reputation', sa.Float, nullable=False, server_default='25'),
    sa.Column('name', sa.String(20)),
    sa.Column('about', sa.String(160)),
    sa.Column('location', sa.String(30)),
    sa.Column('url', sa.String(100)),
    sa.Column('img_url', sa.String(1024)),
    sa.ForeignKeyConstraint(['account'], ['hive_accounts.name'],
                            name='hive_accounts_cache_fk1'),
    mysql_engine='InnoDB',
    mysql_default_charset='utf8mb4'
)

_url = os.environ.get('DATABASE_URL', 'missing ENV DATABASE_URL')
logging.basicConfig()
if os.environ.get('LOG_LEVEL') == 'INFO':
    logging.getLogger('sqlalchemy.engine').setLevel(logging.INFO)


def connect(connection_url=_url, **kwargs):
    return sa.create_engine(connection_url, isolation_level="READ UNCOMMITTED", **kwargs).connect()


def setup(connection_url=_url):
    engine = sa.create_engine(connection_url)
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


def teardown(connection_url=_url):
    engine = sa.create_engine(connection_url)
    metadata.drop_all(engine)


if __name__ == '__main__':
    teardown()
    setup()
