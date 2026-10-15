-- sc_trend / sc_hot recalculation normalization (shared script).
--
-- 2026-Q4 plan 02-indexer.md §五 item 3: the trend/hot scores are
-- computed at flush time (Python math.log10 vs Go math.Log10 may differ
-- in the last ULP, and the two syncs flush at different moments), so
-- md5-comparing the stored columns is meaningless as-is. Both databases
-- execute THIS IDENTICAL SCRIPT before checksumming: the scores are
-- recomputed from in-table inputs (rshares, created_at) by PostgreSQL
-- itself, which makes the stored values a deterministic function of the
-- row and therefore byte-comparable. No tolerance is used -- this is a
-- hard normalization, not an epsilon.
--
-- Formula (verified identical in both codebases, 2026-09-21 review):
--   score = sign(rshares/1e7) * log10(greatest(abs(rshares/1e7), 1))
--         + epoch(created_at) / timescale
--   with sign = -1 when rshares <= 0, and timescale 240000 (trend) /
--   10000 (hot). Sources: hive/utils/post.py:198-222 (Python) and
--   internal/indexer/scoring.go Score() (Go).
--
-- Run on BOTH databases (the same file, same PostgreSQL version class):
--   psql "$DSN" -f recalc_sc_trend.sql
--
-- The columns are float(6) (real): the numeric expression is correctly
-- rounded on assignment, and PostgreSQL's numeric->float4 conversion is
-- deterministic, so identical inputs yield identical stored bits on both
-- sides. created_at is immutable for a given post (score input), and
-- rshares is itself a strict checksum column, so any residual mismatch
-- after this script is a genuine divergence.

UPDATE hive_posts_cache SET
    sc_trend = (CASE WHEN rshares > 0 THEN 1 ELSE -1 END)
             * log(greatest(abs(rshares::numeric / 10000000.0), 1))
             + extract(epoch FROM created_at) / 240000.0,
    sc_hot   = (CASE WHEN rshares > 0 THEN 1 ELSE -1 END)
             * log(greatest(abs(rshares::numeric / 10000000.0), 1))
             + extract(epoch FROM created_at) / 10000.0;

UPDATE hive_posts_cache_temp SET
    sc_trend = (CASE WHEN rshares > 0 THEN 1 ELSE -1 END)
             * log(greatest(abs(rshares::numeric / 10000000.0), 1))
             + extract(epoch FROM created_at) / 240000.0,
    sc_hot   = (CASE WHEN rshares > 0 THEN 1 ELSE -1 END)
             * log(greatest(abs(rshares::numeric / 10000000.0), 1))
             + extract(epoch FROM created_at) / 10000.0;
