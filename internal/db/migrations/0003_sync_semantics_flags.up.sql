-- =====================================================================
-- 0003: Persisted initial-sync semantics flags on hive_state.
--
-- The Python legacy derives "initial sync in progress" from
-- hive_feed_cache emptiness (db_state.py::_is_feed_cache_empty), which is
-- fragile in two ways (PR#402 audit registration, planned PR#405):
--   1. The derivation flips the moment the tail-trio Rebuild populates
--      hive_feed_cache -- BEFORE Follow.force_recount runs. If recount
--      fails, a restart enters the normal loop and the follow counts are
--      never recomputed (stuck at 0 with no recovery path).
--   2. Any later truncation of hive_feed_cache silently re-enters the
--      initial-sync path.
--
-- Both columns are NULLable on purpose: NULL means "not yet written by
-- the Go indexer" (fresh after this migration, or a legacy-provisioned
-- database), and startup falls back to the legacy feed_cache derivation
-- before materializing a concrete value here.
--   is_initial_sync  TRUE  = initial sync in progress
--                    FALSE = initial sync completed
--   recount_done     TRUE  = the tail trio (recover/rebuild/recount)
--                            completed through ForceRecount at the
--                            current head; written right after recount
--                            succeeds so a crash between steps keeps a
--                            recovery path (restart re-runs the trio).
-- =====================================================================

ALTER TABLE hive_state ADD COLUMN IF NOT EXISTS is_initial_sync BOOLEAN;
ALTER TABLE hive_state ADD COLUMN IF NOT EXISTS recount_done BOOLEAN;
