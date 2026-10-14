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

Run:  python3 -m pytest test_api_comparator.py  (from tests/api-comparison/)
"""

import os
import sys

import pytest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from api_comparator import (  # noqa: E402
    ApiCallError,
    CompareOptions,
    IgnoreMatcher,
    compare_error_texts,
    compare_values,
    evaluate_error_pair,
    filter_cases_by_priority,
    is_timestamp_field,
    normalize_timestamp,
    options_for_case,
    parse_amount,
    truncate_for_report,
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
