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
  * Numbers at timestamp-ish field paths compare within timestamp_seconds.
    A field is timestamp-ish when its name is exactly a time token
    (`created`, `time`, ...) or ends with `_<token>` (`db_head_age`,
    `cashout_time`, `last_read_at`); compound counters such as
    `created_block` or `read_count` are NOT timestamps. Epoch comparison
    additionally requires both values >= 1e8 (plausible epoch seconds);
    `*_age` fields hold small second durations and compare by absolute
    difference instead. Values > 1e12 are treated as epoch milliseconds.
  * Amount strings ("0.000 SBD", "$1.23") parse to (value, unit) and
    compare numerically within float_epsilon ONLY when the currency units
    match (case-insensitive; "$" matches only "$"). An amount string never
    bridges to a bare number: the number side carries no unit to verify,
    so that pair is reported as a type mismatch.
  * When both endpoints return JSON-RPC application-layer errors, the
    error texts are compared. Transport-level failures (connection
    refused, timeout, HTTP status error, malformed response) are NEVER
    judged green, even when both sides fail identically -- identical
    transport errors carry no consistency evidence.
  * ignore_fields patterns are path-aware; a bare name matches that dict key
    at any depth, a dotted pattern matches only the exact path. This keeps a
    global ignore such as `author.id` from also masking a top-level `id`.

Exit codes: 0 = every executed case passed, 1 = at least one failure,
2 = no cases executed (e.g. a --priority filter that matched nothing) --
zero evidence must never read as "all green" in CI.

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
from html import escape as html_escape
from typing import Any, Optional

import yaml

DEFAULT_FLOAT_EPSILON = 0.001
DEFAULT_TIMESTAMP_SECONDS = 2.0
REPORT_FIELD_LIMIT = 200

# Field-name tokens that mark a numeric field as a timestamp. A field is
# time-ish when its name EQUALS one of these tokens or ENDS with
# "_<token>" (created, db_head_age, cashout_time, last_read_at). The
# previous any-token-substring match also caught compound counters
# (created_block, read_count, update_count) and hid small numeric drift
# inside the time tolerance; suffix/whole-name matching excludes them.
DEFAULT_TIMESTAMP_TOKENS = frozenset({
    "time", "created", "updated", "update", "cashout", "age", "date",
    "expires", "expiration", "reblogged", "read", "at",
})

# Numbers below this are never treated as epoch seconds at timestamp-ish
# fields (1e8 ~= Mar 1973): block numbers, counters and ids fall back to
# the plain float_epsilon comparison instead of the time tolerance.
EPOCH_MIN = 1e8

# ISO8601-ish: date-only, or date + [T or space] time (+ optional seconds,
# fraction, Z or numeric offset).
_ISO_TS_RE = re.compile(
    r"^\d{4}-\d{2}-\d{2}"
    r"([T ]\d{2}:\d{2}(:\d{2}(\.\d{1,9})?)?(Z|[+-]\d{2}:?\d{2})?)?$"
)

# Amount string: number, mandatory whitespace, and a currency from the
# known Steem/Hive asset set (case-insensitive, see KNOWN_AMOUNT_UNITS).
# The whitespace boundary and the whitelist keep "<number> <word>"
# strings ("24 hours", "1 to") and glued forms ("123abc") from parsing
# as amounts.
_AMOUNT_RE = re.compile(r"^(-?\d+(?:\.\d+)?)\s+([A-Za-z]{3,8})$")
_DOLLAR_AMOUNT_RE = re.compile(r"^\$\s*(-?\d+(?:\.\d+)?)$")

# Currency units recognized after the numeric part of an amount string.
KNOWN_AMOUNT_UNITS = frozenset({"STEEM", "SBD", "VESTS", "HIVE", "HBD"})


class ApiCallError(Exception):
    """Raised when a call fails, at one of two layers:

    * application layer (transport=False): the endpoint answered with a
      JSON-RPC error object -- a deliberate, comparable API behavior;
    * transport layer (transport=True): connection refused/timeout, HTTP
      status error (raise_for_status), or a malformed/non-JSON response.
      Such failures carry no consistency evidence and are never judged
      green, even when both endpoints fail identically.
    """

    def __init__(self, message: str, code: Optional[int] = None,
                 transport: bool = False):
        super().__init__(message)
        self.message = message
        self.code = code
        self.transport = transport

    @property
    def is_transport(self) -> bool:
        """Fail-closed classification: an error is comparable only when it
        is explicitly application-layer AND carries a JSON-RPC code."""
        return self.transport or self.code is None


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


