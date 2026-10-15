#!/usr/bin/env python3
r"""Table-level checksum tool for the 20M Go-vs-Python sync comparison.

Implements the 2026-Q4 plan (02-indexer.md §五) table checksum method:
per-table, whole-table md5 over a normalized row rendering, aggregated in
fixed-size segments ordered by the table's ordering key, with row-count
verification and binary-search drill-down to the offending rows. The same
script runs against both databases (in `checksum` mode); `compare` mode
diffs the two JSON artifacts and, when database access is provided,
bisects mismatched segments down to individual rows.

All heavy lifting happens in PostgreSQL itself (via psql) so the tool has
no Python driver dependency and the md5 input is computed identically on
both sides. psql access is passed as a command prefix, e.g.:

    --psql "psql postgresql://user:pass@host:5432/db"
    --psql "docker exec -i hivemind-test-pg psql -U test -d db"   (remote)

Row normalization (the md5 input per row):
  * every non-excluded column rendered as length-prefixed text:
    `len(value::text) || ':' || value::text`, joined with '|';
  * NULL renders as the two bytes `\N` (length prefix is meaningless for
    a missing value);
  * rows joined with newline, aggregated by md5 per segment in row order.

Exclusions and per-table notes live in tables.yaml (the strict-table list
of 02 §五); see README.md for the comparison methodology and the
registered exemptions (hive_notifs id holes, sc_trend/sc_hot
normalization script, hive_state exemption, volatile columns).

Exit codes (compare mode): 0 = all tables match, 1 = differences found,
2 = operational error. checksum mode exits 2 on operational errors.
"""

from __future__ import annotations

import argparse
import datetime
import json
import shlex
import subprocess
import sys

DEFAULT_SEGMENT_ROWS = 100_000
DEFAULT_DRILLDOWN_ROWS = 8


class ToolError(Exception):
    """Operational failure (psql missing, SQL error, bad file)."""


# ---------------------------------------------------------------------------
# psql plumbing
# ---------------------------------------------------------------------------

def run_psql(prefix: str, sql: str) -> str:
    """Run one SQL statement through psql; return its stdout.

    The prefix is a shell-style command prefix (psql invocation or a
    docker exec wrapper). -X -A -t give unaligned, tuples-only output;
    ON_ERROR_STOP makes SQL errors fail the run instead of printing
    partial results."""
    cmd = shlex.split(prefix) + ["-X", "-A", "-t",
                                 "-v", "ON_ERROR_STOP=1", "-c", sql]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True,
                              check=False)
    except FileNotFoundError as exc:
        raise ToolError(f"cannot execute psql prefix {prefix!r}: {exc}") from exc
    if proc.returncode != 0:
        raise ToolError(f"psql failed (rc={proc.returncode}): "
                        f"{proc.stderr.strip()[:2000]}")
    return proc.stdout.strip()


def run_psql_json(prefix: str, sql: str):
    """Run SQL wrapped to return a JSON array of row objects."""
    wrapped = ("SELECT COALESCE(json_agg(row_to_json(q)), '[]'::json) "
               f"FROM ( {sql} ) q")
    out = run_psql(prefix, wrapped)
    if not out:
        return []
    try:
        return json.loads(out)
    except json.JSONDecodeError as exc:
        raise ToolError(f"psql returned non-JSON output: {out[:500]} "
                        f"({exc})") from exc


# ---------------------------------------------------------------------------
# SQL builders
# ---------------------------------------------------------------------------

def row_norm_expr(columns: list) -> str:
    """Length-prefixed, NULL-safe concatenation of the given columns."""
    parts = [
        f"(CASE WHEN ({col}) IS NULL THEN '\\N' "
        f"ELSE length(({col})::text) || ':' || ({col})::text END)"
        for col in columns
    ]
    return " || '|' || ".join(parts)


def order_key_sql(key_columns: list) -> str:
    return ", ".join(key_columns)


def key_array_sql(key_columns: list) -> str:
    inner = ", ".join(f"({col})::text" for col in key_columns)
    return f"ARRAY[{inner}]"


def table_columns(prefix: str, table: str) -> list:
    rows = run_psql_json(prefix, (
        "SELECT column_name FROM information_schema.columns "
        "WHERE table_schema = 'public' AND table_name = "
        f"'{table}' ORDER BY ordinal_position"))
    return [row["column_name"] for row in rows]


