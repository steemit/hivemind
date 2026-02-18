"""Query routing for hive_posts_cache vs hive_posts_cache_temp."""


class CacheRouter:
    """Route hot-data queries to temp table, others to main table."""

    TEMP_TABLE = 'hive_posts_cache_temp'
    MAIN_TABLE = 'hive_posts_cache'

    HOT_QUERIES = {
        'trending', 'hot', 'payout', 'payout_comments', 'created', 'promoted'
    }

    @classmethod
    def get_table(cls, query_type=None):
        """Return table name for the given query type."""
        if query_type and query_type in cls.HOT_QUERIES:
            return cls.TEMP_TABLE
        return cls.MAIN_TABLE

    @classmethod
    def get_temp_sql(cls, base_sql):
        """Replace main table name with temp table in SQL."""
        return base_sql.replace(cls.MAIN_TABLE, cls.TEMP_TABLE)
