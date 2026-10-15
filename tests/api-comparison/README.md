# Hivemind API Consistency Test Tool

Compares JSON-RPC responses of the legacy Python hivemind server against the
Go rewrite and produces console/JSON/HTML reports.

## Directory layout

```
tests/api-comparison/
├── api_comparator.py        # comparator core + runner + reports
├── run_tests.sh             # convenience runner (health checks, passthrough;
│                            #   self-anchored to its own directory)
├── test_cases.yaml          # the 68-method cross-service matrix (legacy
│                            #   wire protocol; planned PR#421)
├── test_cases_go_only.yaml  # 9 Go-only methods -- D4 Go-vs-Go self-check
│                            #   track (point both URLs at the Go server)
├── test_api_comparator.py   # unit tests for the comparison core (pytest),
│                            #   including matrix coverage validation
├── selftest/                # selftest/smoke infrastructure (stub servers)
│   ├── stub_server.py       #   stdlib JSON-RPC stub with scripted responses
│   ├── stub_responses_python.json / stub_responses_go.json
│   ├── selftest_cases.yaml  #   reduced case set with expected outcomes
│   └── run_selftest.py      #   boots stubs, runs the tool, asserts outcomes
│                            #   incl. full-matrix reachability
├── requirements.txt         # Python dependencies
├── Dockerfile               # Docker image
├── docker-compose.test.yml  # Docker Compose config (legacy path via
│                            #   $LEGACY_CONTEXT; results/ volume-mounted)
├── TEST_PLAN.md             # test plan (English, priorities aligned to the
│                            #   2026-Q4 §二 matrix)
├── API_ENDPOINTS.md         # endpoint inventory
└── results/                 # report + per-case detail output directory
                            #   (git-ignored; kept via results/.gitkeep)
```

## Case coverage (2026-Q4 authoritative matrix)

`test_cases.yaml` covers every one of the 68 method names the legacy
server registers (serve.py `build_methods`): condenser_api x26,
follow_api/tags_api aliases x21 (one reachability case per alias),
bare `call` x1, bridge x19, hive x1 — each with at least one
protocol-reachable case, plus cursor/pagination second requests for the
paging families. `pytest test_api_comparator.py` fails when a matrix
method loses its case, and the selftest asserts end-to-end reachability
of the full set against strict/lenient stubs.

## Quick start

### 1. Install dependencies

```bash
cd tests/api-comparison
pip install -r requirements.txt
```

### 2. Start the API services

Both servers must run against the **same database**:

```bash
# Python legacy (port 8081)
cd /path/to/hivemind_legacy
pip install -e .        # installs the `hive` console script (setup.py)
hive server \
    --database-url=postgresql://user:pass@localhost:5432/hivemind \
    --http-server-port=8081
# (equivalently: DATABASE_URL=... HTTP_SERVER_PORT=8081 hive server)

# Go (port 8082)
cd /path/to/hivemind
make run-server
```

Note: the legacy package has no `python -m hive.server.http_server`
module. Its entry point is the `hive` console script (`hive.cli`, modes:
`server` / `sync` / `status`) installed by the legacy `setup.py`.

### 3. Run the comparison

```bash
./run_tests.sh                       # everything
./run_tests.sh --priority P0         # P0 subset only
python3 api_comparator.py --priority P0,P1 --output results
```

Exit code is `0` when no UNEXPECTED case failed (registered
`expected_diff` cases are counted separately and never fail the run), `1`
when at least one unexpected case failed, and `2` when no case executed
at all (e.g. a `--priority` filter that matched nothing -- zero evidence
must never read as "all green" in CI).

The Go-only track (decision D4) runs the same tool in Go-vs-Go mode:

```bash
python3 api_comparator.py --config test_cases_go_only.yaml \
    --python-url http://localhost:8082 --go-url http://localhost:8082
```

## Tool selftest (no services or database needed)