def parse_amount(value: Any) -> Optional[tuple]:
    """Parse "123.456 SBD" / "$1.23" style amount strings into an
    (amount, unit) tuple; the unit is uppercased ("SBD", "STEEM", ...) or
    "$" for the dollar display form.

    Returns None for anything that is not a currency-marked amount string
    (a bare numeric string stays a string; use numbers to express numbers).
    """
    if isinstance(value, bool) or not isinstance(value, str):
        return None
    text = value.strip()
    match = _DOLLAR_AMOUNT_RE.match(text)
    if match:
        return float(match.group(1)), "$"
    match = _AMOUNT_RE.match(text)
    if match and match.group(2).upper() in KNOWN_AMOUNT_UNITS:
        return float(match.group(1)), match.group(2).upper()
    return None


def units_compatible(unit_a: Optional[str], unit_b: Optional[str]) -> bool:
    """Amount units match case-insensitively ("sbd" == "SBD"); the "$"
    display form matches only "$". A missing unit (None, e.g. the bare
    number side of a cross-type pair) is compatible with nothing: the
    asset cannot be verified, so such pairs stay red instead of bridging
    to green."""
    if unit_a is None or unit_b is None:
        return False
    return unit_a.upper() == unit_b.upper()


def is_timestamp_field(path: tuple, extra_fields=()) -> bool:
    """True when the path's field name marks a numeric value as a timestamp.

    Matching is whole-name or underscore-suffix: the name equals a known
    time token (`created`, `time`, ...) or ends with `_` + token
    (`db_head_age`, `cashout_time`, `last_read_at`). Compound counters
    (`created_block`, `read_count`) do not match. Config extras
    (tolerance.timestamp_fields) compare against the full field name.
    """
    if not path:
        return False
    name = str(path[-1]).lower().replace("-", "_")
    if name in DEFAULT_TIMESTAMP_TOKENS:
        return True
    if any(name.endswith("_" + token) for token in DEFAULT_TIMESTAMP_TOKENS):
        return True
    return name in {str(field).lower() for field in extra_fields}


def is_duration_field(path: tuple) -> bool:
    """True for `age`-like fields: they hold small second durations (e.g.
    hive.db_head_state db_head_age), not epoch values, so they compare by
    absolute difference without epoch/millisecond normalization."""
    if not path:
        return False
    name = str(path[-1]).lower().replace("-", "_")
    return name == "age" or name.endswith("_age")


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


def _short(value: Any, limit: int = REPORT_FIELD_LIMIT) -> str:
    """Bounded repr for values embedded in difference messages (reuses
    truncate_for_report), so a multi-KB response body cannot blow a diff
    line up to twice its size in the console/JSON/HTML reports."""
    return truncate_for_report(repr(value), limit)


def _fmt_path(path: tuple) -> str:
    rendered = ""
    for seg in path:
        rendered += f"[{seg}]" if seg == "*" else (f".{seg}" if rendered else str(seg))
    return rendered or "<root>"


def _amount_pair(a: Any, b: Any) -> Optional[tuple]:
    """((value, unit), (value, unit)) when exactly one side is a currency
    amount string and the other is a plain number; else None. Guards the
    cross-type comparison against non-numeric mismatches (e.g. a list vs
    an amount string), which previously raised TypeError inside the
    numeric subtraction."""
    if isinstance(a, str) and isinstance(b, (int, float)):
        parsed = parse_amount(a)
        return (parsed, (float(b), None)) if parsed is not None else None
    if isinstance(b, str) and isinstance(a, (int, float)):
        parsed = parse_amount(b)
        return ((float(a), None), parsed) if parsed is not None else None
    return None


