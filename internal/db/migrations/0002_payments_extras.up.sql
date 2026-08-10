-- =====================================================================
-- 0002: Add memo and created_at to hive_payments.
--
-- The legacy Python hive_payments schema (schema.py) does not define these
-- columns, but the Go payment_indexer writes them (memo is parsed from the
-- transfer op; created_at is the block date). Rather than discard data the
-- indexer already produces, add them as nullable/loosely-typed extras so the
-- Go model, indexer, and DB schema stay consistent.
--
-- These are intentionally additive and do not touch any legacy column.
-- =====================================================================

ALTER TABLE hive_payments ADD COLUMN IF NOT EXISTS memo VARCHAR(1024);
ALTER TABLE hive_payments ADD COLUMN IF NOT EXISTS created_at TIMESTAMP WITHOUT TIME ZONE NOT NULL DEFAULT '1970-01-01 00:00:00';
