#!/usr/bin/env python3
"""Hivemind API response comparator.

Compares JSON-RPC responses of the legacy Python hivemind server against the
Go rewrite and produces console/JSON/HTML reports.

Test cases are declared in a YAML file (see test_cases.yaml). The file has:

  config:                     # global options, applied to every case
    ignore_fields: [...]      # path-aware ignore patterns (see IgnoreMatcher)
    tolerance:
      float_epsilon: 0.001    # numeric comparison epsilon
      timestamp_seconds: 2    # timestamp comparison tolerance (seconds)
      timestamp_fields: [...] # field-name tokens that mark epoch numbers as
                              # timestamps (see DEFAULT_TIMESTAMP_TOKENS)
  test_cases:
    - id: some-slug           # stable id, used for detail file names
      name: ...               # human readable description
      api: condenser_api.get_content
      priority: P0            # P0/P1/P2, used by --priority filter
      params: [...]           # request params (legacy protocol wire form)
      ignore_fields: [...]    # per-case override; REPLACES the global list
      tolerance: {...}        # per-case override; merged key-wise over global
      unordered: true         # sort top-level list by `unordered_key` first
      unordered_key: name     # dotted path into elements (default: full JSON)
      lenient_list: true      # top-level lists: same length + order-insensitive
                              # one-to-one element matching

Comparison semantics:
  * Numbers compare within float_epsilon.
  * Strings that both parse as ISO8601 timestamps (with/without Z, with
    offset, naive, space separator, fractional seconds) compare within
    timestamp_seconds after normalization to epoch seconds.
  * Numbers at timestamp-ish field paths (e.g. created_at, db_head_age)
    compare within timestamp_seconds (epoch seconds; values > 1e12 are
    treated as epoch milliseconds).
  * Amount strings ("0.000 SBD", "$1.23") compare numerically within
    float_epsilon after parse_amount normalization.
  * ignore_fields patterns are path-aware; a bare name matches that dict key
    at any depth, a dotted pattern matches only the exact path. This keeps a
    global ignore such as `author.id` from also masking a top-level `id`.

`requests` is imported lazily so the pure comparison functions can be unit
tested (test_api_comparator.py) without the HTTP dependency installed.
"""

from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any, Optional

import yaml

DEFAULT_FLOAT_EPSILON = 0.001
DEFAULT_TIMESTAMP_SECONDS = 2.0
REPORT_FIELD_LIMIT = 200

# Field-name tokens that mark a numeric field as a timestamp. A field is
# time-ish when one of its underscore-separated path tokens is in this set
# (or in tolerance.timestamp_fields).
DEFAULT_TIMESTAMP_TOKENS = frozenset({
    "time", "created", "updated", "update", "cashout", "age", "date",
    "expires", "expiration", "reblogged", "read",
})

# ISO8601-ish: date-only, or date + [T or space] time (+ optional seconds,
# fraction, Z or numeric offset).
_ISO_TS_RE = re.compile(
    r"^\d{4}-\d{2}-\d{2}"
    r"([T ]\d{2}:\d{2}(:\d{2}(\.\d{1,9})?)?(Z|[+-]\d{2}:?\d{2})?)?$"
)

# Amount string: optional $, number, mandatory currency (3-8 letters) so that
# ordinary strings are never misread as amounts.
_AMOUNT_RE = re.compile(r"^(-?\d+(?:\.\d+)?)\s*([A-Za-z$]{2,8})$")
_DOLLAR_AMOUNT_RE = re.compile(r"^\$\s*(-?\d+(?:\.\d+)?)$")


class ApiCallError(Exception):
    """Raised when an endpoint returns a JSON-RPC error response."""

    def __init__(self, message: str, code: Optional[int] = None):
        super().__init__(message)
        self.message = message
        self.code = code


# ---------------------------------------------------------------------------
# Normalizers (pure functions; unit tested)
# ---------------------------------------------------------------------------

