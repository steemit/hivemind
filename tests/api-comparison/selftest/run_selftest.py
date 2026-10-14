#!/usr/bin/env python3
"""End-to-end selftest/smoke run for the API comparison tool (planned PR#420).

Boots two local stub JSON-RPC servers (a legacy-flavored one with strict
positional-param validation and a Go-flavored lenient one), runs
api_comparator.py against them with the reduced selftest case set, and
asserts the expected outcomes. Proves, without any real service, database
or network access beyond loopback:

  1.1 positional (array) param cases pass instead of erroring on both ends,
      while the legacy object form still fails on exactly the python side
      (negative control with direction lock);
  1.2 the top-level `config:` block (global ignore/tolerance) is applied;
  1.4 timestamp (ISO variants + numeric epoch fields) and amount-string
      tolerances judge formatting drift green;
  1.6 unordered / lenient_list top-level comparison modes work;
  1.7 reports embed truncated responses while full payloads land in
      per-case detail files under the output directory;
  1.5 --priority filtering runs only the requested priorities;
  PR#420-review: transport-level failures (identical HTTP 500 on both
      sides; both endpoints unreachable) are judged RED, never green, and
      only application-level JSON-RPC errors compare by text;
  PR#420-review: an empty run (--priority matching nothing) exits 2 with
      an explicit warning instead of a green-looking exit 0.

Run from tests/api-comparison/:  python3 selftest/run_selftest.py
"""

import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time

SELFTEST_DIR = os.path.dirname(os.path.abspath(__file__))
TOOL_DIR = os.path.dirname(SELFTEST_DIR)
COMPARATOR = os.path.join(TOOL_DIR, "api_comparator.py")
STUB = os.path.join(SELFTEST_DIR, "stub_server.py")
CASES = os.path.join(SELFTEST_DIR, "selftest_cases.yaml")
PYTHON_SCRIPT = os.path.join(SELFTEST_DIR, "stub_responses_python.json")
GO_SCRIPT = os.path.join(SELFTEST_DIR, "stub_responses_go.json")

# case id -> expected outcome (True = pass)
EXPECTED = {
    "selftest-positional-params": True,
    "selftest-positional-negative-control": False,  # object form = old bug mode
    "selftest-global-ignore": True,
    "selftest-time-and-amount-tolerance": True,
    "selftest-unordered-list": True,
    "selftest-lenient-list": True,
    "selftest-error-text": True,
    "selftest-both-transport-500": False,           # transport errors stay red
    "selftest-real-diff-control": False,            # genuine diff stays red
}


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def wait_for_server(port: int, process: subprocess.Popen, timeout: float = 10.0):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("stub server exited early")
        try:
            with socket.create_connection(("127.0.0.1", port), timeout=0.2):
                return
        except OSError:
            time.sleep(0.05)
    raise RuntimeError(f"stub server on port {port} did not come up")


def run_comparator(python_url: str, go_url: str, out_dir: str,
                   priority=None, expect_rc=(0, 1)) -> tuple[int, dict, str]:
    """Run the comparator; assert it exited with one of the expected codes.

    Exit-code contract: 0 = all executed cases passed, 1 = at least one
    failure, 2 = no cases executed (empty run)."""
    cmd = [
        sys.executable, COMPARATOR,
        "--config", CASES,
        "--python-url", python_url,
        "--go-url", go_url,
        "--output", out_dir,
        "--no-html",
    ]
    if priority:
        cmd += ["--priority", priority]
    proc = subprocess.run(cmd, capture_output=True, text=True, cwd=TOOL_DIR)
    if proc.returncode not in expect_rc:
        raise RuntimeError(
            f"comparator exited rc={proc.returncode} "
            f"(expected one of {expect_rc}):\n{proc.stderr}")
    reports = sorted(
        name for name in os.listdir(out_dir) if name.startswith("report-")
    )
    if not reports:
        raise RuntimeError("comparator produced no report file")
    with open(os.path.join(out_dir, reports[-1])) as handle:
        return proc.returncode, json.load(handle), proc.stdout + proc.stderr


def check(condition: bool, message: str, failures: list):
    status = "ok" if condition else "FAIL"
    print(f"  [{status}] {message}")
    if not condition:
        failures.append(message)


