#!/usr/bin/env python3
"""Unit tests for the api_comparator.py comparison core (pure functions).

Covers the Q4 planned-PR#420 fixes and the PR#420-review hardening:
  * 1.2 global config block merging (ignore replace, tolerance merge)
  * 1.3 path-aware ignore matching (same name, different path: not masked)
  * 1.4 timestamp-aware and amount-aware comparison
  * 1.5 priority filtering
  * 1.6 unordered / lenient_list list comparison modes
  * 1.7 report truncation
  * PR#420-review: transport vs application error classification
    (identical transport failures never judge green)
  * PR#420-review: currency-aware amounts (unit mismatch is red; no
    amount-string-to-bare-number bridging; tightened amount regex)
  * PR#420-review: cross-type bridge TypeError guard
  * PR#420-review: timestamp field naming (suffix/whole-name, compound
    counters excluded) + epoch magnitude gate
  * PR#420-review-r2 (P1): non-envelope 200 bodies (scalars, arrays,
    health-check dicts) are transport errors, never a green result;
    {"result": null} stays legal via key-existence checking
  * PR#420-review-r2: redirects (3xx) are not followed and fail closed
  * PR#420-review-r2: units_compatible None contract (no silent
    amount-to-number bridging), epoch boundaries (1e8 / 1e12), negative
    epochs, is_duration_field exact naming
  * PR#420-review-r3: response envelope contract in APIClient.call --
    result/error mutual exclusivity (by key existence, incl. both-null),
    response id echo (strict int 1; notification form / id mismatch /
    type variants rejected), and strict error-object shape (int "code"
    + str "message"; no str(error) fallbacks)

Run:  python3 -m pytest test_api_comparator.py  (from tests/api-comparison/)
"""

import os
import sys

import pytest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from api_comparator import (  # noqa: E402
    ApiCallError,
    APIClient,
    CompareOptions,
    IgnoreMatcher,
    compare_error_texts,
    compare_values,
    evaluate_error_pair,
    filter_cases_by_priority,
    is_duration_field,
    is_timestamp_field,
    normalize_timestamp,
    options_for_case,
    parse_amount,
    truncate_for_report,
    units_compatible,
)


def make_opts(**kwargs) -> CompareOptions:
    return CompareOptions(**kwargs)


# ---------------------------------------------------------------------------
# 1.3 path-aware ignore
# ---------------------------------------------------------------------------

class TestIgnoreMatcher:
    def test_bare_name_matches_any_depth(self):
        matcher = IgnoreMatcher(["request_id"])
        assert matcher.matches(("request_id",))
        assert matcher.matches(("a", "b", "request_id"))

    def test_dotted_pattern_matches_exact_path_only(self):
        matcher = IgnoreMatcher(["author.id"])
        assert matcher.matches(("author", "id"))
        assert not matcher.matches(("id",))
        assert not matcher.matches(("post", "author", "id"))
        assert not matcher.matches(("author", "id", "extra"))

    def test_same_name_different_path_not_masked(self):
        """The core 1.3 guarantee: a global path-scoped ignore must not mask
        same-named business fields at other paths."""
        matcher = IgnoreMatcher(["author.id"])
        a = {"id": 7, "author": {"id": 1, "name": "steemit"}}
        b = {"id": 7, "author": {"id": 2, "name": "steemit"}}
        # author.id ignored, but top-level id must still compare...
        ok, diff = compare_values(a, b, make_opts(ignore=matcher))
        assert ok, diff
        # ...and a top-level id difference is caught:
        b2 = {"id": 8, "author": {"id": 2, "name": "steemit"}}
        ok, _ = compare_values(a, b2, make_opts(ignore=matcher))
        assert not ok

    def test_star_matches_any_key_or_index(self):
        matcher = IgnoreMatcher(["items.*.id"])
        assert matcher.matches(("items", "*", "id"))
        assert matcher.matches(("items", "7", "id"))
        assert not matcher.matches(("items", "id"))

    def test_double_star_matches_any_depth(self):
        matcher = IgnoreMatcher(["**.updated_at"])
        assert matcher.matches(("updated_at",))
        assert matcher.matches(("x", "y", "updated_at"))

    def test_global_bare_id_masks_everywhere_but_path_form_does_not(self):
        # legacy deep semantics kept for bare names ...
        assert IgnoreMatcher(["id"]).matches(("post", "id"))
        # ... while the path form is surgical
        assert not IgnoreMatcher(["post.id"]).matches(("author", "id"))

    def test_ignore_applies_to_missing_keys_too(self):
        """An ignored key must not trigger a key-set mismatch either."""
        matcher = IgnoreMatcher(["author.cache"])
        a = {"author": {"cache": "x", "name": "n"}}
        b = {"author": {"name": "n"}}
        ok, _ = compare_values(a, b, make_opts(ignore=matcher))
        assert ok


# ---------------------------------------------------------------------------
# 1.2 global config merge
# ---------------------------------------------------------------------------