def normalize_timestamp(value: Any, numeric: bool = False) -> Optional[float]:
    """Normalize a timestamp-like value to epoch seconds, or None.

    Recognizes ISO8601 variants: with/without Z, with UTC offset, naive
    (assumed UTC), space or T separator, optional fractional seconds, and
    date-only strings. Numeric epochs are only honored when `numeric` is
    true (callers gate this on time-ish field names so ordinary ids are
    never mistaken for timestamps). Values above 1e12 are treated as epoch
    milliseconds. Plain digit strings of 10/13 chars are treated as epochs.
    """
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, float)):
        if not numeric:
            return None
        return _epoch_to_seconds(float(value))
    if isinstance(value, str):
        text = value.strip()
        if _ISO_TS_RE.match(text):
            if text.endswith("Z"):
                text = text[:-1] + "+00:00"
            try:
                dt = datetime.fromisoformat(text)
            except ValueError:
                return None
            if dt.tzinfo is None:
                dt = dt.replace(tzinfo=timezone.utc)
            return dt.timestamp()
        if text.isdigit() and len(text) in (10, 13):
            return _epoch_to_seconds(float(text))
    return None


def _epoch_to_seconds(epoch: float) -> float:
    return epoch / 1000.0 if epoch > 1e12 else epoch


def parse_amount(value: Any) -> Optional[float]:
    """Parse "123.456 SBD" / "$1.23" style amount strings into a float.

    Returns None for anything that is not a currency-marked amount string
    (a bare numeric string stays a string; use numbers to express numbers).
    """
    if isinstance(value, bool) or not isinstance(value, str):
        return None
    text = value.strip()
    match = _DOLLAR_AMOUNT_RE.match(text)
    if match:
        return float(match.group(1))
    match = _AMOUNT_RE.match(text)
    if match:
        return float(match.group(1))
    return None


def is_timestamp_field(path: tuple, extra_fields=()) -> bool:
    """True when the path's field name marks a numeric value as a timestamp."""
    if not path:
        return False
    name = str(path[-1])
    tokens = set(name.replace("-", "_").split("_"))
    return bool(tokens & DEFAULT_TIMESTAMP_TOKENS) or name in set(extra_fields)


# ---------------------------------------------------------------------------
# Path-aware ignore matching
# ---------------------------------------------------------------------------

class IgnoreMatcher:
    """Path-aware ignore-field matcher.

    Pattern syntax (dot-separated segments):
      * "name"  -- matches a dict key `name` at any depth (legacy deep match;
                   use sparingly, it masks same-named business fields);
      * "a.b.c" -- matches only the exact path <root> a -> b -> c;
      * "*"     -- matches any single segment (dict key or list index);
      * "**"    -- matches zero or more segments.

    Compared paths use actual dict keys; list indices are normalized to "*"
    so a pattern like "items.*.id" covers every list element.
    """

    def __init__(self, patterns=None):
        self.deep_names = set()
        self.patterns = []
        for pattern in patterns or []:
            pattern = str(pattern).strip()
            if not pattern:
                continue
            if "." not in pattern and "*" not in pattern:
                self.deep_names.add(pattern)
            else:
                self.patterns.append(pattern.split("."))

    def matches(self, path: tuple) -> bool:
        if not path:
            return False
        if str(path[-1]) in self.deep_names:
            return True
        segments = [str(seg) for seg in path]
        for pattern in self.patterns:
            if self._match_segments(pattern, segments):
                return True
        return False

    @classmethod
    def _match_segments(cls, pattern: list, path: list) -> bool:
        if not pattern:
            return not path
        head, rest = pattern[0], pattern[1:]
        if head == "**":
            return any(
                cls._match_segments(rest, path[skip:])
                for skip in range(len(path) + 1)
            )
        if not path:
            return False
        if head == "*" or head == path[0]:
            return cls._match_segments(rest, path[1:])
        return False


# ---------------------------------------------------------------------------
# Comparison core (pure functions; unit tested)
# ---------------------------------------------------------------------------

@dataclass
class CompareOptions:
    """Per-case comparison options (global config merged with case config)."""
    ignore: IgnoreMatcher = field(default_factory=IgnoreMatcher)
    float_epsilon: float = DEFAULT_FLOAT_EPSILON
    timestamp_seconds: float = DEFAULT_TIMESTAMP_SECONDS
    timestamp_fields: tuple = ()
    unordered: bool = False
    unordered_key: Optional[str] = None
    lenient_list: bool = False


def canonical_json(value: Any) -> str:
    """Stable JSON rendering used for sort keys."""
    return json.dumps(value, sort_keys=True, separators=(",", ":"), default=str)