def compare_values(a: Any, b: Any, opts: CompareOptions,
                   path: tuple = ()) -> tuple[bool, Optional[str]]:
    """Compare two decoded JSON values. Returns (matched, difference)."""
    where = _fmt_path(path)

    if a is None and b is None:
        return True, None
    if a is None or b is None:
        return False, (f"{where}: one side is null "
                       f"(python={_short(a)}, go={_short(b)})")

    # Booleans first: bool is an int subclass in Python, so True must never
    # compare equal to 1.
    if isinstance(a, bool) or isinstance(b, bool):
        if isinstance(a, bool) and isinstance(b, bool) and a == b:
            return True, None
        return False, f"{where}: bool mismatch (python={_short(a)}, go={_short(b)})"

    if isinstance(a, (int, float)) and isinstance(b, (int, float)):
        return _compare_numbers(a, b, opts, path, where)

    if isinstance(a, str) and isinstance(b, str):
        return _compare_strings(a, b, opts, path, where)

    # Cross-type: exactly one side is a string. Amount strings used to
    # bridge to bare numbers here, silently dropping the currency unit
    # ("1.500 SBD" == 1.5 == "1.500 STEEM" judged green). Bridging now
    # requires both sides to carry compatible units (units_compatible); a
    # bare JSON number has no unit, so an amount string vs a number is a
    # genuine type divergence and stays red. The isinstance-numeric guard
    # inside _amount_pair keeps non-numeric pairs (["x"] vs "1.5 SBD") on
    # this type-mismatch path instead of raising TypeError.
    if isinstance(a, str) != isinstance(b, str):
        pair = _amount_pair(a, b)
        if pair is not None and units_compatible(pair[0][1], pair[1][1]):
            ok, _ = _compare_numbers(pair[0][0], pair[1][0], opts, path, where)
            if ok:
                return True, None
        return False, (
            f"{where}: type mismatch "
            f"(python={type(a).__name__} {_short(a)}, "
            f"go={type(b).__name__} {_short(b)})"
        )

    if isinstance(a, list) and isinstance(b, list):
        return _compare_lists(a, b, opts, path, where)

    if isinstance(a, dict) and isinstance(b, dict):
        return _compare_dicts(a, b, opts, path, where)

    return False, f"{where}: unsupported types (python={type(a).__name__}, go={type(b).__name__})"


def _compare_numbers(a: float, b: float, opts: CompareOptions,
                     path: tuple, where: str) -> tuple[bool, Optional[str]]:
    if is_timestamp_field(path, opts.timestamp_fields):
        if is_duration_field(path):
            # age-like fields hold small second durations: plain absolute
            # difference, no epoch/millisecond normalization.
            diff = abs(a - b)
            if diff <= opts.timestamp_seconds:
                return True, None
            return False, (
                f"{where}: duration mismatch: python={a}, go={b}, "
                f"delta={diff}s > tolerance={opts.timestamp_seconds}s"
            )
        if abs(a) >= EPOCH_MIN and abs(b) >= EPOCH_MIN:
            # Both plausibly epoch seconds (or milliseconds): normalize
            # and compare within the time tolerance.
            ts_a = normalize_timestamp(a, numeric=True)
            ts_b = normalize_timestamp(b, numeric=True)
            diff = abs(ts_a - ts_b)
            if diff <= opts.timestamp_seconds:
                return True, None
            return False, (
                f"{where}: timestamp mismatch: python={a}, go={b}, "
                f"delta={diff}s > tolerance={opts.timestamp_seconds}s"
            )
        # Small numbers at a time-ish field name are NOT epochs (block
        # counters, ids); fall through to the plain numeric comparison.
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
            f"{where}: timestamp mismatch: python={_short(a)} ({ts_a}), "
            f"go={_short(b)} ({ts_b}), delta={diff:.3f}s > "
            f"tolerance={opts.timestamp_seconds}s"
        )

    # Amount-aware and currency-aware: "1.500 SBD" vs "1.5 SBD" compares
    # numerically, but ONLY when the units match ("$1.23" never equals
    # "1.23 SBD", and "1.500 SBD" never equals "1.500 STEEM").
    amount_a = parse_amount(a)
    amount_b = parse_amount(b)
    if amount_a is not None and amount_b is not None:
        value_a, unit_a = amount_a
        value_b, unit_b = amount_b
        if not units_compatible(unit_a, unit_b):
            return False, (
                f"{where}: amount unit mismatch: python={_short(a)}, "
                f"go={_short(b)} ({unit_a} vs {unit_b}); the currency must "
                f"match on both sides"
            )
        diff = abs(value_a - value_b)
        if diff <= opts.float_epsilon:
            return True, None
        return False, (
            f"{where}: amount mismatch: python={_short(a)}, go={_short(b)}, "
            f"delta={diff} > epsilon={opts.float_epsilon}"
        )

    if a == b:
        return True, None
    return False, f"{where}: string mismatch: python={_short(a)}, go={_short(b)}"


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
    return False, (f"error text mismatch: python={_short(err_python)}, "
                   f"go={_short(err_go)}")


