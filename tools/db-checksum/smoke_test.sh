#!/bin/bash
# End-to-end smoke test for the db-checksum tool (planned PR#421).
#
# Builds two SCRATCH databases seeded with a few rows -- including one
# deliberate value difference, a hive_notifs id-hole pattern, and
# diverging pre-normalization score values -- then verifies:
#   1. identical databases compare clean (exit 0, "all tables match");
#   2. the injected single-row difference is found and the bisection
#      drill-down pinpoints the exact row;
#   3. hive_notifs id holes (different serial consumption, same logical
#      rows in the same order) do NOT fail the id-normalized comparison;
#   4. recalc_sc_trend.sql produces identical stored sc_trend/sc_hot
#      values on both databases even when the pre-normalization values
#      were written differently.
#
# The script creates and drops ONLY its own scratch databases
# (dbchecksum_smoke_a / dbchecksum_smoke_b). It never touches other
# databases and never stops or restarts the server. ADMIN_PSQL must point
# at an admin connection capable of CREATE/DROP DATABASE with a literal
# "-d postgres" database argument (the per-DB prefixes are derived from
# it by substitution).
#
# Usage:
#   ADMIN_PSQL="docker exec -i hivemind-test-pg psql -U test -d postgres" \
#     bash smoke_test.sh

set -euo pipefail

TOOL_DIR="$(cd "$(dirname "$0")" && pwd)"
CHECKSUM="$TOOL_DIR/db_checksum.py"
TABLES_FILE="$TOOL_DIR/tables.yaml"
RECALC_SQL="$TOOL_DIR/recalc_sc_trend.sql"

ADMIN_PSQL="${ADMIN_PSQL:-docker exec -i hivemind-test-pg psql -U test -d postgres}"
A_DB="dbchecksum_smoke_a"
B_DB="dbchecksum_smoke_b"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# Derive per-DB prefixes from the admin prefix (swap the -d database).
A_PSQL="${ADMIN_PSQL/ -d postgres/ -d $A_DB}"
B_PSQL="${ADMIN_PSQL/ -d postgres/ -d $B_DB}"

note() { echo "  $*"; }
fail() { echo "SMOKE FAILED: $*" >&2; exit 1; }

run() { # run SQL on a psql prefix; single-quoted SQL literals only
    eval "$1 -c \"$2\"" >/dev/null
}

run_file() { # feed a SQL file via stdin (works for both plain psql and
    # `docker exec -i ...` prefixes; \i would resolve inside the container)
    eval "$1" < "$2" >/dev/null
}

echo "== preparing scratch databases =="
run "$ADMIN_PSQL" "DROP DATABASE IF EXISTS $A_DB"
run "$ADMIN_PSQL" "DROP DATABASE IF EXISTS $B_DB"
run "$ADMIN_PSQL" "CREATE DATABASE $A_DB"
run "$ADMIN_PSQL" "CREATE DATABASE $B_DB"

SCHEMA="
CREATE TABLE hive_blocks (num INTEGER PRIMARY KEY, hash TEXT NOT NULL, prev TEXT NOT NULL, created_at TIMESTAMP NOT NULL);
CREATE TABLE hive_notifs (id SERIAL PRIMARY KEY, type_id SMALLINT NOT NULL, score SMALLINT NOT NULL, created_at TIMESTAMP NOT NULL, src_id INTEGER, dst_id INTEGER, post_id INTEGER, community_id INTEGER, block_num INTEGER, payload TEXT);
CREATE TABLE hive_posts_cache (post_id INTEGER PRIMARY KEY, author VARCHAR(16) NOT NULL, rshares BIGINT NOT NULL DEFAULT 0, sc_trend FLOAT(6) NOT NULL DEFAULT 0, sc_hot FLOAT(6) NOT NULL DEFAULT 0, created_at TIMESTAMP NOT NULL DEFAULT '1990-01-01');
CREATE TABLE hive_posts_cache_temp (post_id INTEGER PRIMARY KEY, author VARCHAR(16) NOT NULL, rshares BIGINT NOT NULL DEFAULT 0, sc_trend FLOAT(6) NOT NULL DEFAULT 0, sc_hot FLOAT(6) NOT NULL DEFAULT 0, created_at TIMESTAMP NOT NULL DEFAULT '1990-01-01', _synced_at TIMESTAMP);
"
run "$A_PSQL" "$SCHEMA"
run "$B_PSQL" "$SCHEMA"

# 100 blocks, identical on both sides except ONE row (block 73 hash).
SEED_BLOCKS="
INSERT INTO hive_blocks (num, hash, prev, created_at)
SELECT g, 'hash' || lpad(g::text, 5, '0'), 'prev' || lpad(g::text, 5, '0'),
       '2016-03-24'::timestamp + (g || ' minutes')::interval
FROM generate_series(1, 100) g;
"
run "$A_PSQL" "$SEED_BLOCKS"
run "$B_PSQL" "$SEED_BLOCKS"
run "$A_PSQL" "UPDATE hive_blocks SET hash = 'hash-TAMPERED' WHERE num = 73"

# notifs: same logical rows in the same order, but B consumed serial space
# before its inserts (the block-rollback hole pattern: B ids 101..150 vs
# A ids 1..50).
NOTIFS_BASE="
INSERT INTO hive_notifs (type_id, score, created_at, dst_id, block_num, payload)
SELECT 1, 10, '2016-03-24'::timestamp + (g || ' minutes')::interval, 5, g,
       'payload-' || lpad(g::text, 4, '0')