class TestOptionsForCase:
    def test_global_defaults_applied(self):
        opts = options_for_case(
            {"ignore_fields": ["author.id"],
             "tolerance": {"float_epsilon": 0.01, "timestamp_seconds": 5}},
            {"api": "x"},
        )
        assert opts.float_epsilon == 0.01
        assert opts.timestamp_seconds == 5
        assert opts.ignore.matches(("author", "id"))

    def test_case_ignore_replaces_global(self):
        opts = options_for_case(
            {"ignore_fields": ["author.id"], "tolerance": {}},
            {"ignore_fields": ["post.body"]},
        )
        assert not opts.ignore.matches(("author", "id"))
        assert opts.ignore.matches(("post", "body"))

    def test_case_tolerance_merges_keywise_over_global(self):
        opts = options_for_case(
            {"tolerance": {"float_epsilon": 0.01, "timestamp_seconds": 5}},
            {"tolerance": {"timestamp_seconds": 30}},
        )
        assert opts.timestamp_seconds == 30
        assert opts.float_epsilon == 0.01

    def test_timestamp_fields_extra_config(self):
        opts = options_for_case(
            {"tolerance": {"timestamp_fields": ["head_stamp"]}},
            {"tolerance": {}},
        )
        assert is_timestamp_field(("x", "head_stamp"), opts.timestamp_fields)
        assert not is_timestamp_field(("x", "other"), opts.timestamp_fields)

    def test_list_mode_flags(self):
        opts = options_for_case({}, {"unordered": True, "unordered_key": "name"})
        assert opts.unordered and opts.unordered_key == "name"
        assert not opts.lenient_list


# ---------------------------------------------------------------------------
# 1.4 timestamps and amounts
# ---------------------------------------------------------------------------

class TestNormalizeTimestamp:
    def test_iso_variants_normalize_to_same_epoch(self):
        expected = normalize_timestamp("2024-01-01T12:00:00Z")
        assert expected is not None
        variants = [
            "2024-01-01T12:00:00",            # naive
            "2024-01-01T12:00:00Z",           # Zulu
            "2024-01-01T12:00:00+00:00",      # explicit UTC
            "2024-01-01 12:00:00",            # space separator
            "2024-01-01T12:00:00.000",        # zero fraction
        ]
        for variant in variants:
            assert normalize_timestamp(variant) == expected, variant

    def test_offsets_are_honored(self):
        utc = normalize_timestamp("2024-01-01T12:00:00Z")
        plus8 = normalize_timestamp("2024-01-01T20:00:00+08:00")
        assert utc == plus8

    def test_epoch_digit_strings(self):
        assert normalize_timestamp("1720000000") == 1720000000.0
        assert normalize_timestamp("1720000000000") == 1720000000.0

    def test_numbers_only_when_gated(self):
        assert normalize_timestamp(1720000000) is None            # not gated
        assert normalize_timestamp(1720000000, numeric=True) == 1720000000.0
        assert normalize_timestamp(1720000000000, numeric=True) == 1720000000.0

    def test_non_timestamps_return_none(self):
        for value in ["steemit", "firstpost", "123", "12.5 SBD", 3.5, True, None]:
            assert normalize_timestamp(value) is None, value


class TestTimestampComparison:
    def test_timestamp_strings_within_tolerance_pass(self):
        opts = make_opts(timestamp_seconds=2)
        ok, _ = compare_values("2024-01-01T12:00:00",
                               "2024-01-01T12:00:01Z", opts, ("created",))
        assert ok

    def test_timestamp_strings_beyond_tolerance_fail(self):
        opts = make_opts(timestamp_seconds=2)
        ok, diff = compare_values("2024-01-01T12:00:00",
                                  "2024-01-01T12:00:30", opts, ("created",))
        assert not ok and "timestamp" in diff

    def test_numeric_epoch_at_time_field_uses_tolerance(self):
        opts = make_opts(timestamp_seconds=5)
        ok, _ = compare_values(1720000000, 1720000003, opts, ("db_head", "age"))
        assert ok
        ok, _ = compare_values(1720000000, 1720000030, opts, ("db_head", "age"))
        assert not ok

    def test_plain_numbers_are_not_treated_as_epochs(self):
        """Regression guard: with timestamp tolerance 2s an id drift of 1 must
        still fail; ids are not timestamps."""
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        ok, _ = compare_values(1000, 1001, opts, ("id",))
        assert not ok

    def test_epoch_milliseconds_vs_seconds(self):
        opts = make_opts(timestamp_seconds=2)
        ok, _ = compare_values(1720000000000, 1720000001,
                               opts, ("last_read",))
        assert ok


class TestTimestampFieldNaming:
    """PR#420-review: compound counters must not inherit the time
    tolerance just because a time token appears inside the field name."""

    def test_compound_counters_are_not_timestamp_fields(self):
        assert not is_timestamp_field(("created_block",))
        assert not is_timestamp_field(("read_count",))
        assert not is_timestamp_field(("update_count",))
        assert not is_timestamp_field(("posts", "created_block"))

    def test_whole_name_and_suffix_tokens_match(self):
        assert is_timestamp_field(("created",))
        assert is_timestamp_field(("db_head_age",))
        assert is_timestamp_field(("cashout_time",))
        assert is_timestamp_field(("last_read_at",))
        assert is_timestamp_field(("last_update",))
        assert is_timestamp_field(("time",))

    def test_suffix_requires_underscore_boundary(self):
        # "coverage"/"somepage" end with the letters of age/page but must
        # not match: the token must be the whole name or follow a "_".
        assert not is_timestamp_field(("coverage",))
        assert not is_timestamp_field(("somepage",))
        assert not is_timestamp_field(("datetime",))

    def test_created_block_drift_is_red(self):
        """The audited false-green: 1000 vs 1002 at created_block hid in
        the 2s time tolerance; it must now be a plain number mismatch."""
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        ok, diff = compare_values(1000, 1002, opts, ("created_block",))
        assert not ok and "number mismatch" in diff

    def test_read_count_drift_is_red(self):
        opts = make_opts(timestamp_seconds=2)
        ok, _ = compare_values(5, 7, opts, ("read_count",))
        assert not ok