def segment_checksum_sql(table: str, key_columns: list, columns: list,
                         segment_rows: int) -> str:
    return (
        "SELECT (rn - 1) / {seg} AS seg, count(*)::bigint AS rows, "
        "min(array_to_string(karr, '|')) AS key_min, "
        "max(array_to_string(karr, '|')) AS key_max, "
        "md5(string_agg(row_norm, E'\\n' ORDER BY rn)) AS md5 "
        "FROM (SELECT row_number() OVER (ORDER BY {key}) AS rn, "
        "{karr} AS karr, {norm} AS row_norm FROM {table}) s "
        "GROUP BY 1 ORDER BY 1"
    ).format(seg=segment_rows, key=order_key_sql(key_columns),
             karr=key_array_sql(key_columns), norm=row_norm_expr(columns),
             table=table)


def range_checksum_sql(table: str, key_columns: list, columns: list,
                       offset: int, limit: int) -> str:
    """Checksum one ordinal window [offset, offset+limit) of the table."""
    return (
        "SELECT md5(string_agg(row_norm, E'\\n' ORDER BY {key})) AS md5 "
        "FROM (SELECT {key}, {norm} AS row_norm FROM {table} "
        "ORDER BY {key} OFFSET {off} LIMIT {lim}) s"
    ).format(key=order_key_sql(key_columns), norm=row_norm_expr(columns),
             table=table, off=offset, lim=limit)


def range_rows_sql(table: str, key_columns: list, columns: list,
                   offset: int, limit: int) -> str:
    return (
        "SELECT rn, array_to_string(karr, '|') AS key, row_norm "
        "FROM (SELECT row_number() OVER (ORDER BY {key}) AS rn, "
        "{karr} AS karr, {norm} AS row_norm FROM {table}) s "
        "WHERE rn >= {off} AND rn < {off} + {lim} ORDER BY rn"
    ).format(key=order_key_sql(key_columns), karr=key_array_sql(key_columns),
             norm=row_norm_expr(columns), table=table,
             off=offset, lim=limit)


def column_subcheck_sql(table: str, key_columns: list, column: str) -> str:
    """Whole-table single-column md5 (单列核对): attributes a mismatch to a
    designated column without a second full run."""
    return (
        "SELECT md5(string_agg(row_norm, E'\\n' ORDER BY {key})) AS md5 "
        "FROM (SELECT {key}, {norm} AS row_norm FROM {table}) s"
    ).format(key=order_key_sql(key_columns),
             norm=row_norm_expr([column]), table=table)


# ---------------------------------------------------------------------------
# checksum mode
# ---------------------------------------------------------------------------

def load_tables_file(path: str) -> dict:
    try:
        import yaml
    except ImportError as exc:  # pragma: no cover - pyyaml ships with the
        # comparator requirements
        raise ToolError("pyyaml is required for --tables-file "
                        "(see tests/api-comparison/requirements.txt)") from exc
    with open(path) as handle:
        loaded = yaml.safe_load(handle) or {}
    return loaded.get("tables") or {}


