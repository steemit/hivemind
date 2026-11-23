"""Tests for DbState index bookkeeping which need no database connection."""

from hive.db.db_state import DbState
from hive.db.schema import build_metadata


def test_disableable_indexes_exist_in_schema():
    """Every index dropped before initial sync must exist in the schema.

    _disableable_indexes() is called by _before_initial_sync() right after a
    fresh database is set up, and again by _after_initial_sync() to rebuild
    the indexes. Both rely on each listed index being defined in the schema
    metadata; the method asserts when one is missing.

    Regression test: #367 removed hive_posts_cache_ix31 from the schema but
    left it in the list, so `hive sync` on a fresh database crashed in
    _before_initial_sync() with "indexes not located" before syncing any
    block. Databases with an already loaded schema skip the hook, which is
    why production was not affected.
    """
    schema_indexes = set()
    for table in build_metadata().tables.values():
        for index in table.indexes:
            schema_indexes.add(index.name)

    disableable = DbState._disableable_indexes()

    assert disableable
    for index in disableable:
        assert index.name in schema_indexes