def _fmt_path(path: tuple) -> str:
    rendered = ""
    for seg in path:
        rendered += f"[{seg}]" if seg == "*" else (f".{seg}" if rendered else str(seg))
    return rendered or "<root>"


def compare_values(a: Any, b: Any, opts: CompareOptions,
                   path: tuple = ()) -> tuple[bool, Optional[str]]:
    """Compare two decoded JSON values. Returns (matched, difference)."""
    where = _fmt_path(path)

    if a is None and b is None:
        return True, None
    if a is None or b is None:
        return False, f"{where}: one side is null (python={a!r}, go={b!r})"

    # Booleans first: bool is an int subclass in Python, so True must never
    # compare equal to 1.
    if isinstance(a, bool) or isinstance(b, bool):
        if isinstance(a, bool) and isinstance(b, bool) and a == b:
            return True, None
        return False, f"{where}: bool mismatch (python={a!r}, go={b!r})"

    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return _compare_numbers(a, b, opts, path, where)

    if isinstance(a, str) and isinstance(b, str):
        return _compare_strings(a, b, opts, path, where)

    # Cross-type: allow amount string vs number ("1.5 SBD" vs 1.5).
    if isinstance(a, str) != isinstance(b, str):
        num_a = parse_amount(a) if isinstance(a, str) else a
        num_b = parse_amount(b) if isinstance(b, str) else b
        if num_a is not None and num_b is not None:
            ok, _ = _compare_numbers(num_a, num_b, opts, path, where)
            if ok:
                return True, None
        return False, (
            f"{where}: type mismatch "
            f"(python={type(a).__name__} {a!r}, go={type(b).__name__} {b!r})"
        )

    if isinstance(a, list) and isinstance(b, list):
        return _compare_lists(a, b, opts, path, where)

    if isinstance(a, dict) and isinstance(b, dict):
        return _compare_dicts(a, b, opts, path, where)

    return False, f"{where}: unsupported types (python={type(a).__name__}, go={type(b).__name__})"


def _compare_numbers(a: float, b: float, opts: CompareOptions,
                     path: tuple, where: str) -> tuple[bool, Optional[str]]:
    if is_timestamp_field(path, opts.timestamp_fields):
        ts_a = normalize_timestamp(a, numeric=True)
        ts_b = normalize_timestamp(b, numeric=True)
        diff = abs(ts_a - ts_b)
        if diff <= opts.timestamp_seconds:
            return True, None
        return False, (
            f"{where}: timestamp mismatch: python={a}, go={b}, "
            f"delta={diff}s > tolerance={opts.timestamp_seconds}s"
        )
    diff = abs(a - b)
    if diff <= opts.float_epsilon:
        return True, None
    return False, (
        f"{where}: number mismatch: python={a}, go={b}, "
        f"delta={diff} > epsilon={opts.float_epsilon}"
    )


def _compare_strings(a: str, b: str, opts: CompareOptions,
                     path: tuple, where: str) -> tuple[bool, Optional[str]]:
    # Timestamp-aware: whenever both sides parse as timestamps, compare by
    # normalized epoch distance regardless of formatting.
    ts_a = normalize_timestamp(a)
    ts_b = normalize_timestamp(b)
    if ts_a is not None and ts_b is not None:
        diff = abs(ts_a - ts_b)
        if diff <= opts.timestamp_seconds:
            return True, None
        return False, (
            f"{where}: timestamp mismatch: python={a!r} ({ts_a}), "
            f"go={b!r} ({ts_b}), delta={diff:.3f}s > "
            f"tolerance={opts.timestamp_seconds}s"
        )

    # Amount-aware: "1.500 SBD" vs "1.5 SBD" compares numerically.
    amount_a = parse_amount(a)
    amount_b = parse_amount(b)
    if amount_a is not None and amount_b is not None:
        diff = abs(amount_a - amount_b)
        if diff <= opts.float_epsilon:
            return True, None
        return False, (
            f"{where}: amount mismatch: python={a!r}, go={b!r}, "
            f"delta={diff} > epsilon={opts.float_epsilon}"
        )

    if a == b:
        return True, None
    return False, f"{where}: string mismatch: python={a!r}, go={b!r}"


def _extract_sort_key(element: Any, key_path: Optional[str]) -> str:
    """Extract a business sort key from a list element (dotted path)."""
    if not key_path:
        return canonical_json(element)
    current = element
    for segment in key_path.split("."):
        if isinstance(current, dict) and segment in current:
            current = current[segment]
        else:
            return "\x00missing"  # sort unfindable keys first, deterministically
    return canonical_json(current)