```bash
python3 -m pytest test_api_comparator.py   # unit tests for the compare core
python3 selftest/run_selftest.py           # end-to-end smoke against stubs
```

The selftest boots two loopback stub servers (one strict like the legacy
dispatcher, one lenient like the Go server) and proves the core behaviors:
positional params, global config application, timestamp/amount tolerance,
list modes, report slimming, priority filtering, plus the negative
controls: transport-level failures (both sides 500 / unreachable / 302
redirect) stay red, a 200 response without a JSON-RPC envelope (the
health-body shape) is rejected on both sides, the object-form rejection
is locked to the python side, and an empty run exits 2 with an explicit
warning. Since planned PR#421 it also runs a full-matrix reachability
pass: every case in `test_cases.yaml` (68 methods) and
`test_cases_go_only.yaml` (9 methods) must run green against catch-all
stubs, with the strict stub enforcing the legacy param shape — a wrong
wire form in the YAML errors on exactly the python side and turns red.

## Command line options

```
python3 api_comparator.py [options]

  --config FILE          case YAML file (default: test_cases.yaml)
  --python-url URL       Python (legacy) API URL (default: http://localhost:8081)
  --go-url URL           Go API URL (default: http://localhost:8082)
  --timeout SECONDS      request timeout (default: 30)
  --output DIR           output directory (default: results)
  --priority P0,P1,...   only run cases with these priorities
  --no-html              skip HTML report generation
```

## Case file format

Parameter shapes follow the **legacy server's wire protocol** (both servers
must accept them): `condenser_api.*` cases use positional arrays (discussion
queries use the one-element nested-query array), `bridge.*` cases use object
params whose keys match the legacy method signatures.

```yaml
config:
  # Global options, applied to every case (fix: top-level config is honored).
  ignore_fields: []            # path-aware patterns, see below
  tolerance:
    float_epsilon: 0.001       # numeric epsilon; also used for amount strings
    timestamp_seconds: 2       # timestamp comparison tolerance
    timestamp_fields: []       # extra WHOLE field names whose numbers are
                               # compared with the time tolerance (e.g.
                               # head_stamp); default time tokens are
                               # built in, see Tolerance semantics

test_cases:
  - id: condenser-get-content   # stable id; names the per-case detail file
    name: condenser_api.get_content steemit/firstpost
    api: condenser_api.get_content
    priority: P0                # P0/P1/P2; used by --priority
    params: ["steemit", "firstpost"]
    # expected_diff: true       # REGISTERED expected difference: counted in
                                #   a separate bucket, never a failure (e.g.
                                #   get_state, normalize_post baseline #23)
    # ignore_fields: [...]      # per-case: REPLACES the global list
    # tolerance: {...}          # per-case: merged key-wise over global
    # unordered: true           # top-level list: sort by business key first
    # unordered_key: author
    # lenient_list: true        # top-level list: length + order-insensitive
                                # one-to-one matching
```

### ignore_fields (path-aware)

| Pattern      | Matches                                            |
|--------------|----------------------------------------------------|
| `name`       | a dict key `name` at any depth (legacy deep match) |
| `a.b.c`      | only the exact path `a -> b -> c` from the root    |
| `*`          | any single segment (dict key or list index)        |
| `**`         | zero or more segments                               |

Path scoping keeps a global entry such as `author.id` from masking business
`id` fields at other paths; list indices are normalized to `*` so
`items.*.id` covers every element. Per-case `ignore_fields` replaces the
global list; per-case `tolerance` merges key-wise over the global one.

### Tolerance semantics

* Numbers compare within `float_epsilon`.
* Strings that both parse as ISO8601 (with/without `Z`, with offset, naive,
  space separator, fractional seconds) compare within `timestamp_seconds`
  after normalization to epoch seconds.