def cmd_checksum(args) -> int:
    tables_cfg = load_tables_file(args.tables_file)
    if args.tables:
        wanted = {name.strip() for name in args.tables.split(",")}
        unknown = wanted - set(tables_cfg)
        if unknown:
            raise ToolError(f"--tables names not in {args.tables_file}: "
                            f"{sorted(unknown)}")
        tables_cfg = {k: v for k, v in tables_cfg.items() if k in wanted}

    output = {
        "generated_at": datetime.datetime.now().isoformat(),
        "segment_rows": args.segment_rows,
        "tables": {},
    }

    for table, cfg in tables_cfg.items():
        key_columns = list(cfg.get("key") or [])
        if not key_columns:
            raise ToolError(f"{table}: no ordering key configured")
        excluded = set(cfg.get("exclude_columns") or [])
        columns = [c for c in table_columns(args.psql, table)
                   if c not in excluded]
        if not columns:
            raise ToolError(f"{table}: table not found or all columns "
                            f"excluded")

        row_count = run_psql_json(
            args.psql, f"SELECT count(*)::bigint AS n FROM {table}")[0]["n"]
        segments = run_psql_json(args.psql, segment_checksum_sql(
            table, key_columns, columns, args.segment_rows))
        for index, segment in enumerate(segments):
            segment["index"] = index

        entry = {
            "key": key_columns,
            "row_count": row_count,
            "columns": columns,
            "excluded_columns": sorted(excluded),
            "segments": segments,
        }

        subchecks = cfg.get("column_subchecks") or []
        if subchecks:
            entry["column_subchecks"] = {
                col: run_psql_json(args.psql, column_subcheck_sql(
                    table, key_columns, col))[0]["md5"]
                for col in subchecks
            }
        output["tables"][table] = entry
        print(f"  {table}: {row_count} rows, {len(segments)} segment(s), "
              f"{len(columns)} columns "
              f"({len(excluded)} excluded)", file=sys.stderr)

    payload = json.dumps(output, indent=2)
    if args.out and args.out != "-":
        with open(args.out, "w") as handle:
            handle.write(payload + "\n")
        print(f"checksums written: {args.out}", file=sys.stderr)
    else:
        print(payload)
    return 0


# ---------------------------------------------------------------------------
# compare mode
# ---------------------------------------------------------------------------

def load_artifact(path: str) -> dict:
    with open(path) as handle:
        artifact = json.load(handle)
    if "tables" not in artifact:
        raise ToolError(f"{path}: not a checksum artifact (no 'tables')")
    return artifact


def bisect_range(left_psql: str, right_psql: str, table: str,
                 key_columns: list, columns: list, start: int, end: int,
                 floor: int, depth: int = 0) -> list:
    """Binary-search an ordinal range whose checksums differ, down to
    `floor` rows; returns the final range for row-level diffing."""
    while end - start > floor:
        mid = start + (end - start) // 2
        left_md5 = run_psql_json(left_psql, range_checksum_sql(
            table, key_columns, columns, start, mid - start))[0]["md5"]
        right_md5 = run_psql_json(right_psql, range_checksum_sql(
            table, key_columns, columns, start, mid - start))[0]["md5"]
        if left_md5 != right_md5:
            end = mid
        else:
            # First half matches: the difference is in the second half.
            left_md5 = run_psql_json(left_psql, range_checksum_sql(
                table, key_columns, columns, mid, end - mid))[0]["md5"]
            right_md5 = run_psql_json(right_psql, range_checksum_sql(
                table, key_columns, columns, mid, end - mid))[0]["md5"]
            if left_md5 != right_md5:
                start = mid
            else:
                # Both halves match while the whole range differs: rows
                # were INSERTED/DELETED inside the range (row alignment
                # shifted). Stop; the row diff below shows the context.
                break
        depth += 1
    return start, end