def _compare_lists(a: list, b: list, opts: CompareOptions,
                   path: tuple, where: str) -> tuple[bool, Optional[str]]:
    if len(a) != len(b):
        return False, (
            f"{where}: list length mismatch (python={len(a)}, go={len(b)})"
        )

    # unordered / lenient_list only apply to the top-level response list so
    # that nested list ordering still compares strictly.
    top_level = len(path) == 0
    if top_level and opts.unordered:
        key = opts.unordered_key
        a_sorted = sorted(a, key=lambda item: _extract_sort_key(item, key))
        b_sorted = sorted(b, key=lambda item: _extract_sort_key(item, key))
        for item_a, item_b in zip(a_sorted, b_sorted):
            matched, diff = compare_values(item_a, item_b, opts, ("*",))
            if not matched:
                return False, (
                    f"{where}: unordered list mismatch after sort by "
                    f"{key or 'canonical json'}: {diff}"
                )
        return True, None

    if top_level and opts.lenient_list:
        used = [False] * len(b)
        for item in a:
            found = None
            for idx, candidate in enumerate(b):
                if used[idx]:
                    continue
                ok, _ = compare_values(item, candidate, opts, ("*",))
                if ok:
                    found = idx
                    break
            if found is None:
                return False, (
                    f"{where}: lenient list: python element has no go match: "
                    f"{canonical_json(item)[:REPORT_FIELD_LIMIT]}"
                )
            used[found] = True
        return True, None

    for idx, (item_a, item_b) in enumerate(zip(a, b)):
        matched, diff = compare_values(item_a, item_b, opts, path + ("*",))
        if not matched:
            return False, diff
    return True, None


def _compare_dicts(a: dict, b: dict, opts: CompareOptions,
                   path: tuple, where: str) -> tuple[bool, Optional[str]]:
    keys_a = {k for k in a if not opts.ignore.matches(path + (k,))}
    keys_b = {k for k in b if not opts.ignore.matches(path + (k,))}

    if keys_a != keys_b:
        only_a = sorted(keys_a - keys_b)
        only_b = sorted(keys_b - keys_a)
        return False, (
            f"{where}: key mismatch (python only: {only_a}, go only: {only_b})"
        )

    for key in sorted(keys_a):
        matched, diff = compare_values(a[key], b[key], opts, path + (key,))
        if not matched:
            return False, diff
    return True, None


def compare_error_texts(err_python: str, err_go: str) -> tuple[bool, Optional[str]]:
    """Compare JSON-RPC error texts (both-sides-error dummy methods)."""
    if err_python.strip() == err_go.strip():
        return True, None
    return False, f"error text mismatch: python={err_python!r}, go={err_go!r}"


# ---------------------------------------------------------------------------
# Report slimming
# ---------------------------------------------------------------------------

def truncate_for_report(value: Any, limit: int = REPORT_FIELD_LIMIT) -> Any:
    """Recursively truncate long strings so reports stay readable.

    Full untruncated responses are written to per-case detail files instead.
    """
    if isinstance(value, str):
        if len(value) <= limit:
            return value
        return value[:limit] + f"...[truncated {len(value) - limit} chars]"
    if isinstance(value, list):
        return [truncate_for_report(item, limit) for item in value]
    if isinstance(value, dict):
        return {key: truncate_for_report(item, limit) for key, item in value.items()}
    return value


# ---------------------------------------------------------------------------
# HTTP client
# ---------------------------------------------------------------------------

class APIClient:
    """Minimal JSON-RPC HTTP client."""

    def __init__(self, base_url: str, timeout: int = 30):
        self.base_url = base_url
        self.timeout = timeout
        import requests  # lazy: lets comparison core run without the dep

        self.session = requests.Session()
        self.session.headers.update({"Content-Type": "application/json"})

    def call(self, method: str, params: Any = None) -> Any:
        payload = {
            "jsonrpc": "2.0",
            "id": 1,
            "method": method,
            "params": params if params is not None else [],
        }
        try:
            response = self.session.post(self.base_url, json=payload,
                                         timeout=self.timeout)
            response.raise_for_status()
            data = response.json()
        except Exception as exc:  # requests errors + JSON decode errors
            raise ApiCallError(f"request failed: {exc}") from exc

        if isinstance(data, dict) and "error" in data and data["error"]:
            error = data["error"]
            message = error.get("message", str(error)) if isinstance(error, dict) else str(error)
            raise ApiCallError(message, code=error.get("code") if isinstance(error, dict) else None)
        if isinstance(data, dict):
            return data.get("result", data)
        return data


