from hive.db.adapter import Db

DB = Db.instance()

# non-SELECT queries
def query(sql, **kwargs):
    return DB.query(sql, **kwargs)

# SELECT n*m
def query_all(sql, **kwargs):
    return DB.query_all(sql, **kwargs)

# SELECT 1*m
def query_row(sql, **kwargs):
    return DB.query_row(sql, **kwargs)

# SELECT n*1
def query_col(sql, **kwargs):
    return DB.query_col(sql, **kwargs)

# SELECT 1*1
def query_one(sql, **kwargs):
    return DB.query_one(sql, **kwargs)

def db_engine():
    return DB.db_engine()