def cmd_compare(args) -> int:
    left = load_artifact(args.left)
    right = load_artifact(args.right)
    if left.get("segment_rows") != right.get("segment_rows"):
        raise ToolError(
            "artifacts were produced with different --segment-rows values "
            f"({left.get('segment_rows')} vs {right.get('segment_rows')}); "
            "regenerate with matching segmentation before comparing"
        )
    left_tables = left["tables"]
    right_tables = right["tables"]
    differences = []
    drilldown = bool(args.left_psql and args.right_psql)

    for table in sorted(set(left_tables) | set(right_tables)):
        if table not in left_tables or table not in right_tables:
            differences.append(f"{table}: present in only one artifact")
            continue
        lte, rte = left_tables[table], right_tables[table]

        if lte.get("columns") != rte.get("columns"):
            only_left = sorted(set(lte["columns"]) - set(rte["columns"]))
            only_right = sorted(set(rte["columns"]) - set(lte["columns"]))
            differences.append(
                f"{table}: column set mismatch (left only {only_left}, "
                f"right only {only_right}); segments are not comparable")
            continue

        for col, lmd5 in (lte.get("column_subchecks") or {}).items():
            rmd5 = (rte.get("column_subchecks") or {}).get(col)
            if rmd5 is not None and lmd5 != rmd5:
                differences.append(f"{table}.{col}: single-column md5 "
                                   f"mismatch ({lmd5} vs {rmd5})")

        if lte["row_count"] != rte["row_count"]:
            differences.append(
                f"{table}: row count mismatch "
                f"(left={lte['row_count']}, right={rte['row_count']})")
            continue

        lsegs = {seg["index"]: seg for seg in lte["segments"]}
        rsegs = {seg["index"]: seg for seg in rte["segments"]}
        for index in sorted(set(lsegs) | set(rsegs)):
            lseg, rseg = lsegs.get(index), rsegs.get(index)
            if lseg is None or rseg is None:
                differences.append(
                    f"{table}: segment {index} present on only one side")
                continue
            if lseg["md5"] == rseg["md5"]:
                continue
            start = index * left.get("segment_rows", DEFAULT_SEGMENT_ROWS)
            end = start + int(lseg["rows"])
            message = (f"{table}: segment {index} differs "
                       f"(rows {start}..{end - 1}, left md5 "
                       f"{lseg['md5'][:12]}, right md5 {rseg['md5'][:12]})")
            if not drilldown:
                differences.append(message + " [no --left-psql/--right-psql; "
                                          "drill-down skipped]")
                continue
            lo, hi = bisect_range(args.left_psql, args.right_psql, table,
                                  lte["key"], lte["columns"], start, end,
                                  args.drilldown_rows)
            differences.append(message)
            lrows = run_psql_json(args.left_psql, range_rows_sql(
                table, lte["key"], lte["columns"], lo, hi - lo))
            rrows = run_psql_json(args.right_psql, range_rows_sql(
                table, lte["key"], lte["columns"], lo, hi - lo))
            differences.append(f"  drill-down to rows {lo}..{hi - 1}:")
            lmap = {row["rn"]: row for row in lrows}
            rmap = {row["rn"]: row for row in rrows}
            for rn in sorted(set(lmap) | set(rmap)):
                lrow, rrow = lmap.get(rn), rmap.get(rn)
                if lrow == rrow:
                    continue
                differences.append(
                    f"    row {rn} key={((lrow or rrow) or {}).get('key')}\n"
                    f"      left : {lrow and lrow['row_norm'][:400]}\n"
                    f"      right: {rrow and rrow['row_norm'][:400]}")

    if differences:
        print(f"DIFFERENCES FOUND ({len(differences)}):")
        for line in differences:
            print(f"  {line}")
        return 1
    print("all tables match")
    return 0


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main(argv=None) -> int:
    parser = argparse.ArgumentParser(
        description="20M table checksum comparison tool (02-indexer.md §五)")
    sub = parser.add_subparsers(dest="command", required=True)

    p_checksum = sub.add_parser(
        "checksum", help="compute per-table segment checksums of one DB")
    p_checksum.add_argument("--psql", required=True,
                            help="psql command prefix, e.g. "
                                 "'psql postgresql://user@host/db' or "
                                 "'docker exec -i CONTAINER psql -U user "
                                 "-d db'")
    p_checksum.add_argument("--tables-file", default=None,
                            help="tables.yaml with the strict table list")
    p_checksum.add_argument("--out", default="-",
                            help="output JSON file ('-' = stdout)")
    p_checksum.add_argument("--segment-rows", type=int,
                            default=DEFAULT_SEGMENT_ROWS,
                            help="rows per checksum segment (default %(default)s)")
    p_checksum.add_argument("--tables", default=None,
                            help="comma-separated subset of tables")
    p_checksum.set_defaults(func=cmd_checksum)

    p_compare = sub.add_parser(
        "compare", help="diff two checksum artifacts (with optional "
                        "bisection drill-down)")
    p_compare.add_argument("--left", required=True, help="left JSON artifact")
    p_compare.add_argument("--right", required=True,
                           help="right JSON artifact")
    p_compare.add_argument("--left-psql", default=None,
                           help="psql prefix of the left DB (enables "
                                "drill-down)")
    p_compare.add_argument("--right-psql", default=None,
                           help="psql prefix of the right DB")
    p_compare.add_argument("--drilldown-rows", type=int,
                           default=DEFAULT_DRILLDOWN_ROWS,
                           help="bisect mismatched segments down to this "
                                "many rows (default %(default)s)")
    p_compare.set_defaults(func=cmd_compare)

    args = parser.parse_args(argv)
    try:
        return args.func(args)
    except ToolError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
