-- =====================================================================
-- 0003 down: drop the persisted initial-sync semantics flags.
-- =====================================================================

ALTER TABLE hive_state DROP COLUMN IF EXISTS is_initial_sync;
ALTER TABLE hive_state DROP COLUMN IF EXISTS recount_done;