class TestEpochMagnitudeGate:
    """PR#420-review: only plausibly-epoch numbers (>= 1e8) get the time
    tolerance; *_age fields are durations and compare by absolute diff."""

    def test_small_numbers_at_timeish_field_are_not_epochs(self):
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        # Same field NAME that is time-ish, but small values: fall back to
        # the epsilon comparison (delta 2 > 0.001 -> red).
        ok, diff = compare_values(1000, 1002, opts, ("created",))
        assert not ok and "number mismatch" in diff

    def test_epoch_seconds_use_time_tolerance(self):
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        ok, _ = compare_values(1720000000, 1720000002, opts, ("created",))
        assert ok
        ok, _ = compare_values(1720000000, 1720000030, opts, ("created",))
        assert not ok

    def test_epoch_ms_vs_s_still_normalizes(self):
        opts = make_opts(timestamp_seconds=2)
        ok, _ = compare_values(1720000000000, 1720000001, opts, ("created",))
        assert ok

    def test_age_fields_use_absolute_difference(self):
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        ok, _ = compare_values(1000, 1001, opts, ("db_head_age",))
        assert ok
        ok, _ = compare_values(1000, 1005, opts, ("db_head_age",))
        assert not ok

    def test_epoch_vs_small_number_is_red(self):
        opts = make_opts(timestamp_seconds=2)
        # One epoch, one counter-sized value: no normalization bridges them.
        ok, _ = compare_values(1720000000, 1000, opts, ("created",))
        assert not ok


class TestEpochBoundaries:
    """PR#420-review-r2: exact EPOCH_MIN (1e8) and millisecond-threshold
    (1e12) boundary semantics, and negative-epoch behavior."""

    def test_epoch_min_inclusive_uses_time_tolerance(self):
        # Exactly 1e8 on both sides: the >= gate includes the boundary, so
        # a 2s drift is green under a 2s tolerance.
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        ok, diff = compare_values(100000000, 100000002, opts, ("created",))
        assert ok, diff

    def test_just_below_epoch_min_falls_back_to_epsilon(self):
        # 99999999 is below the gate on one side, so the SAME 2-unit drift
        # that was green at the boundary must be a plain number mismatch.
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        ok, diff = compare_values(99999999, 100000001, opts, ("created",))
        assert not ok and "number mismatch" in diff

    def test_boundary_is_sharp_not_gradual(self):
        # Same 3-unit drift on both sides of the boundary: above it, the
        # time tolerance applies (red only beyond 2s); below it, epsilon.
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        ok, diff = compare_values(100000000, 100000003, opts, ("created",))
        assert not ok and "timestamp mismatch" in diff
        ok, diff = compare_values(99999998, 100000001, opts, ("created",))
        assert not ok and "number mismatch" in diff

    def test_ms_threshold_is_exclusive(self):
        # The "> 1e12" gate: exactly 1e12 stays SECONDS...
        assert normalize_timestamp(1e12, numeric=True) == 1e12
        # ... and 1e12 + 1 is already epoch MILLISECONDS.
        assert normalize_timestamp(1e12 + 1, numeric=True) == (1e12 + 1) / 1000.0

    def test_ms_threshold_boundary_through_compare(self):
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        # Exactly 1e12 on both sides: seconds semantics, equal -> green.
        ok, diff = compare_values(1e12, 1e12, opts, ("created",))
        assert ok, diff
        # Crossing the threshold switches UNITS: 1e12 stays seconds while
        # 1e12+2 normalizes as milliseconds (~1e9), so this pair is a
        # mismatch. The exclusive gate makes the boundary itself a unit
        # switch -- pinned here rather than silently bridged.
        ok, diff = compare_values(1e12, 1e12 + 2, opts, ("created",))
        assert not ok and "timestamp mismatch" in diff
        # Both above the threshold: milliseconds normalize together; a 2s
        # (2000ms) drift is green...
        ok, _ = compare_values(1e12 + 1, 1e12 + 2001, opts, ("created",))
        assert ok
        # ... a 5s drift is red.
        ok, _ = compare_values(1e12 + 1, 1e12 + 5001, opts, ("created",))
        assert not ok

    def test_negative_epoch_magnitude_semantics(self):
        """Large-magnitude negative epochs (abs >= 1e8) compare by the time
        tolerance; the SMALL numeric form of pre-1970 sentinels (e.g. -1,
        the numeric equivalent of the cashout sentinel
        "1969-12-31T23:59:59" that real Steem payloads carry) stays below
        the gate and compares by plain epsilon -- fail closed: a numeric
        -1 vs -3 drift must not hide inside the time tolerance."""
        opts = make_opts(timestamp_seconds=2, float_epsilon=0.001)
        ok, _ = compare_values(-1720000000, -1720000002, opts, ("cashout_time",))
        assert ok
        ok, diff = compare_values(-1720000000, -1720000030, opts,
                                  ("cashout_time",))
        assert not ok and "timestamp mismatch" in diff
        # small negative numeric sentinel: plain epsilon (delta 2 > 0.001)
        ok, diff = compare_values(-1, -3, opts, ("cashout_time",))
        assert not ok and "number mismatch" in diff
        # ... while ISO STRING sentinels compare by normalized time (the
        # string path is unambiguous and carries no magnitude gate).
        ok, _ = compare_values("1969-12-31T23:59:59", "1969-12-31T23:59:57",
                               opts, ("cashout_time",))
        assert ok


