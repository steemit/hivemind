"""Hive indexer: various utility tasks"""
import logging

log = logging.getLogger(__name__)

def _last_post_id(db):
    sql = "SELECT id FROM hive_posts ORDER BY id DESC LIMIT 1"
    return db.query_one(sql) or 0

def audit_cache_posts(db):
    """Scan all posts to check for cache inconsistencies."""

    last_id = _last_post_id(db)
    step = 1000000
    steps = int(last_id / step) + 1

    log.info("last post id: %d, batches: %d", last_id, steps)

    sql_missing = """
        SELECT COUNT(*)
          FROM hive_posts hp
     LEFT JOIN hive_posts_cache hpc
            ON hp.id = hpc.post_id
         WHERE hp.is_deleted = False
           AND hp.id BETWEEN :lbound AND :ubound
           AND hpc.post_id IS NULL"""

    sql_extra = """
        SELECT COUNT(*)
          FROM hive_posts hp
          JOIN hive_posts_cache hpc
            ON hp.id = hpc.post_id
         WHERE hp.id BETWEEN :lbound AND :ubound
           AND hp.is_deleted = True"""

    for idx in range(steps):
        lbound = (idx * step) + 1
        ubound = (idx + 1) * step

        missing = db.query_one(sql_missing, lbound=lbound, ubound=ubound)
        log.info("between id %d and %d, missing: %d", lbound, ubound, missing)

        extra = db.query_one(sql_extra, lbound=lbound, ubound=ubound)
        log.info("between id %d and %d, extra: %d", lbound, ubound, extra)
