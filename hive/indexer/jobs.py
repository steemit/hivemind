"""Hive indexer: various utility tasks"""
import logging
from hive.indexer.cached_post import CachedPost

log = logging.getLogger(__name__)

def _last_post_id(db):
    sql = "SELECT post_id FROM hive_posts_cache ORDER BY post_id DESC LIMIT 1"
    return db.query_one(sql) or 0

def audit_cache_missing(db, steem):
    """Scan all posts to check for missing cache entries."""
    last_id = _last_post_id(db)
    step = 1000000
    steps = int(last_id / step) + 1
    log.info("last post id: %d, batches: %d", last_id, steps)

    sql = """
        SELECT hp.id, hp.author, hp.permlink
          FROM hive_posts hp
     LEFT JOIN hive_posts_cache hpc
            ON hp.id = hpc.post_id
         WHERE hp.is_deleted = False
           AND hp.id BETWEEN :lbound AND :ubound
           AND hpc.post_id IS NULL"""

    for idx in range(steps):
        lbound = (idx * step) + 1
        ubound = (idx + 1) * step

        missing = db.query_all(sql, lbound=lbound, ubound=ubound)
        log.info("%d <= id <= %d: %d missing", lbound, ubound, len(missing))
        for row in missing:
            CachedPost.insert(row['author'], row['permlink'], row['id'])

        CachedPost.flush(steem, trx=True)

def audit_cache_deleted(db):
    """Scan all posts to check for extraneous cache entries."""
    last_id = _last_post_id(db)
    step = 1000000
    steps = int(last_id / step) + 1
    log.info("audit_cache_deleted -- last id: %d, batches: %d", last_id, steps)

    sql = """
        SELECT hp.id, hp.author, hp.permlink
          FROM hive_posts hp
          JOIN hive_posts_cache hpc
            ON hp.id = hpc.post_id
         WHERE hp.id BETWEEN :lbound AND :ubound
           AND hp.is_deleted = True"""

    for idx in range(steps):
        lbound = (idx * step) + 1
        ubound = (idx + 1) * step

        extra = db.query_all(sql, lbound=lbound, ubound=ubound)
        log.info("%d <= id <= %d: %d to delete", lbound, ubound, len(extra))
        for row in extra:
            CachedPost.delete(row['id'], row['author'], row['permlink'])
