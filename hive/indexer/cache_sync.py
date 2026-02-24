"""Sync hive_posts_cache to hive_posts_cache_temp (non-blocking, 60s interval)."""

import logging
import threading
from datetime import datetime, timedelta

from hive.db.adapter import Db

log = logging.getLogger(__name__)


class CacheSync:
    """Sync hive_posts_cache to temp table (non-blocking).

    Runs every 60s (20 blocks). DELETE steps only (remove rows outside 90-day window).
    Orphan rows (post_id not in hive_posts_cache) are removed at delete time in
    cached_post.delete() and blocks fork recovery; no NOT IN sweep here.
    Cold-start backfill is done once in db_state migration (version 24).
    """

    SYNC_WINDOW = 60
    HOT_DAYS = 90

    _syncing = False
    _lock = threading.Lock()

    @classmethod
    def sync(cls):
        """Trigger sync (non-blocking, returns immediately).

        If a sync is already running, skip this invocation.
        Sync runs in a background thread and does not block the listen loop.
        """
        with cls._lock:
            if cls._syncing:
                log.debug("CacheSync: previous sync still running, skip")
                return
            cls._syncing = True

        thread = threading.Thread(target=cls._do_sync, daemon=True)
        thread.start()

    @classmethod
    def _do_sync(cls):
        """Run actual sync logic (background thread)."""
        try:
            cls._sync()
        finally:
            with cls._lock:
                cls._syncing = False

    @classmethod
    def _sync(cls):
        """Core sync logic (one run). DELETE steps only (out-of-window rows)."""
        try:
            db = Db.instance()
        except AssertionError:
            # Db shared instance may not be initialized yet (or in unit tests).
            log.debug("CacheSync: Db shared instance not initialized, skip")
            return {'inserted': 0, 'updated': 0, 'deleted': 0}

        now = datetime.now()
        cutoff = now - timedelta(days=cls.HOT_DAYS)

        stats = {'inserted': 0, 'updated': 0, 'deleted': 0}

        try:
            # Orphan rows in temp are now removed at delete time (cached_post.delete + blocks fork).
            # Only prune rows outside the 90-day hot window.
            sql = """
                DELETE FROM hive_posts_cache_temp
                WHERE created_at < :cutoff
            """
            result = db.query(sql, cutoff=cutoff)
            stats['deleted'] += result.rowcount if hasattr(result, 'rowcount') else 0

            log.info("CacheSync: deleted=%d", stats['deleted'])
        except Exception as e:
            log.error("CacheSync failed: %s", str(e))

        return stats
