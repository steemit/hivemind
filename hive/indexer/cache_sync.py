"""Sync hive_posts_cache to hive_posts_cache_temp (non-blocking, 30s interval)."""

import logging
import threading
from datetime import datetime, timedelta

from hive.db.adapter import Db

log = logging.getLogger(__name__)


class CacheSync:
    """Sync hive_posts_cache to temp table (non-blocking)."""

    SYNC_WINDOW = 30
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
        """Core sync logic (one run)."""
        try:
            db = Db.instance()
        except AssertionError:
            # Db shared instance may not be initialized yet (or in unit tests).
            log.debug("CacheSync: Db shared instance not initialized, skip")
            return {'inserted': 0, 'updated': 0, 'deleted': 0}

        now = datetime.now()
        cutoff = now - timedelta(days=cls.HOT_DAYS)
        sync_from = now - timedelta(seconds=cls.SYNC_WINDOW * 2)

        stats = {'inserted': 0, 'updated': 0, 'deleted': 0}

        try:
            sql = """
                INSERT INTO hive_posts_cache_temp
                SELECT post_id, author, permlink, category, community_id, depth, children,
                       author_rep, flag_weight, total_votes, up_votes, title, preview, img_url,
                       payout, promoted, created_at, payout_at, updated_at, is_paidout,
                       is_nsfw, is_declined, is_full_power, is_hidden, is_grayed,
                       rshares, sc_trend, sc_hot, body, votes, json, raw_json,
                       :now as _synced_at
                FROM hive_posts_cache
                WHERE created_at >= :cutoff
                  AND updated_at >= :sync_from
                ON CONFLICT (post_id) DO UPDATE SET
                    author = EXCLUDED.author,
                    permlink = EXCLUDED.permlink,
                    title = EXCLUDED.title,
                    body = EXCLUDED.body,
                    payout = EXCLUDED.payout,
                    sc_trend = EXCLUDED.sc_trend,
                    sc_hot = EXCLUDED.sc_hot,
                    votes = EXCLUDED.votes,
                    updated_at = EXCLUDED.updated_at,
                    is_paidout = EXCLUDED.is_paidout,
                    is_hidden = EXCLUDED.is_hidden,
                    is_grayed = EXCLUDED.is_grayed,
                    _synced_at = EXCLUDED._synced_at
            """
            result = db.query(sql, now=now, cutoff=cutoff, sync_from=sync_from)
            stats['updated'] = result.rowcount if hasattr(result, 'rowcount') else 0

            sql = """
                DELETE FROM hive_posts_cache_temp
                WHERE _synced_at < :sync_from
                  AND post_id NOT IN (
                      SELECT post_id FROM hive_posts_cache
                  )
            """
            result = db.query(sql, sync_from=sync_from)
            stats['deleted'] = result.rowcount if hasattr(result, 'rowcount') else 0

            sql = """
                DELETE FROM hive_posts_cache_temp
                WHERE created_at < :cutoff
            """
            result = db.query(sql, cutoff=cutoff)
            stats['deleted'] += result.rowcount if hasattr(result, 'rowcount') else 0

            log.info("CacheSync: inserted/updated=%d, deleted=%d",
                     stats['updated'], stats['deleted'])
        except Exception as e:
            log.error("CacheSync failed: %s", str(e))

        return stats

    @classmethod
    def init_temp_table(cls):
        """Bootstrap temp table (first run)."""
        try:
            db = Db.instance()
        except AssertionError:
            log.debug("CacheSync: Db shared instance not initialized, skip init")
            return

        cutoff = datetime.now() - timedelta(days=cls.HOT_DAYS)

        sql = """
            INSERT INTO hive_posts_cache_temp (
                post_id, author, permlink, category, community_id, depth, children,
                author_rep, flag_weight, total_votes, up_votes, title, preview, img_url,
                payout, promoted, created_at, payout_at, updated_at, is_paidout,
                is_nsfw, is_declined, is_full_power, is_hidden, is_grayed,
                rshares, sc_trend, sc_hot, body, votes, json, raw_json, _synced_at
            )
            SELECT post_id, author, permlink, category, community_id, depth, children,
                   author_rep, flag_weight, total_votes, up_votes, title, preview, img_url,
                   payout, promoted, created_at, payout_at, updated_at, is_paidout,
                   is_nsfw, is_declined, is_full_power, is_hidden, is_grayed,
                   rshares, sc_trend, sc_hot, body, votes, json, raw_json,
                   NOW() as _synced_at
            FROM hive_posts_cache
            WHERE created_at >= :cutoff
        """
        db.query(sql, cutoff=cutoff)
        log.info("CacheSync: initialized temp table with %d days of data", cls.HOT_DAYS)
