# -*- coding: utf-8 -*-
"""Tests for hive_posts_cache_temp query routing."""

import pytest

from hive.db.cache_router import CacheRouter


def test_get_table_returns_temp_for_hot_queries():
    """Hot query types use temp table."""
    for sort in ('trending', 'hot', 'payout', 'payout_comments', 'created', 'promoted'):
        assert CacheRouter.get_table(sort) == CacheRouter.TEMP_TABLE


def test_get_table_returns_main_for_other_queries():
    """Non-hot or unknown query type uses main table."""
    assert CacheRouter.get_table(None) == CacheRouter.MAIN_TABLE
    assert CacheRouter.get_table('blog') == CacheRouter.MAIN_TABLE
    assert CacheRouter.get_table('muted') == CacheRouter.MAIN_TABLE


def test_get_table_muted_uses_main():
    """muted is not in HOT_QUERIES so uses main table."""
    assert CacheRouter.get_table('muted') == CacheRouter.MAIN_TABLE


def test_get_temp_sql_replaces_table_name():
    """get_temp_sql replaces main table with temp table."""
    base = "SELECT post_id FROM hive_posts_cache WHERE depth = 0"
    got = CacheRouter.get_temp_sql(base)
    assert "hive_posts_cache_temp" in got
    assert "WHERE depth = 0" in got
