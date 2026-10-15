# db-checksum: 20M sync comparison tooling

Tooling for the 20,000,000-block Go-vs-Python checksum comparison
(2026-Q4 plan, 02-indexer.md §五, KR3-P3). The comparison proves the Go
indexer produces the same database content as a Python fresh sync over
the same frozen steemd node; this directory provides the shared
methodology: what is compared strictly, what is normalized, what is
exempt and why.

## Contents

| File | Purpose |
|------|---------|
| `db_checksum.py` | per-table segment md5 checksums + two-DB compare with binary-search drill-down |
| `tables.yaml` | the strict table list (02 §五) with per-table keys, exclusions and single-column checks |
| `recalc_sc_trend.sql` | shared sc_trend/sc_hot normalization script (run on BOTH databases before checksumming) |
| `smoke_test.sh` | end-to-end smoke against a scratch database pair (creates and drops only its own scratch DBs) |

## Method

**Principle: only tables whose content is a pure function of the block
stream are compared strictly; everything else is normalized or exempt,
and every deviation is registered — never silently skipped.**

For each table in `tables.yaml`:

1. rows are ordered by the table's ordering key (primary key, or the
   composite UNIQUE constraint for keyless tables);
2. each row is rendered as a length-prefixed, NULL-safe text
   concatenation of its non-excluded columns
   (`len(col::text)||':'||col::text`, joined by `|`, NULL → `\N`);
3. rows are grouped into fixed-size segments (default 100k rows by
   ordinal) and each segment is reduced to
   `md5(string_agg(row_norm, '\n' ORDER BY key))`;
4. both databases produce a JSON artifact (`checksum` mode); `compare`
   mode diffs them (row counts, segment md5s, single-column checks) and,
   when given psql access to both databases, bisects each mismatched
   segment by ordinal halves down to a handful of rows and prints the
   offending rows side by side.

All computation happens inside PostgreSQL (the tool only orchestrates
psql), so the md5 inputs are computed identically on both sides.

### Strict tables

`hive_blocks`, `hive_posts`, `hive_trxid_block_num`, `hive_reblogs`,
`hive_payments`, `hive_follows`, `hive_feed_cache` (Rebuild product —
determined by follows x posts once tail maintenance finished).

### Strict with registered normalizations

| Table | Normalization | Reason |
|-------|---------------|--------|
| `hive_accounts` | exclude `rank`, `cached_at`; `followers`/`following` get dedicated single-column checks | rank and cached_at are load-time dependent; the counters depend on the follow-delta fixes and are checked separately for attributability |
| `hive_notifs` | exclude `id` from the digest; rows ordered by id, aligned by **ordinal** | block rollbacks consume serial values differently on the two syncs (#404 registration) — equal logical rows carry different ids, but the id ORDER of logical rows matches, so ordinal alignment holds |
| `hive_posts_cache` / `hive_posts_cache_temp` | exclude `sc_trend`, `sc_hot` (and `_synced_at` on the temp table); run `recalc_sc_trend.sql` on BOTH databases first, then the two columns compare strictly again | the scores are flush-time dependent (Python `math.log10` vs Go `math.Log10` may differ in the last ULP; flush moments differ 0-4 weeks). The shared script recomputes both columns from in-table inputs (`rshares`, `created_at`) with identical SQL, making them a deterministic function of the row. Hard normalization, no tolerance — md5 has no epsilon semantics |

### Exempt tables

* `hive_state` — Go's `StateRepository.Update` is never called (price
  columns stay at seed values). `head_block_num` must still match and is
  verified as a single column (see the SQL snippet in `tables.yaml`).
* `hive_communities` / `hive_roles` / `hive_subscriptions` — no
  community ops exist below block 37.5M; both sides must be EMPTY
  (consistent-empty = pass, non-empty = investigate).

### Registered divergence (report, do not "fix")

* **audit-path undelete suppresses community error notifications** on
  the Go side (deliberate: the repair tool must not fabricate history;
  #407 registration). `hive_notifs` comparisons for segments beyond
  block 37,500,000 are exempt; at the 20M checkpoint this does not bite
  (20M < 37.5M), but it must be recorded when the 30M-terminal or any
  post-37.5M comparison runs.

## Preconditions (02 §五 item 1)

* both syncs stopped at `TEST_MAX_BLOCK=20000000` with tail maintenance
  (RecoverMissingPosts / Rebuild / ForceRecount) completed;
* the steemd node still frozen at head 31,000,000 (assert
  `head_block_num` and `head_block_time` before running, record in the
  report);
* `recalc_sc_trend.sql` executed on BOTH databases immediately before
  checksumming.

## Usage

```bash
# 1. Normalize the score columns on BOTH databases (identical file):
psql "$PYTHON_DSN" -f tools/db-checksum/recalc_sc_trend.sql
psql "$GO_DSN"      -f tools/db-checksum/recalc_sc_trend.sql

# 2. Checksum both sides (any psql-accessible prefix works; the tool has
#    no Python driver dependency):
python3 tools/db-checksum/db_checksum.py checksum \
    --psql "psql $PYTHON_DSN" \
    --tables-file tools/db-checksum/tables.yaml \
    --out python-checksums.json
python3 tools/db-checksum/db_checksum.py checksum \
    --psql "psql $GO_DSN" \
    --tables-file tools/db-checksum/tables.yaml \
    --out go-checksums.json

# 3. Compare, with bisection drill-down to the differing rows:
python3 tools/db-checksum/db_checksum.py compare \
    --left python-checksums.json --right go-checksums.json \
    --left-psql "psql $PYTHON_DSN" --right-psql "psql $GO_DSN"
```

Docker-container access works the same way, e.g.
`--psql "docker exec -i hivemind-test-pg psql -U test -d mydb"`.

Exit codes (compare): `0` all tables match, `1` differences found,
`2` operational error. The comparison report (02 §五 item 4) is produced
from the compare output plus the registered exemptions above.

## Smoke test

`smoke_test.sh` builds two scratch databases with a few seeded rows —
including a deliberate value difference and a hive_notifs id-hole pattern
— and verifies: identical DBs compare clean; the injected difference is
found by bisection at the exact row; id holes do not fail the notifs
comparison; the sc_trend recalc script produces identical values from
different starting values. It creates and drops ONLY its own scratch
databases (`dbchecksum_smoke_a` / `dbchecksum_smoke_b`); point
`ADMIN_PSQL` at an admin connection (default: the shared local test
container) — it never touches other databases and never restarts the
server.

```bash
ADMIN_PSQL="docker exec -i hivemind-test-pg psql -U test -d postgres" \
  bash tools/db-checksum/smoke_test.sh
```