def main() -> int:
    failures: list[str] = []

    python_port, go_port = free_port(), free_port()
    stubs = [
        subprocess.Popen(
            [sys.executable, STUB, "--port", str(python_port),
             "--script", PYTHON_SCRIPT],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL),
        subprocess.Popen(
            [sys.executable, STUB, "--port", str(go_port),
             "--script", GO_SCRIPT],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL),
    ]
    out_dir = tempfile.mkdtemp(prefix="hivemind-selftest-")

    try:
        wait_for_server(python_port, stubs[0])
        wait_for_server(go_port, stubs[1])
        python_url = f"http://127.0.0.1:{python_port}"
        go_url = f"http://127.0.0.1:{go_port}"

        print("Run 1: full selftest case set")
        rc, report, _ = run_comparator(python_url, go_url, out_dir)

        results = {entry["id"]: entry for entry in report["results"]}
        print(f"  comparator rc={rc} (1 expected: negative controls are red)")

        for case_id, expected_pass in EXPECTED.items():
            entry = results.get(case_id)
            check(entry is not None, f"case ran: {case_id}", failures)
            if entry is None:
                continue
            check(entry["passed"] == expected_pass,
                  f"{case_id}: expected "
                  f"{'PASS' if expected_pass else 'FAIL'}, got "
                  f"{'PASS' if entry['passed'] else 'FAIL'} "
                  f"({entry.get('difference') or entry.get('error')})",
                  failures)

        # 1.1 evidence: the array-form case passed with real results on both
        # sides (no error entries), the object-form control failed because
        # exactly the python side rejected the params (direction locked so
        # even a stub-script mix-up cannot silently pass).
        entry = results["selftest-positional-params"]
        check(entry["python_error"] is None and entry["go_error"] is None,
              "positional case: both sides returned results, no errors",
              failures)
        entry = results["selftest-positional-negative-control"]
        check(entry["difference"] and "one side errored" in entry["difference"],
              "object-form control: exactly one side rejected the params",
              failures)
        check(entry["python_error"] is not None and entry["go_error"] is None,
              "object-form control: the PYTHON (strict) side is the one "
              "that errored (direction locked)",
              failures)

        # PR#420-review evidence: identical transport-level failures on both
        # sides (bare HTTP 500) are judged red, never green.
        entry = results["selftest-both-transport-500"]
        check(not entry["passed"]
              and entry["difference"] and "transport" in entry["difference"],
              "both-500 control: transport-level errors fail red",
              failures)

        # 1.7 evidence: report responses are truncated, detail files are full.
        entry = results["selftest-positional-params"]
        body = entry["python_response"]["body"]
        check("[truncated" in body and len(body) < 300,
              "report embeds truncated long fields (600-char body slimmed)",
              failures)
        detail_path = os.path.join(out_dir, entry["detail_file"])
        check(os.path.isfile(detail_path),
              f"per-case detail file exists: {entry['detail_file']}", failures)
        if os.path.isfile(detail_path):
            with open(detail_path) as handle:
                detail = json.load(handle)
            check(len(detail["python_response"]["body"]) == 600,
                  "detail file keeps the full untruncated response", failures)

        # 1.5 evidence: --priority P0 runs only the P0 case.
        print("Run 2: --priority P0")
        out_dir2 = tempfile.mkdtemp(prefix="hivemind-selftest-")
        _, report2, _ = run_comparator(python_url, go_url, out_dir2,
                                       priority="P0")
        ids2 = [entry["id"] for entry in report2["results"]]
        check(ids2 == ["selftest-positional-params"],
              f"--priority P0 ran exactly the P0 case (ran: {ids2})", failures)
        shutil.rmtree(out_dir2, ignore_errors=True)

        # PR#420-review evidence: both endpoints unreachable (connection
        # refused on both sides) is RED, not "identical error text" green.
        print("Run 3: both endpoints unreachable (connection refused)")
        dead_port = free_port()  # bound+closed: nothing listens here
        dead_url = f"http://127.0.0.1:{dead_port}"
        out_dir3 = tempfile.mkdtemp(prefix="hivemind-selftest-")
        rc3, report3, _ = run_comparator(dead_url, dead_url, out_dir3,
                                         priority="P0")
        entry3 = report3["results"][0]
        check(rc3 == 1 and not entry3["passed"]
              and entry3["difference"] and "transport" in entry3["difference"],
              "connection-refused on both sides: judged red as transport "
              "failure, not green",
              failures)
        shutil.rmtree(out_dir3, ignore_errors=True)

        # PR#420-review evidence: a priority filter matching nothing exits 2
        # with an explicit warning (zero evidence is not "all green").
        print("Run 4: --priority P9 (matches nothing; expect rc=2)")
        out_dir4 = tempfile.mkdtemp(prefix="hivemind-selftest-")
        rc4, report4, output4 = run_comparator(python_url, go_url, out_dir4,
                                               priority="P9", expect_rc=(2,))
        check(rc4 == 2 and report4["summary"]["total"] == 0,
              "empty run: exit code 2 and zero executed cases", failures)
        check("no test cases executed" in output4.lower()
              or "no cases executed" in output4.lower(),
              "empty run: explicit warning printed", failures)
        shutil.rmtree(out_dir4, ignore_errors=True)

        # 1.2 evidence: the global-ignore case passed with the differing
        # field present in both payloads (only the global config masks it).
        detail_path = os.path.join(
            out_dir, results["selftest-global-ignore"]["detail_file"])
        with open(detail_path) as handle:
            detail = json.load(handle)
        check(detail["python_response"]["author"]["internal_id"]
              != detail["go_response"]["author"]["internal_id"],
              "global-ignore case: masked field really did differ "
              "(green comes from the config, not the data)", failures)

        print()
        if failures:
            print(f"SELFTEST FAILED ({len(failures)} check(s))")
            print(f"artifacts kept for debugging: {out_dir}")
            return 1
        print("SELFTEST PASSED: all comparator fixes behave as expected")
        shutil.rmtree(out_dir, ignore_errors=True)
        return 0
    finally:
        for stub in stubs:
            stub.terminate()
        for stub in stubs:
            try:
                stub.wait(timeout=5)
            except subprocess.TimeoutExpired:
                stub.kill()


if __name__ == "__main__":
    sys.exit(main())