# ---------------------------------------------------------------------------
# Test runner
# ---------------------------------------------------------------------------

@dataclass
class CaseResult:
    """Outcome of a single comparison case."""
    case_id: str
    name: str
    api: str
    priority: str
    passed: bool
    python_response: Any = None
    go_response: Any = None
    python_error: Optional[str] = None
    go_error: Optional[str] = None
    difference: Optional[str] = None
    error: Optional[str] = None
    duration_ms: int = 0
    detail_file: Optional[str] = None

    def to_report_dict(self) -> dict:
        return {
            "id": self.case_id,
            "name": self.name,
            "api": self.api,
            "priority": self.priority,
            "passed": self.passed,
            "difference": self.difference,
            "error": self.error,
            "python_error": self.python_error,
            "go_error": self.go_error,
            "python_response": truncate_for_report(self.python_response),
            "go_response": truncate_for_report(self.go_response),
            "duration_ms": self.duration_ms,
            "detail_file": self.detail_file,
        }


def options_for_case(global_config: dict, case: dict) -> CompareOptions:
    """Merge global config with per-case overrides.

    Per-case ignore_fields REPLACE the global list (explicit override);
    per-case tolerance merges key-wise over the global tolerance.
    """
    tolerance = dict(global_config.get("tolerance") or {})
    tolerance.update(case.get("tolerance") or {})

    ignore = case.get("ignore_fields")
    if ignore is None:
        ignore = global_config.get("ignore_fields") or []

    ts_fields = tolerance.get("timestamp_fields") or ()
    if isinstance(ts_fields, str):
        ts_fields = (ts_fields,)

    return CompareOptions(
        ignore=IgnoreMatcher(ignore),
        float_epsilon=float(tolerance.get("float_epsilon", DEFAULT_FLOAT_EPSILON)),
        timestamp_seconds=float(tolerance.get("timestamp_seconds", DEFAULT_TIMESTAMP_SECONDS)),
        timestamp_fields=tuple(ts_fields),
        unordered=bool(case.get("unordered", False)),
        unordered_key=case.get("unordered_key"),
        lenient_list=bool(case.get("lenient_list", False)),
    )


def filter_cases_by_priority(test_cases: list, priority: Optional[str]) -> list:
    """Keep cases whose priority is in the comma-separated priority filter."""
    if not priority:
        return test_cases
    wanted = {part.strip().upper() for part in priority.split(",") if part.strip()}
    return [case for case in test_cases
            if str(case.get("priority", "")).upper() in wanted]


