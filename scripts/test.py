from pprint import pprint
from sqlalchemy import (
    Column,
    Integer,
    MetaData,
    String,
    Table,
    create_engine,
)
from sqlalchemy import select
from steem.account import Account

engine = create_engine('sqlite:///hive.sqlite', echo=True)

metadata = MetaData()

authors = Table(
    'authors',
    metadata,
    Column('id', Integer, primary_key=True, autoincrement=True),
    Column('name', String(16)),
    Column('balances', String),
)

metadata.create_all(engine)

if __name__ == '__main__':
    conn = engine.connect()
    acc = Account('furion')
    r = authors.insert().values(name=acc.name, balances='100')
    engine.execute(r)
    q = select([authors])
    pprint(conn.execute(q).fetchall())
