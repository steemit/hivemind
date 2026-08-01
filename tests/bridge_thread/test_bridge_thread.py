#!/usr/bin/env python3
"""
Unit tests for hive.server.bridge_api.thread.

These are pure-logic tests that do NOT require a live database. They live
under tests/bridge_thread/ (rather than tests/server/) on purpose: the
tests/server/__init__.py eagerly opens a real DB connection, which would
prevent these no-DB tests from running here.

They exercise the connection-pool-exhaustion safeguards added to
`_load_discussion` (MAX_DEPTH / MAX_THREAD_POSTS caps) and verify the
hide-id lookups forward their cache_key/cache_ttl to the db layer.

A fake async db records the calls it receives so assertions can inspect how
many `_child_ids`-equivalent queries ran and what cache params were passed.
"""

# pylint: disable=protected-access,missing-docstring

import asyncio
import os
import sys

# Allow running directly (python test_bridge_thread.py) without pytest's rootdir.
sys.path.insert(0, os.path.join(os.path.dirname(__file__), '../..'))

from hive.server.bridge_api import thread  # noqa: E402
from hive.server.bridge_api.thread import (  # noqa: E402
    MAX_DEPTH, MAX_THREAD_POSTS, _check_posts_hide_id, _get_author_hide_id,
    _get_post_id, _load_discussion,
)

import pytest  # noqa: E402


class FakeAsyncDb:
    """Records calls and returns canned results for the methods thread.py uses.

    `query_one` results are configured per-cache_key. `query_all` results are
    configured per-call-count to simulate walking a comment tree level by level.
    """

    def __init__(self, query_all_seq=None, query_one_map=None):
        self.query_all_calls = []
        self.query_one_calls = []
        self._query_all_seq = query_all_seq or []
        self._query_one_map = query_one_map or {}

    async def query_one(self, sql, **kwargs):
        cache_key = kwargs.get('cache_key')
        self.query_one_calls.append({'sql': sql, 'kwargs': kwargs})
        return self._query_one_map.get(cache_key)

    async def query_all(self, sql, **kwargs):
        self.query_all_calls.append({'sql': sql, 'kwargs': kwargs})
        idx = len(self.query_all_calls) - 1
        if idx < len(self._query_all_seq):
            return self._query_all_seq[idx]
        return []


def _run(coro):
    """Run a coroutine to completion in a fresh event loop."""
    loop = asyncio.new_event_loop()
    try:
        return loop.run_until_complete(coro)
    finally:
        loop.close()


def _install_load_posts_keyed(monkeypatch, posts=None):
    """Stub out load_posts_keyed so _load_discussion needs no real post data."""

    async def _stub(_db, _ids, _truncate_body=0):
        return posts or {}

    monkeypatch.setattr(thread, 'load_posts_keyed', _stub)


def _install_hide_pids_by_ids(monkeypatch, hidden=None):
    """Stub out hide_pids_by_ids so no real filtering happens."""
    hidden = hidden or set()

    async def _stub(_db, cids):
        return [pid for pid in cids if pid in hidden]

    monkeypatch.setattr(thread, 'hide_pids_by_ids', _stub)


def _child_ids_rows(parent_to_children):
    """Build a query_all result list (one batch) from a {parent: [children]} map."""
    return [{
        'parent_id': pid,
        'child_ids': cids
    } for pid, cids in parent_to_children.items()]


def test_load_discussion_respects_max_depth(monkeypatch):
    """A chain deeper than MAX_DEPTH must stop after MAX_DEPTH _child_ids calls.

    Build an infinitely deep single-child chain (1 -> 2 -> 3 -> ...). Each
    query_all call returns the next level. Without the cap this loops forever.
    """
    _install_load_posts_keyed(monkeypatch)
    _install_hide_pids_by_ids(monkeypatch)

    call_count = {'n': 0}

    def next_level():
        call_count['n'] += 1
        parent = call_count['n']  # level N's parent is N
        return _child_ids_rows({parent: [parent + 1]})

    db = FakeAsyncDb()
    # Provide MAX_DEPTH+5 levels so we can prove it stops at the cap rather
    # than running out of data.
    db._query_all_seq = [next_level() for _ in range(MAX_DEPTH + 5)]

    _run(_load_discussion(db, 1))

    # One _child_ids query per depth level, capped at MAX_DEPTH.
    assert len(db.query_all_calls) == MAX_DEPTH


def test_load_discussion_respects_max_thread_posts(monkeypatch):
    """A very wide thread must be truncated at MAX_THREAD_POSTS total posts."""
    _install_load_posts_keyed(monkeypatch)
    _install_hide_pids_by_ids(monkeypatch)

    # Level 1: root has 600 children (already > MAX_THREAD_POSTS=500).
    wide_children = list(range(1000, 1600))
    db = FakeAsyncDb(query_all_seq=[_child_ids_rows({1: wide_children})])

    _run(_load_discussion(db, 1))

    # Root level resolves, then the over-cap batch is truncated to fit.
    assert len(db.query_all_calls) == 2
    # The second call's parent_ids length is what got added: 500 - 1 = 499.
    second_call_ids = db.query_all_calls[1]['kwargs']['ids']
    assert len(second_call_ids) == MAX_THREAD_POSTS - 1


def test_load_discussion_terminates_on_leaf(monkeypatch):
    """A root with no children completes in a single _child_ids call."""
    _install_load_posts_keyed(monkeypatch)
    _install_hide_pids_by_ids(monkeypatch)

    db = FakeAsyncDb(query_all_seq=[_child_ids_rows({1: []})])
    _run(_load_discussion(db, 1))
    assert len(db.query_all_calls) == 1


def test_get_post_id_forwards_cache_params():
    """_get_post_id must pass a long-TTL cache_key so the lookup is cached."""
    db = FakeAsyncDb(query_one_map={'post_id_a_p': 42})
    result = _run(_get_post_id(db, 'a', 'p'))
    assert result == 42
    call = db.query_one_calls[0]
    assert call['kwargs']['cache_key'] == 'post_id_a_p'
    assert call['kwargs']['cache_ttl'] == 3600


def test_get_author_hide_id_forwards_cache_params():
    """_get_author_hide_id must now be cached (was uncached before the fix)."""
    db = FakeAsyncDb()
    _run(_get_author_hide_id(db, 'alice'))
    call = db.query_one_calls[0]
    assert call['kwargs']['cache_key'] == 'author_hide_id_alice'
    assert call['kwargs']['cache_ttl'] == 300


def test_check_posts_hide_id_forwards_cache_params():
    """_check_posts_hide_id must now be cached (was uncached before the fix)."""
    db = FakeAsyncDb()
    _run(_check_posts_hide_id(db, 99))
    call = db.query_one_calls[0]
    assert call['kwargs']['cache_key'] == 'post_hide_id_99'
    assert call['kwargs']['cache_ttl'] == 300


if __name__ == '__main__':
    # Allow `python test_bridge_thread.py` style execution without pytest.
    sys.exit(pytest.main([__file__, '-v']))