class APITestRunner:
    """Runs comparison cases against two endpoints."""

    def __init__(self, python_url: str, go_url: str, timeout: int = 30,
                 output_dir: str = "results"):
        self.python_client = APIClient(python_url, timeout)
        self.go_client = APIClient(go_url, timeout)
        self.output_dir = output_dir
        self.results: list[CaseResult] = []

    def run_case(self, case: dict, global_config: dict) -> CaseResult:
        case_id = str(case.get("id") or case.get("name") or case.get("api") or "case")
        api_method = case["api"]
        params = case.get("params")
        opts = options_for_case(global_config, case)

        started = datetime.now()
        python_response = go_response = None
        python_error = go_error = difference = error = None
        passed = False

        try:
            try:
                python_response = self.python_client.call(api_method, params)
            except ApiCallError as exc:
                python_error = exc.message

            try:
                go_response = self.go_client.call(api_method, params)
            except ApiCallError as exc:
                go_error = exc.message

            if python_error is not None and go_error is not None:
                # Both endpoints errored: for dummy methods the interesting
                # comparison is the error text itself.
                passed, difference = compare_error_texts(python_error, go_error)
                if passed:
                    difference = None
            elif python_error is not None or go_error is not None:
                passed = False
                difference = (f"one side errored "
                              f"(python={python_error!r}, go={go_error!r})")
            else:
                passed, difference = compare_values(python_response, go_response, opts)
                if passed:
                    difference = None
        except Exception as exc:  # defensive: keep running remaining cases
            error = f"unexpected comparator failure: {exc}"

        duration_ms = int((datetime.now() - started).total_seconds() * 1000)
        result = CaseResult(
            case_id=case_id,
            name=str(case.get("name", case_id)),
            api=api_method,
            priority=str(case.get("priority", "")),
            passed=passed,
            python_response=python_response,
            go_response=go_response,
            python_error=python_error,
            go_error=go_error,
            difference=difference,
            error=error,
            duration_ms=duration_ms,
        )
        result.detail_file = self._write_detail_file(result)
        self.results.append(result)
        return result

    def _write_detail_file(self, result: CaseResult) -> Optional[str]:
        import os

        try:
            os.makedirs(self.output_dir, exist_ok=True)
            filename = f"case-{result.case_id}.json"
            path = os.path.join(self.output_dir, filename)
            payload = {
                "id": result.case_id,
                "name": result.name,
                "api": result.api,
                "priority": result.priority,
                "passed": result.passed,
                "difference": result.difference,
                "error": result.error,
                "python_error": result.python_error,
                "go_error": result.go_error,
                "duration_ms": result.duration_ms,
                # Full, untruncated responses:
                "python_response": result.python_response,
                "go_response": result.go_response,
            }
            with open(path, "w") as handle:
                json.dump(payload, handle, indent=2, ensure_ascii=False, default=str)
            return filename
        except OSError as exc:
            return f"<detail file write failed: {exc}>"

    def run_from_config(self, config_file: str,
                        priority: Optional[str] = None) -> list[CaseResult]:
        with open(config_file) as handle:
            loaded = yaml.safe_load(handle) or {}

        global_config = loaded.get("config") or {}
        test_cases = filter_cases_by_priority(loaded.get("test_cases") or [], priority)

        for case in test_cases:
            self.run_case(case, global_config)
        return self.results

    def get_summary(self) -> dict:
        total = len(self.results)
        passed = sum(1 for result in self.results if result.passed)
        by_priority = {}
        for result in self.results:
            bucket = by_priority.setdefault(result.priority or "-", {"total": 0, "passed": 0})
            bucket["total"] += 1
            if result.passed:
                bucket["passed"] += 1
        return {
            "total": total,
            "passed": passed,
            "failed": total - passed,
            "pass_rate": f"{(passed / total * 100):.1f}%" if total else "0%",
            "total_duration_ms": sum(result.duration_ms for result in self.results),
            "by_priority": by_priority,
        }


# ---------------------------------------------------------------------------
# Reports
# ---------------------------------------------------------------------------