class TestDurationFieldNaming:
    """PR#420-review-r2: is_duration_field exact-name contract (whole
    `age` or `_<something>_age`/`_age` suffix; letter-lookalikes excluded)."""

    def test_exact_names_match(self):
        assert is_duration_field(("age",))
        assert is_duration_field(("db_head_age",))
        assert is_duration_field(("head", "age"))
        assert is_duration_field(("cache_age",))

    def test_lookalikes_do_not_match(self):
        # ends with the letters of "age" but not with "_age"
        assert not is_duration_field(("coverage",))
        assert not is_duration_field(("average",))
        assert not is_duration_field(("page",))
        assert not is_duration_field(("db_page",))
        assert not is_duration_field(("gauge",))

    def test_case_and_hyphen_normalized(self):
        assert is_duration_field(("DB_HEAD_AGE",))
        assert is_duration_field(("db-head-age",))

    def test_empty_path_is_not_a_duration_field(self):
        assert not is_duration_field(())


class TestAmountComparison:
    def test_parse_amount_returns_value_and_unit(self):
        assert parse_amount("1.500 SBD") == (1.5, "SBD")
        assert parse_amount("$0.99") == (0.99, "$")
        assert parse_amount("-2.000 STEEM") == (-2.0, "STEEM")
        assert parse_amount("0.000000 VESTS") == (0.0, "VESTS")
        assert parse_amount("1.5 sbd") == (1.5, "SBD")  # unit case-insensitive

    def test_non_amounts_return_none(self):
        for value in ["steemit", "123", "12", 12, 1.5, None, True]:
            assert parse_amount(value) is None, value

    def test_format_traps_do_not_parse(self):
        """PR#420-review: "<number> <word>" strings and glued forms must
        never be misread as currency amounts."""
        for value in ["123abc", "1 to", "24 hours", "1.500SBD", "$ 1.5x",
                      "1.5 USD", "3 items"]:
            assert parse_amount(value) is None, value

    def test_amount_format_drift_passes(self):
        opts = make_opts(float_epsilon=0.001)
        ok, _ = compare_values("1.500 SBD", "1.5 SBD", opts, ("payout",))
        assert ok

    def test_amount_unit_case_insensitive_passes(self):
        opts = make_opts(float_epsilon=0.001)
        ok, _ = compare_values("1.500 sbd", "1.5 SBD", opts, ("payout",))
        assert ok

    def test_amount_beyond_epsilon_fails(self):
        opts = make_opts(float_epsilon=0.001)
        ok, diff = compare_values("1.500 SBD", "1.6 SBD", opts, ("payout",))
        assert not ok and "amount mismatch" in diff

    def test_currency_mismatch_is_red(self):
        """PR#420-review: the audited false-green -- parse_amount dropped
        the unit, so SBD vs STEEM with equal values judged green."""
        opts = make_opts(float_epsilon=0.001)
        ok, diff = compare_values("1.500 SBD", "1.500 STEEM", opts,
                                  ("payout",))
        assert not ok and "unit mismatch" in diff

    def test_dollar_form_only_matches_dollar_form(self):
        opts = make_opts(float_epsilon=0.001)
        ok, _ = compare_values("$1.23", "$1.230", opts, ("payout",))
        assert ok
        ok, diff = compare_values("$1.23", "1.23 SBD", opts, ("payout",))
        assert not ok and "unit mismatch" in diff

    def test_amount_string_vs_number_not_bridged(self):
        """PR#420-review: a bare number carries no currency, so an amount
        string vs number is a type divergence, not a green bridge."""
        opts = make_opts(float_epsilon=0.001)
        ok, diff = compare_values("0.750 SBD", 0.75, opts, ("pending_payout",))
        assert not ok and "type mismatch" in diff
        ok, _ = compare_values(0.75, "0.750 SBD", opts, ("pending_payout",))
        assert not ok

    def test_plain_strings_stay_exact(self):
        opts = make_opts()
        ok, _ = compare_values("abc", "abc", opts, ("body",))
        assert ok
        ok, _ = compare_values("abc", "abd", opts, ("body",))
        assert not ok


class TestUnitsCompatible:
    """PR#420-review-r2: direct contract tests for units_compatible. The
    None clause is what makes the removed cross-type amount bridge
    structurally unreachable -- a bare number side has unit None, and
    None is compatible with nothing. Pinning it directly prevents a
    future 'lenient mode' change from silently re-enabling the false
    green (amount string == bare number of any asset)."""

    def test_none_unit_is_compatible_with_nothing(self):
        assert not units_compatible(None, "SBD")
        assert not units_compatible("SBD", None)
        assert not units_compatible(None, None)
        assert not units_compatible(None, "$")

    def test_units_match_case_insensitively(self):
        assert units_compatible("SBD", "SBD")
        assert units_compatible("sbd", "SBD")
        assert units_compatible("Steem", "STEEM")

    def test_dollar_form_matches_only_dollar(self):
        assert units_compatible("$", "$")
        assert not units_compatible("$", "SBD")
        assert not units_compatible("SBD", "$")
        assert not units_compatible("$", "HBD")

    def test_distinct_currencies_are_incompatible(self):
        assert not units_compatible("STEEM", "SBD")
        assert not units_compatible("HIVE", "HBD")
        assert not units_compatible("VESTS", "STEEM")


