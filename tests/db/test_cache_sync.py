# -*- coding: utf-8 -*-
"""Tests for hive_posts_cache_temp sync module."""

import pytest

from hive.indexer.cache_sync import CacheSync


def test_sync_constants():
    """Sync window and hot-days constants are set."""
    assert CacheSync.SYNC_WINDOW == 60
    assert CacheSync.HOT_DAYS == 90


def test_sync_returns_immediately():
    """sync() is non-blocking and returns without waiting for background work."""
    # Just ensure we can call it; actual DB work runs in thread
    CacheSync.sync()


def test_sync_skip_when_busy():
    """Calling sync() again while syncing skips (no exception)."""
    CacheSync.sync()
    CacheSync.sync()  # second call should skip and return