class ReportGenerator:
    """Console/JSON/HTML report generation (responses are truncated)."""

    def __init__(self, results: list[CaseResult], summary: dict):
        self.results = results
        self.summary = summary

    def generate_console_report(self) -> str:
        lines = [
            "=" * 62,
            "Hivemind API consistency report",
            "=" * 62,
            "",
            "Summary:",
            f"  total:    {self.summary['total']}",
            f"  passed:   {self.summary['passed']}",
            f"  failed:   {self.summary['failed']}",
            f"  pass rate:{self.summary['pass_rate']}",
            f"  duration: {self.summary['total_duration_ms']} ms",
        ]
        for name in sorted(self.summary.get("by_priority", {})):
            bucket = self.summary["by_priority"][name]
            lines.append(
                f"  {name}: {bucket['passed']}/{bucket['total']} passed"
            )
        lines.append("")
        if self.summary["failed"] == 0:
            lines.append("All cases passed.")
            return "\n".join(lines)

        lines.extend(["Failed cases:", "-" * 62])
        for result in self.results:
            if result.passed:
                continue
            lines.append(f"  - [{result.priority}] {result.case_id} "
                         f"({result.api})")
            if result.error:
                lines.append(f"      error: {result.error}")
            if result.difference:
                lines.append(f"      diff:  {result.difference}")
            lines.append(f"      detail: {result.detail_file}")
        return "\n".join(lines)

    def generate_json_report(self, output_file: str):
        report = {
            "timestamp": datetime.now().isoformat(),
            "summary": self.summary,
            "results": [result.to_report_dict() for result in self.results],
        }
        with open(output_file, "w") as handle:
            json.dump(report, handle, indent=2, ensure_ascii=False, default=str)

    def generate_html_report(self, output_file: str):
        rows = []
        for result in self.results:
            status = "PASS" if result.passed else "FAIL"
            css = "result-passed" if result.passed else "result-failed"
            diff = result.difference or result.error or "-"
            detail = result.detail_file or "-"
            rows.append(
                "<tr class='{css}'><td>{priority}</td><td>{api}</td>"
                "<td>{case_id}</td><td>{status}</td><td>{duration} ms</td>"
                "<td class='diff'>{diff}</td><td>{detail}</td></tr>".format(
                    css=css, priority=result.priority, api=result.api,
                    case_id=result.case_id, status=status,
                    duration=result.duration_ms, diff=diff, detail=detail,
                )
            )
        priority_rows = "".join(
            f"<p>{name}: {bucket['passed']}/{bucket['total']} passed</p>"
            for name, bucket in sorted(self.summary.get("by_priority", {}).items())
        )
        html = f"""<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <title>Hivemind API consistency report</title>
    <style>
        body {{ font-family: Arial, sans-serif; margin: 20px; }}
        .summary {{ background: #f0f0f0; padding: 15px; border-radius: 5px; margin-bottom: 20px; }}
        .passed {{ color: green; }}
        .failed {{ color: red; }}
        table {{ border-collapse: collapse; width: 100%; }}
        th, td {{ border: 1px solid #ddd; padding: 8px; text-align: left; }}
        th {{ background-color: #4CAF50; color: white; }}
        tr:nth-child(even) {{ background-color: #f2f2f2; }}
        .result-passed {{ background-color: #d4edda; }}
        .result-failed {{ background-color: #f8d7da; }}
        .diff {{ white-space: pre-wrap; font-family: monospace; font-size: 12px; }}
    </style>
</head>
<body>
    <h1>Hivemind API consistency report</h1>
    <p>Generated: {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}</p>
    <div class="summary">
        <h2>Summary</h2>
        <p>total: {self.summary['total']}</p>
        <p>passed: <span class="passed">{self.summary['passed']}</span></p>
        <p>failed: <span class="failed">{self.summary['failed']}</span></p>
        <p>pass rate: {self.summary['pass_rate']}</p>
        <p>duration: {self.summary['total_duration_ms']} ms</p>
        {priority_rows}
    </div>
    <h2>Results</h2>
    <p>Responses in this report are truncated; see per-case detail files for
    full payloads.</p>
    <table>
        <tr><th>Priority</th><th>API</th><th>Case</th><th>Status</th>
        <th>Duration</th><th>Difference / error</th><th>Detail file</th></tr>
        {''.join(rows)}
    </table>
</body>
</html>
"""
        with open(output_file, "w") as handle:
            handle.write(html)


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main(argv=None):
    parser = argparse.ArgumentParser(description="Hivemind API consistency test tool")
    parser.add_argument("--config", default="test_cases.yaml", help="test case YAML file")
    parser.add_argument("--python-url", default="http://localhost:8081",
                        help="Python (legacy) API URL")
    parser.add_argument("--go-url", default="http://localhost:8082",
                        help="Go API URL")
    parser.add_argument("--timeout", type=int, default=30,
                        help="request timeout in seconds")
    parser.add_argument("--output", default="results",
                        help="result output directory")
    parser.add_argument("--priority", default=None,
                        help="only run cases with these priorities "
                             "(comma-separated, e.g. P0 or P0,P1)")
    parser.add_argument("--no-html", action="store_true",
                        help="skip HTML report generation")
    args = parser.parse_args(argv)

    import os

    os.makedirs(args.output, exist_ok=True)

    runner = APITestRunner(args.python_url, args.go_url, args.timeout, args.output)
    runner.run_from_config(args.config, priority=args.priority)

    summary = runner.get_summary()
    generator = ReportGenerator(runner.results, summary)

    print(generator.generate_console_report())

    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    json_file = os.path.join(args.output, f"report-{stamp}.json")
    generator.generate_json_report(json_file)
    print(f"\nJSON report saved: {json_file}")

    if not args.no_html:
        html_file = os.path.join(args.output, f"report-{stamp}.html")
        generator.generate_html_report(html_file)
        print(f"HTML report saved: {html_file}")

    return 0 if summary["failed"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
