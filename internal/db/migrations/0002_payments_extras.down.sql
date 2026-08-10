-- =====================================================================
-- 0002 down: remove the Go-indexer-only extras from hive_payments.
-- =====================================================================

ALTER TABLE hive_payments DROP COLUMN IF EXISTS created_at;
ALTER TABLE hive_payments DROP COLUMN IF EXISTS memo;
