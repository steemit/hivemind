"""Utility stats functions."""

import logging
from time import perf_counter as perf

log = logging.getLogger(__name__)

class PayoutStats:
    """Singleton responsible for maintaining payout_stats temp table."""

    _instance = None
    _updated = None
    _db = None

    @classmethod
    def instance(cls):
        """Get the shared instance."""
        assert cls._instance, 'set_shared_instance was never called'
        return cls._instance

    @classmethod
    def set_shared_instance(cls, instance):
        """Set the global/shared instance."""
        cls._instance = instance

    def __init__(self, db):
        self._db = db

    @classmethod
    def all(cls):
        """Return the set of all muted accounts from singleton instance."""
        return cls.instance().accounts

    async def generate(self):
        """Re-generate payout stats temp table."""
        if self._updated and perf() - self._updated < 60 * 60:
            return # only update if age > 1hr

        sql = """
            SELECT community_id,
                   author,
                   SUM(payout) payout,
                   COUNT(*) posts,
                   NULL authors
              FROM hive_posts_cache
             WHERE is_paidout = '0'
          GROUP BY community_id, author

             UNION ALL

            SELECT community_id,
                   NULL author,
                   SUM(payout) payout,
                   COUNT(*) posts,
                   COUNT(DISTINCT(author)) authors
              FROM hive_posts_cache
             WHERE is_paidout = '0'
          GROUP BY community_id
        """

        log.warning("Rebuilding payout_stats")

        await self._db.query("""
            BEGIN;
              DROP TABLE IF EXISTS payout_stats;
            CREATE TEMPORARY TABLE payout_stats AS %s;
            CREATE INDEX payout_stats_ix1
                ON payout_stats (community_id, author, payout);
            COMMIT;
        """ % sql)

        self._updated = perf()