# ---------------------------------------------------------------------------
# 1.6 list modes
# ---------------------------------------------------------------------------

class TestListModes:
    def test_strict_positional_by_default(self):
        opts = make_opts()
        a = [{"name": "x"}, {"name": "y"}]
        b = [{"name": "y"}, {"name": "x"}]
        ok, _ = compare_values(a, b, opts)
        assert not ok

    def test_unordered_sorts_by_business_key(self):
        opts = make_opts(unordered=True, unordered_key="name")
        a = [{"name": "x", "n": 1}, {"name": "y", "n": 2}]
        b = [{"name": "y", "n": 2}, {"name": "x", "n": 1}]
        ok, diff = compare_values(a, b, opts)
        assert ok, diff

    def test_unordered_still_catches_content_diff(self):
        opts = make_opts(unordered=True, unordered_key="name")
        a = [{"name": "x", "n": 1}, {"name": "y", "n": 2}]
        b = [{"name": "y", "n": 2}, {"name": "x", "n": 3}]
        ok, _ = compare_values(a, b, opts)
        assert not ok

    def test_unordered_length_mismatch_fails(self):
        opts = make_opts(unordered=True, unordered_key="name")
        ok, _ = compare_values([{"name": "x"}], [], opts)
        assert not ok

    def test_lenient_list_matches_out_of_order(self):
        opts = make_opts(lenient_list=True)
        a = [{"a": 1}, {"a": 2}, {"a": 3}]
        b = [{"a": 3}, {"a": 1}, {"a": 2}]
        ok, diff = compare_values(a, b, opts)
        assert ok, diff

    def test_lenient_list_length_checked(self):
        opts = make_opts(lenient_list=True)
        ok, _ = compare_values([{"a": 1}], [{"a": 1}, {"a": 2}], opts)
        assert not ok

    def test_lenient_list_unmatched_element_fails(self):
        opts = make_opts(lenient_list=True)
        ok, _ = compare_values([{"a": 1}, {"a": 2}], [{"a": 1}, {"a": 9}], opts)
        assert not ok

    def test_nested_lists_stay_strict_under_list_modes(self):
        """unordered/lenient_list apply to the top-level list only."""
        opts = make_opts(lenient_list=True)
        a = [{"tags": ["x", "y"]}]
        b = [{"tags": ["y", "x"]}]
        ok, _ = compare_values(a, b, opts)
        assert not ok


# ---------------------------------------------------------------------------
# 1.5 priority filtering
# ---------------------------------------------------------------------------

class TestPriorityFilter:
    CASES = [
        {"id": "a", "priority": "P0"},
        {"id": "b", "priority": "P1"},
        {"id": "c", "priority": "P2"},
        {"id": "d"},  # no priority declared
    ]

    def test_no_filter_keeps_all(self):
        assert filter_cases_by_priority(self.CASES, None) == self.CASES
        assert filter_cases_by_priority(self.CASES, "") == self.CASES

    def test_single_priority(self):
        kept = filter_cases_by_priority(self.CASES, "P0")
        assert [case["id"] for case in kept] == ["a"]

    def test_comma_separated_priorities(self):
        kept = filter_cases_by_priority(self.CASES, "p0,p2")
        assert [case["id"] for case in kept] == ["a", "c"]

    def test_unprioritized_case_never_matches_a_filter(self):
        kept = filter_cases_by_priority(self.CASES, "P0,P1,P2")
        assert [case["id"] for case in kept] == ["a", "b", "c"]


# ---------------------------------------------------------------------------
# 1.7 report truncation
# ---------------------------------------------------------------------------

class TestTruncateForReport:
    def test_short_strings_unchanged(self):
        assert truncate_for_report("hello") == "hello"

    def test_long_strings_truncated_at_limit(self):
        long_text = "x" * 500
        out = truncate_for_report(long_text, limit=200)
        assert out.startswith("x" * 200)
        assert "[truncated 300 chars]" in out

    def test_nested_structures_truncated(self):
        payload = {"body": "y" * 300, "replies": [{"body": "z" * 250}]}
        out = truncate_for_report(payload, limit=200)
        assert "[truncated 100 chars]" in out["body"]
        assert "[truncated 50 chars]" in out["replies"][0]["body"]

    def test_non_strings_pass_through(self):
        assert truncate_for_report({"n": 3, "ok": True, "x": None}) == {
            "n": 3, "ok": True, "x": None}


# ---------------------------------------------------------------------------
# core comparison guards
# ---------------------------------------------------------------------------

class TestComparisonGuards:
    def test_bool_never_equals_one(self):
        opts = make_opts()
        ok, _ = compare_values(True, 1, opts, ("flag",))
        assert not ok
        ok, _ = compare_values(True, True, opts, ("flag",))
        assert ok

    def test_int_float_interchange_within_epsilon(self):
        opts = make_opts(float_epsilon=0.001)
        ok, _ = compare_values(1, 1.0005, opts, ("n",))
        assert ok

    def test_type_mismatch_message(self):
        opts = make_opts()
        ok, diff = compare_values("abc", 3, opts, ("n",))
        assert not ok and "type mismatch" in diff

    def test_cross_type_list_vs_amount_string_no_typeerror(self):
        """PR#420-review regression: ["x"] vs "1.5 SBD" used to raise
        TypeError (list subtraction in the amount bridge); it must land
        on the type-mismatch branch instead."""
        opts = make_opts()
        try:
            ok, diff = compare_values(["x"], "1.5 SBD", opts, ("n",))
        except TypeError:
            pytest.fail("compare_values raised TypeError on cross-type pair")
        assert not ok and "type mismatch" in diff

        ok, diff = compare_values("1.5 SBD", ["x"], opts, ("n",))
        assert not ok and "type mismatch" in diff

    def test_null_handling(self):
        opts = make_opts()
        ok, _ = compare_values(None, None, opts, ("n",))
        assert ok
        ok, _ = compare_values(None, 3, opts, ("n",))
        assert not ok

    def test_key_mismatch_detected(self):
        opts = make_opts()
        ok, diff = compare_values({"a": 1}, {"b": 1}, opts)
        assert not ok and "key mismatch" in diff

    def test_error_text_comparison(self):
        ok, _ = compare_error_texts("not supported ", "not supported")
        assert ok
        ok, _ = compare_error_texts("not supported", "unsupported")
        assert not ok