* Numbers at timestamp-ish field names compare within `timestamp_seconds`.
  A field is timestamp-ish when its name is exactly a time token
  (`created`, `time`, ...) or ends with `_<token>` (`db_head_age`,
  `cashout_time`, `last_read_at`); compound counters such as
  `created_block` or `read_count` are NOT timestamps and compare with the
  plain epsilon. Fields outside the built-in token set can be added via
  `tolerance.timestamp_fields` (global or per-case; entries match the
  whole field name, unlike the token suffix rule). Epoch comparison
  additionally requires both values >= 1e8 (plausible epoch seconds;
  values above 1e12 count as epoch milliseconds). `*_age` fields hold
  small second durations and compare by absolute difference. Plain
  numeric fields are never treated as timestamps.
* Amount strings (`"1.500 SBD"`, `"$0.99"`) parse to (value, unit) and
  compare numerically within `float_epsilon` only when the currency units
  match (case-insensitive; `"$"` matches only `"$"`). An amount string
  never bridges to a bare number: the number side carries no unit to
  verify, so that pair is reported as a type mismatch.
* JSON-valued strings: when both sides parse as JSON and at least one
  parses to an object/array, the parsed values are compared recursively.
  Registered normalization (planned PR#421): payload strings such as
  community `settings` render key order differently (Go alphabetical,
  legacy insertion-ordered); scalar JSON strings stay plain strings.
* When both endpoints return JSON-RPC **application** errors, the error
  texts are compared (used by dummy methods such as `get_account_votes`).
  Registered whitelist (planned PR#421): `error.code` and `error.data`
  are never compared — legacy's debug dispatch carries
  `"ClassName: <msg>"` data on -32000 responses while Go omits data on
  -32000 entirely; the message layer is the contract.
  Transport-level failures (connection refused, timeout, HTTP 5xx,
  malformed response) are never judged green, even when both sides fail
  identically -- identical transport errors carry no consistency evidence.

### List modes

Top-level response lists compare positionally by default. Enable per case
only where the legacy behavior itself is unordered, and record the reason in
a YAML comment:

* `unordered: true` (+ optional `unordered_key`): sort both sides by the
  business key, group equal keys, and compare groups element-wise; groups
  with more than one element use maximum bipartite matching so two
  elements sharing a business key cannot be zipped into the wrong partner.
* `lenient_list: true`: same length plus order-insensitive one-to-one
  element matching via maximum bipartite matching (Kuhn's algorithm) —
  greedy first-match pairing could strand an element whose only partner
  was already taken and false-red lists that DO have a perfect matching
  (planned PR#421 obligation ⑥).

### Reports

* Console: summary (overall and per-priority, unexpected-failure count,
  expected-diff bucket) plus failures with diffs and the separate
  "Expected differences (registered)" section.
* `results/report-<timestamp>.json` / `.html`: embedded responses are
  **truncated** (long string fields cut at 200 chars) to keep reports
  readable.
* `results/case-<id>.json`: one full, untruncated result per case (both
  responses + diff), referenced from the reports.

`results/` contents are git-ignored (the directory itself is kept via
`results/.gitkeep`); commit run artifacts only by explicit copy.

## Docker

```bash
# LEGACY_CONTEXT parameterizes the legacy checkout path (defaults to
# /home/ety001/workspace/hivemind_legacy on this machine).
LEGACY_CONTEXT=/path/to/hivemind_legacy \
  docker-compose -f docker-compose.test.yml up -d
docker-compose -f docker-compose.test.yml logs -f test-runner
docker-compose -f docker-compose.test.yml down
```

## Troubleshooting

* **Connection refused** – check that both servers are running and the ports
  match (`--python-url` / `--go-url`).
* **Data mismatch** – both servers must point at the same database; check the
  diff path in the console report and open the case detail file for the full
  payloads. Tune with `ignore_fields`/`tolerance` only for legitimate format
  drift, not to mask real regressions.
* **Timeouts** – raise `--timeout`, or check server load.