def evaluate_error_pair(python_exc: ApiCallError,
                        go_exc: ApiCallError) -> tuple[bool, Optional[str]]:
    """Judge a both-sides-errored case.

    Only JSON-RPC application-layer errors on BOTH sides are comparable
    (via error text comparison). If either side failed at the transport
    layer (connection refused, timeout, HTTP 5xx, malformed response) the
    case fails outright: identical transport error texts (e.g. both
    endpoints down, or both returning 500) must never certify consistency
    -- that was the "both errored, texts match, so green" false-green
    path this function closes.
    """
    if python_exc.is_transport or go_exc.is_transport:
        sides = []
        if python_exc.is_transport:
            sides.append("python")
        if go_exc.is_transport:
            sides.append("go")
        return False, (
            f"transport-level error on {' and '.join(sides)} side "
            f"(python={_short(python_exc.message)}, "
            f"go={_short(go_exc.message)}); both-error comparison requires "
            "JSON-RPC application errors on both endpoints"
        )
    passed, difference = compare_error_texts(python_exc.message, go_exc.message)
    if passed:
        return True, None
    return passed, difference


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
            # Transport layer: connection refused/timeout, HTTP status
            # error, malformed body. Never comparable, always fail-closed.
            raise ApiCallError(f"request failed: {exc}",
                               transport=True) from exc

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
            "difference": truncate_for_report(self.difference),
            "error": truncate_for_report(self.error),
            "python_error": truncate_for_report(self.python_error),
            "go_error": truncate_for_report(self.go_error),
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
        python_exc: Optional[ApiCallError] = None
        go_exc: Optional[ApiCallError] = None
        passed = False

        try:
            try:
                python_response = self.python_client.call(api_method, params)
            except ApiCallError as exc:
                python_exc = exc
                python_error = exc.message

            try:
                go_response = self.go_client.call(api_method, params)
            except ApiCallError as exc:
                go_exc = exc
                go_error = exc.message

            if python_exc is not None and go_exc is not None:
                # Both endpoints errored: comparable only when both are
                # application-level JSON-RPC errors; a transport failure on
                # either side fails the case (see evaluate_error_pair).
                passed, difference = evaluate_error_pair(python_exc, go_exc)
                if passed:
                    difference = None
            elif python_exc is not None or go_exc is not None:
                passed = False
                difference = (f"one side errored "
                              f"(python={_short(python_error)}, "
                              f"go={_short(go_error)})")
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
        if self.summary["total"] == 0:
            lines.append(
                "  WARNING: no cases executed (empty run); nothing was "
                "verified -- check the --priority filter."
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
            # Escape everything attacker- or payload-controlled (case ids,
            # api names, diff text) so response bodies cannot inject
            # markup into the report.
            diff = html_escape(result.difference or result.error or "-")
            detail = html_escape(str(result.detail_file or "-"))
            rows.append(
                "<tr class='{css}'><td>{priority}</td><td>{api}</td>"
                "<td>{case_id}</td><td>{status}</td><td>{duration} ms</td>"
                "<td class='diff'>{diff}</td><td>{detail}</td></tr>".format(
                    css=css, priority=html_escape(result.priority),
                    api=html_escape(result.api),
                    case_id=html_escape(result.case_id), status=status,
                    duration=result.duration_ms, diff=diff, detail=detail,
                )
            )
        priority_rows = "".join(
            f"<p>{html_escape(name)}: {bucket['passed']}/{bucket['total']} passed</p>"
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

    if summary["total"] == 0:
        # Empty run: a --priority filter that matches nothing must not exit
        # 0 -- zero evidence must never read as "all green" in CI (e.g. a
        # typoed priority would otherwise produce a flawless-looking run).
        print(
            f"WARNING: no test cases executed (priority filter "
            f"{args.priority!r} matched nothing); nothing was verified.",
            file=sys.stderr,
        )
        return 2

    return 0 if summary["failed"] == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