# ---------------------------------------------------------------------------
# PR#420-review: transport vs application error classification
# ---------------------------------------------------------------------------

class TestErrorLayerClassification:
    def test_both_transport_errors_fail_even_with_identical_text(self):
        """The audited false-green: both endpoints refusing connections or
        returning 500 produce identical error texts, which the old
        both-error branch judged green."""
        ok, diff = evaluate_error_pair(
            ApiCallError("request failed: 500", transport=True),
            ApiCallError("request failed: 500", transport=True))
        assert not ok and "transport" in diff

    def test_one_transport_one_application_fails(self):
        ok, diff = evaluate_error_pair(
            ApiCallError("unsupported", code=10),
            ApiCallError("request failed: timeout", transport=True))
        assert not ok and "transport" in diff
        ok, _ = evaluate_error_pair(
            ApiCallError("request failed: refused", transport=True),
            ApiCallError("unsupported", code=10))
        assert not ok

    def test_codeless_error_is_treated_as_transport(self):
        """Fail-closed: an application error only counts as comparable
        when it explicitly carries a JSON-RPC code."""
        assert ApiCallError("boom").is_transport
        assert not ApiCallError("boom", code=10).is_transport
        assert ApiCallError("boom", code=10, transport=True).is_transport
        ok, diff = evaluate_error_pair(ApiCallError("boom"),
                                       ApiCallError("boom"))
        assert not ok and "transport" in diff

    def test_both_application_errors_compare_by_text(self):
        ok, _ = evaluate_error_pair(
            ApiCallError("get_account_votes is no longer supported", code=10),
            ApiCallError("get_account_votes is no longer supported ", code=10))
        assert ok
        ok, diff = evaluate_error_pair(
            ApiCallError("msg A", code=10), ApiCallError("msg B", code=10))
        assert not ok and "error text mismatch" in diff


# ---------------------------------------------------------------------------
# PR#420-review-r2 (P1): APIClient envelope validation
# ---------------------------------------------------------------------------

class _FakeResponse:
    """Minimal requests.Response stand-in (status, headers, JSON body)."""

    def __init__(self, status_code=200, json_data=None, headers=None,
                 json_error=None):
        self.status_code = status_code
        self.headers = headers or {}
        self._json_data = json_data
        self._json_error = json_error

    def raise_for_status(self):
        if 400 <= self.status_code < 600:
            raise RuntimeError(f"HTTP {self.status_code} error")

    def json(self):
        if self._json_error is not None:
            raise self._json_error
        return self._json_data


class _FakeSession:
    """Records post() kwargs; returns a canned response."""

    def __init__(self, response):
        self.response = response
        self.calls = []

    def post(self, _url, **kwargs):
        self.calls.append(kwargs)
        return self.response


def make_client(response) -> tuple:
    pytest.importorskip("requests")  # APIClient.__init__ imports it lazily
    client = APIClient("http://stub.example")
    client.session = _FakeSession(response)
    return client, client.session


