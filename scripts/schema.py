import datetime as dt

from sqlalchemy import (
    Column,
    Integer,
    MetaData,
    Table,
    DateTime,
    Index,
)
from sqlalchemy import create_engine
from sqlalchemy.dialects.mysql import SMALLINT

metadata = MetaData()

addresses = Table(
    'hive_blocks', metadata,
    Column('num', Integer, primary_key=True),
    Column('prev', Integer),
    Column('txs', SMALLINT(unsigned=True), default=0),
    Column('created_at', DateTime),
    Index('hive_blocks_ux1', 'prev', unique=True),
    # ForeignKeyConstraint(name='hive_blocks_fk1', columns='prev', refcolumns='hive_blocks.num'),
    mysql_engine='InnoDB',
    mysql_charset='utf8mb4',
)

if __name__ == '__main__':
    engine = create_engine('sqlite:///hive.sqlite', echo=True)
    metadata.create_all(engine)
    engine.execute(
        addresses.insert([addresses.c.num, addresses.c.prev, addresses.c.created_at]).
        values(num=0, prev=None, created_at=dt.datetime.fromtimestamp(0)))
    # INSERT INTO hive_blocks (num, prev, created_at) VALUES (0, NULL, "1970-01-01T00:00:00");
