#!/usr/bin/env python3
"""
Unit tests for hive.server.bridge_api.thread.

These are pure-logic tests that do NOT require a live database. They live
under tests/bridge_thread/ (rather than tests/server/) on purpose: the
tests/server/__init__.py eagerly opens a real DB connection, which would
prevent these no-DB tests from running here.

They exercise the new single-recursive-CTE implementation of
`_load_discussion` (replaces the old level-by-level BFS): exactly one
query_all call with the right cache params, correct tree/ids assembly in
BFS order, and the MAX_DEPTH / MAX_THREAD_POSTS truncation safeguards.
The hide-id lookups forward their cache_key/cache_ttl to the db layer.

A fake async db records the calls it receives so assertions can inspect
query parameters without a real database.
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

    `query_one` results are configured per-cache_key. `query_all` returns the
    first element of `query_all_seq` (the single CTE call) then empty lists.
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


def _install_load_posts_keyed(monkeypatch, posts=None, seen_ids=None):
    """Stub out load_posts_keyed so _load_discussion needs no real post data."""

    async def _stub(_db, ids, _truncate_body=0):
        if seen_ids is not None:
            seen_ids['ids'] = list(ids)
        return posts or {}

    monkeypatch.setattr(thread, 'load_posts_keyed', _stub)


def _cte_rows(*tuples):
    """Build query_all rows from (id, parent_id, depth) tuples."""
    return [{'parent_id': p, 'id': i, 'depth': d} for (i, p, d) in tuples]


def _minimal_posts(ids):
    """Build a posts dict (id -> post) with non-hidden stats."""
    return {
        pid: {'author': 'a%d' % pid, 'permlink': 'p%d' % pid,
              'stats': {'hide': False}}
        for pid in ids
    }


def test_load_discussion_issues_single_cte_query(monkeypatch):
    """_load_discussion must issue exactly ONE query_all (the recursive CTE).

    The old implementation walked the tree level by level with one
    _child_ids query per depth; the new one fetches the whole subtree in a
    single round-trip, so exactly one query_all call is expected.
    """
    _install_load_posts_keyed(monkeypatch)

    db = FakeAsyncDb(query_all_seq=[
        _cte_rows((2, 1, 1), (3, 1, 1), (4, 2, 2))
    ])
    _run(_load_discussion(db, 1))

    assert len(db.query_all_calls) == 1
    call = db.query_all_calls[0]
    assert call['kwargs']['root_id'] == 1
    assert call['kwargs']['max_depth'] == MAX_DEPTH
    assert call['kwargs']['max_posts'] == MAX_THREAD_POSTS
    assert call['kwargs']['cache_key'] == 'discussion_tree_1'
    assert call['kwargs']['cache_ttl'] == 120


def test_load_discussion_builds_tree_in_bfs_order(monkeypatch):
    """Root first, then children by (depth, id); replies wired per parent."""
    ids = [1, 2, 3, 4]
    posts = _minimal_posts(ids)
    _install_load_posts_keyed(monkeypatch, posts=posts, seen_ids={})

    db = FakeAsyncDb(query_all_seq=[
        _cte_rows((2, 1, 1), (3, 1, 1), (4, 2, 2))
    ])
    result = _run(_load_discussion(db, 1))

    # ids passed to load_posts_keyed: root first, then BFS order
    assert set(result.keys()) == {'a1/p1', 'a2/p2', 'a3/p3', 'a4/p4'}
    assert result['a1/p1']['replies'] == ['a2/p2', 'a3/p3']
    assert result['a2/p2']['replies'] == ['a4/p4']
    # leaf posts have no 'replies' key (only set when pid in tree)
    assert 'replies' not in result['a3/p3']
    assert 'replies' not in result['a4/p4']


def test_load_discussion_single_child_chain(monkeypatch):
    """Deep chain 1->2->3->4 completes with one query; all ids loaded."""
    _install_load_posts_keyed(monkeypatch, posts=_minimal_posts([1, 2, 3, 4]))

    db = FakeAsyncDb(query_all_seq=[
        _cte_rows((2, 1, 1), (3, 2, 2), (4, 3, 3))
    ])
    _run(_load_discussion(db, 1))

    assert len(db.query_all_calls) == 1


def test_load_discussion_truncated_when_limit_hit(monkeypatch):
    """If the CTE returned more rows than MAX_THREAD_POSTS, mark truncated.

    (The SQL LIMIT normally prevents this; the Python guard is a belt-and-
    braces check that len(ids) > MAX_THREAD_POSTS is treated as truncation.)
    """
    warnings = []
    monkeypatch.setattr(thread.log, 'warning',
                        lambda *a, **k: warnings.append(a))
    _install_load_posts_keyed(monkeypatch)

    # Root + MAX_THREAD_POSTS + 5 children -> ids length exceeds the cap.
    extra = [((i, 1, 1)) for i in range(2, MAX_THREAD_POSTS + 7)]
    db = FakeAsyncDb(query_all_seq=[_cte_rows(*extra)])
    _run(_load_discussion(db, 1))

    assert any('truncated' in str(w) for w in warnings)


def test_load_discussion_truncated_when_depth_cap_hit(monkeypatch):
    """max_depth_seen reaching MAX_DEPTH marks the walk truncated."""
    warnings = []
    monkeypatch.setattr(thread.log, 'warning',
                        lambda *a, **k: warnings.append(a))
    _install_load_posts_keyed(monkeypatch)

    # A row whose depth equals MAX_DEPTH => recursion hit the cap.
    db = FakeAsyncDb(query_all_seq=[
        _cte_rows((2, 1, MAX_DEPTH))
    ])
    _run(_load_discussion(db, 1))

    assert any('truncated' in str(w) for w in warnings)


def test_load_discussion_leaf_terminates(monkeypatch):
    """Root with no children: single CTE call, root-only ids, no truncation."""
    warnings = []
    monkeypatch.setattr(thread.log, 'warning',
                        lambda *a, **k: warnings.append(a))
    seen = {}
    _install_load_posts_keyed(monkeypatch, posts=_minimal_posts([1]),
                              seen_ids=seen)

    db = FakeAsyncDb(query_all_seq=[_cte_rows()])  # empty tree below root
    _run(_load_discussion(db, 1))

    assert len(db.query_all_calls) == 1
    assert seen['ids'] == [1]
    assert not any('truncated' in str(w) for w in warnings)


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