FROM generate_series(1, 50) g;
"
run "$A_PSQL" "$NOTIFS_BASE"
run "$B_PSQL" "SELECT setval('hive_notifs_id_seq', 100)"
run "$B_PSQL" "$NOTIFS_BASE"

# posts cache (+ temp mirror): same inputs, DIFFERENT pre-existing score
# values on A (simulates the flush-time drift the recalc script
# normalizes away).
POSTS_BASE="
INSERT INTO hive_posts_cache (post_id, author, rshares, created_at)
SELECT g, 'author' || lpad((g % 7)::text, 2, '0'),
       (g * 137438953471) % 50000000000,
       '2016-03-24'::timestamp + (g || ' hours')::interval
FROM generate_series(1, 40) g;
INSERT INTO hive_posts_cache_temp (post_id, author, rshares, created_at, _synced_at)
SELECT post_id, author, rshares, created_at, now() FROM hive_posts_cache;
"
run "$A_PSQL" "$POSTS_BASE"
run "$B_PSQL" "$POSTS_BASE"
run "$A_PSQL" "UPDATE hive_posts_cache SET sc_trend = sc_trend + 1.0, sc_hot = sc_hot + 2.0"
run "$B_PSQL" "UPDATE hive_posts_cache SET sc_trend = sc_trend - 5.0, sc_hot = sc_hot - 7.0"

echo "== running recalc_sc_trend.sql on both =="
run_file "$A_PSQL" "$RECALC_SQL"
run_file "$B_PSQL" "$RECALC_SQL"

SCORES_A=$(eval "$A_PSQL -t -A -c \"SELECT md5(string_agg(sc_trend::text || '/' || sc_hot::text, ',' ORDER BY post_id)) FROM hive_posts_cache\"")
SCORES_B=$(eval "$B_PSQL -t -A -c \"SELECT md5(string_agg(sc_trend::text || '/' || sc_hot::text, ',' ORDER BY post_id)) FROM hive_posts_cache\"")
if [ "$SCORES_A" = "$SCORES_B" ] && [ -n "$SCORES_A" ]; then
    note "recalc normalization: identical stored scores after the shared script (md5 ${SCORES_A:0:12})"
else
    fail "recalc script did not normalize scores ($SCORES_A vs $SCORES_B)"
fi

echo "== checksum both sides (blocks + notifs + cache + cache_temp) =="
python3 "$CHECKSUM" checksum --psql "$A_PSQL" --tables-file "$TABLES_FILE" \
    --tables hive_blocks,hive_notifs,hive_posts_cache,hive_posts_cache_temp \
    --segment-rows 10 --out "$WORK/a.json" 2>/dev/null
python3 "$CHECKSUM" checksum --psql "$B_PSQL" --tables-file "$TABLES_FILE" \
    --tables hive_blocks,hive_notifs,hive_posts_cache,hive_posts_cache_temp \
    --segment-rows 10 --out "$WORK/b.json" 2>/dev/null
note "checksum artifacts written"

echo "== compare (expect exactly the block-73 difference, drill-down included) =="
set +e
python3 "$CHECKSUM" compare \
    --left "$WORK/a.json" --right "$WORK/b.json" \
    --left-psql "$A_PSQL" --right-psql "$B_PSQL" --drilldown-rows 4 \
    > "$WORK/compare.txt" 2>&1
RC=$?
set -e
sed 's/^/  /' "$WORK/compare.txt"

[ $RC -eq 1 ] || fail "compare exit code = $RC, want 1 (differences found)"
grep -q "hive_blocks: segment 7 differs" "$WORK/compare.txt" \
    || fail "the mismatched hive_blocks segment was not reported"
grep -q "hash-TAMPERED" "$WORK/compare.txt" \
    || fail "bisection drill-down did not surface the tampered row"
if grep -q "hive_notifs" "$WORK/compare.txt"; then
    fail "hive_notifs id holes must NOT fail the normalized comparison"
fi
if grep -q "hive_posts_cache" "$WORK/compare.txt"; then
    fail "hive_posts_cache flagged; recalc normalization did not hold"
fi
note "single-row drill-down verified; notifs id-hole and score normalizations hold"

echo "== negative control: identical DBs compare clean =="
run "$A_PSQL" "UPDATE hive_blocks SET hash = 'hash' || lpad(num::text, 5, '0') WHERE num = 73"
python3 "$CHECKSUM" checksum --psql "$A_PSQL" --tables-file "$TABLES_FILE" \
    --tables hive_blocks,hive_notifs,hive_posts_cache,hive_posts_cache_temp \
    --segment-rows 10 --out "$WORK/a2.json" 2>/dev/null
set +e
python3 "$CHECKSUM" compare --left "$WORK/a2.json" --right "$WORK/b.json" \
    > "$WORK/compare2.txt" 2>&1
RC2=$?
set -e
[ $RC2 -eq 0 ] || { sed 's/^/  /' "$WORK/compare2.txt"; \
    fail "identical DBs must compare clean (rc=$RC2)"; }
grep -q "all tables match" "$WORK/compare2.txt" \
    || fail "expected 'all tables match' in clean comparison"
note "clean comparison verified"

echo "== cleaning up scratch databases =="
run "$ADMIN_PSQL" "DROP DATABASE $A_DB"
run "$ADMIN_PSQL" "DROP DATABASE $B_DB"

echo "SMOKE PASSED"
