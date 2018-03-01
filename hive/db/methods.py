"""[Deprecated] Importable methods which proxy to `hive.db.adapter`."""

from hive.db.adapter import Db

DB = Db.instance()

def query(sql, **kwargs):
    """non-SELECT queries"""
    return DB.query(sql, **kwargs)

def query_all(sql, **kwargs):
    """SELECT n*m"""
    return DB.query_all(sql, **kwargs)

def query_row(sql, **kwargs):
    """SELECT 1*m"""
    return DB.query_row(sql, **kwargs)

def query_col(sql, **kwargs):
    """SELECT n*1"""
    return DB.query_col(sql, **kwargs)

def query_one(sql, **kwargs):
    """SELECT 1*1"""
    return DB.query_one(sql, **kwargs)