class TestAPIClientEnvelopeValidation:
    """PR#420-review-r2 (P1): the audited false-green -- a 200 response
    whose body is NOT a JSON-RPC envelope (scalar, array, or a dict like
    {"status": "ok"}) was returned whole as the "result"; when both
    --python-url and --go-url were (mis)pointed at such a service (e.g. a
    health endpoint, which run_tests.sh's /health probe happily passes),
    both sides matched and the entire run judged green with zero
    evidence. Now any non-envelope body raises a transport ApiCallError."""

    def test_scalar_body_is_transport_error(self):
        client, _ = make_client(_FakeResponse(json_data="ok"))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", ["a", "b"])
        assert excinfo.value.is_transport
        assert "malformed JSON-RPC" in excinfo.value.message

    def test_array_body_is_transport_error(self):
        client, _ = make_client(_FakeResponse(json_data=[1, 2, 3]))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_followers", [])
        assert excinfo.value.is_transport
        assert "malformed JSON-RPC" in excinfo.value.message

    def test_health_check_dict_is_transport_error(self):
        """The exact probe scenario: {"status": "ok"} from a health
        service must not be treated as a comparable result."""
        client, _ = make_client(_FakeResponse(json_data={"status": "ok"}))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", ["steemit", "firstpost"])
        assert excinfo.value.is_transport
        assert "malformed JSON-RPC" in excinfo.value.message

    def test_null_result_is_legal(self):
        """{"result": null} is a valid empty result: validity is decided by
        KEY EXISTENCE, never by truthiness (an `if data.get("result")`
        check would reject legal null results)."""
        client, _ = make_client(
            _FakeResponse(json_data={"jsonrpc": "2.0", "id": 1, "result": None}))
        assert client.call("condenser_api.get_content", ["a", "b"]) is None

    def test_falsy_results_are_legal(self):
        client, _ = make_client(
            _FakeResponse(json_data={"jsonrpc": "2.0", "id": 1, "result": []}))
        assert client.call("condenser_api.get_followers", []) == []
        client, _ = make_client(
            _FakeResponse(json_data={"jsonrpc": "2.0", "id": 1, "result": 0}))
        assert client.call("hive.db_head_state", []) == 0

    def test_result_dict_returned(self):
        body = {"jsonrpc": "2.0", "id": 1, "result": {"author": "steemit"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        assert client.call("bridge.get_profile", {"account": "steemit"}) == {
            "author": "steemit"}

    def test_error_envelope_is_application_error(self):
        body = {"jsonrpc": "2.0", "id": 1,
                "error": {"code": 10, "message": "unsupported"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_account_votes", ["steemit"])
        assert not excinfo.value.is_transport
        assert excinfo.value.code == 10
        assert excinfo.value.message == "unsupported"

    def test_error_null_is_malformed(self):
        """{"error": null} is envelope-shaped but carries neither a usable
        error nor a result: fail closed as malformed."""
        client, _ = make_client(
            _FakeResponse(json_data={"jsonrpc": "2.0", "id": 1, "error": None}))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "malformed JSON-RPC" in excinfo.value.message

    def test_http_error_wrapped_as_transport(self):
        client, _ = make_client(_FakeResponse(status_code=503))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "request failed" in excinfo.value.message

    def test_non_json_body_wrapped_as_transport(self):
        client, _ = make_client(
            _FakeResponse(json_error=ValueError("expecting value")))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport


class TestAPIClientRedirects:
    """PR#420-review-r2: POST redirects must never be followed. requests
    rewrites POST 301/302 to GET while following, so a followed redirect
    would silently compare whatever the target serves (e.g. a health
    page). The client sends allow_redirects=False and surfaces any 3xx
    as a transport error."""

    def test_post_disables_redirect_following(self):
        client, session = make_client(
            _FakeResponse(json_data={"jsonrpc": "2.0", "id": 1, "result": None}))
        client.call("condenser_api.get_content", [])
        assert session.calls, "fake session saw no request"
        assert session.calls[0].get("allow_redirects") is False


# ---------------------------------------------------------------------------
# PR#420-review-r3 (P1): result/error mutual exclusivity, response id,
# error-object shape -- the audited false-greens in APIClient.call
# ---------------------------------------------------------------------------

class TestResultErrorExclusivity:
    """PR#420-review-r3 (P1): a body carrying BOTH "result" and "error"
    used to fall into the error branch only, so an identical error on
    both endpoints masked divergent results ({"result": A, "error": E} vs
    {"result": B, "error": E} judged green -- probes e5/e5b/e13). Key
    EXISTENCE decides: even {"result": null, "error": null} is rejected."""

    def test_result_and_error_populated_is_malformed(self):
        body = {"jsonrpc": "2.0", "id": 1, "result": {"author": "alice"},
                "error": {"code": 10, "message": "partial failure"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "malformed JSON-RPC" in excinfo.value.message
        assert "mutually exclusive" in excinfo.value.message

    def test_result_null_error_null_is_malformed(self):
        """Key existence, not truthiness: both keys null must still be
        rejected (a truthiness check would let {"result": null,
        "error": null} slip through the exclusivity gate)."""
        body = {"jsonrpc": "2.0", "id": 1, "result": None, "error": None}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "mutually exclusive" in excinfo.value.message

    def test_result_populated_error_null_is_malformed(self):
        body = {"jsonrpc": "2.0", "id": 1, "result": {"author": "alice"},
                "error": None}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "mutually exclusive" in excinfo.value.message

    def test_result_null_error_populated_is_malformed(self):
        body = {"jsonrpc": "2.0", "id": 1, "result": None,
                "error": {"code": 10, "message": "partial failure"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "mutually exclusive" in excinfo.value.message

    def test_masked_divergence_judged_red_end_to_end(self):
        """The full audited false-green (probe e5): python answers
        {"result": A, "error": E}, go answers {"result": B, same E}. The
        old client raised the identical application error on both sides,
        and the both-error branch compared the texts green -- the results
        A vs B were silently dropped. Now both sides are transport
        malformations and evaluate_error_pair fails the case."""
        error = {"code": 10, "message": "partial failure"}
        py_client, _ = make_client(_FakeResponse(json_data={
            "jsonrpc": "2.0", "id": 1, "result": {"author": "alice"},
            "error": error}))
        go_client, _ = make_client(_FakeResponse(json_data={
            "jsonrpc": "2.0", "id": 1, "result": {"author": "bob"},
            "error": error}))
        excs = []
        for client in (py_client, go_client):
            with pytest.raises(ApiCallError) as excinfo:
                client.call("condenser_api.get_content", [])
            excs.append(excinfo.value)
        ok, diff = evaluate_error_pair(*excs)
        assert not ok and "transport" in diff


class TestResponseIdValidation:
    """PR#420-review-r3 (P2): the client always sends id=1 but never
    checked the response id (probe e6) -- a notification-form body (no
    id) or an id=999 canned response compared green when both endpoints
    served the same shape. The response must echo int 1 exactly; type
    variants ("1", 1.0, true) count as mismatches too."""

    def test_missing_id_is_malformed(self):
        body = {"jsonrpc": "2.0", "result": {"author": "steemit"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "malformed JSON-RPC" in excinfo.value.message
        assert "id" in excinfo.value.message

    def test_wrong_id_value_is_malformed(self):
        body = {"jsonrpc": "2.0", "id": 999, "result": {"author": "steemit"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "id" in excinfo.value.message

    def test_string_id_is_malformed(self):
        """Type must match: "1" is not int 1 (cross-talk must not pass
        just because the value stringifies the same)."""
        body = {"jsonrpc": "2.0", "id": "1", "result": {"author": "steemit"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "id" in excinfo.value.message

    def test_float_id_is_malformed(self):
        body = {"jsonrpc": "2.0", "id": 1.0, "result": {"author": "steemit"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport

    def test_null_id_is_malformed(self):
        body = {"jsonrpc": "2.0", "id": None, "result": {"author": "steemit"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport

    def test_error_envelope_without_id_is_malformed(self):
        """The id gate applies to error responses too: a well-formed
        error object without an echoed id is a canned/cross-wired
        response, not a comparable application error."""
        body = {"jsonrpc": "2.0",
                "error": {"code": 10, "message": "unsupported"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_account_votes", [])
        assert excinfo.value.is_transport
        assert "id" in excinfo.value.message

    def test_correct_id_result_passes(self):
        body = {"jsonrpc": "2.0", "id": 1, "result": {"author": "steemit"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        assert client.call("condenser_api.get_content", []) == {
            "author": "steemit"}


class TestErrorObjectShape:
    """PR#420-review-r3 (P2): the error branch validated code and message
    asymmetrically. A missing message fell back to str(error) -- the dict
    repr compared "equal" on both endpoints (probe e12) -- and a null
    message produced ApiCallError(message=None) rescued only by run_case.
    Now an error must be an object with int "code" and str "message";
    anything else is a transport malformation."""

    def _expect_malformed(self, error):
        client, _ = make_client(_FakeResponse(json_data={
            "jsonrpc": "2.0", "id": 1, "error": error}))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_account_votes", [])
        assert excinfo.value.is_transport
        assert "malformed JSON-RPC" in excinfo.value.message

    def test_missing_message_is_malformed(self):
        """The e12 probe: {"error": {"code": -1}} used to raise
        ApiCallError(str(error)) whose dict-repr text matched on both
        sides and judged the case green."""
        self._expect_malformed({"code": -1})

    def test_null_message_is_malformed(self):
        self._expect_malformed({"code": -1, "message": None})

    def test_non_string_message_is_malformed(self):
        self._expect_malformed({"code": -1, "message": 123})

    def test_missing_code_is_malformed(self):
        """Code omission was already fail-closed via the codeless-error
        rule, but only implicitly; now it is an explicit malformation."""
        self._expect_malformed({"message": "boom"})

    def test_null_code_is_malformed(self):
        self._expect_malformed({"code": None, "message": "boom"})

    def test_string_code_is_malformed(self):
        self._expect_malformed({"code": "-32601", "message": "boom"})

    def test_float_code_is_malformed(self):
        self._expect_malformed({"code": -32601.0, "message": "boom"})

    def test_list_code_is_malformed(self):
        self._expect_malformed({"code": [-32601], "message": "boom"})

    def test_bare_string_error_is_malformed(self):
        """A non-object error value is a malformed envelope, not an
        application error with a codeless dict-repr text."""
        self._expect_malformed("boom")

    def test_well_formed_error_still_application_error(self):
        body = {"jsonrpc": "2.0", "id": 1,
                "error": {"code": -32601, "message": "method not found"}}
        client, _ = make_client(_FakeResponse(json_data=body))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("no.such_method", [])
        assert not excinfo.value.is_transport
        assert excinfo.value.code == -32601
        assert excinfo.value.message == "method not found"

    def test_302_is_transport_error(self):
        client, _ = make_client(_FakeResponse(
            status_code=302, headers={"Location": "/health"}))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "redirect" in excinfo.value.message
        assert "302" in excinfo.value.message
        assert "/health" in excinfo.value.message

    def test_301_is_transport_error(self):
        client, _ = make_client(_FakeResponse(
            status_code=301, headers={"Location": "https://other.example"}))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "redirect" in excinfo.value.message

    def test_302_without_location_still_fails(self):
        client, _ = make_client(_FakeResponse(status_code=302))
        with pytest.raises(ApiCallError) as excinfo:
            client.call("condenser_api.get_content", [])
        assert excinfo.value.is_transport
        assert "redirect" in excinfo.value.message


# ---------------------------------------------------------------------------
# PR#420-review: diff lines stay bounded in reports
# ---------------------------------------------------------------------------

class TestDiffTruncation:
    def test_long_string_mismatch_diff_is_bounded(self):
        """A 5000-char body must not produce a 10KB diff line: embedded
        values are truncated via truncate_for_report."""
        opts = make_opts()
        _, diff = compare_values("x" * 5000, "y" * 5000, opts, ("body",))
        assert len(diff) < 600
        assert "truncated" in diff

    def test_error_text_mismatch_diff_is_bounded(self):
        _, diff = compare_error_texts("e" * 5000, "f" * 5000)
        assert len(diff) < 600

    def test_long_null_mismatch_diff_is_bounded(self):
        opts = make_opts()
        _, diff = compare_values("x" * 5000, None, opts, ("body",))
        assert len(diff) < 600
