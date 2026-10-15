# loadgen - concurrency load generator

A minimal fixed-rate load harness for the hivemind JSON-RPC API (planned
PR#432, 2026-Q4 deploy plan 05-deploy.md section 2 note 4: the REQUIRED
synthetic-traffic component of the 7-day stability window - "low pressure
all day + daily 1h high-pressure spike" at 10-50 QPS against the
comparison case set).

It is deliberately boring: a ticker dispatches requests at the target rate
to a fixed worker pool, cases are cycled round-robin, and the run ends with
a summary (achieved RPS, error rate, latency percentiles, per-method
table). No external dependencies beyond the Go standard library plus the
YAML parser already in the module graph.

## Build

```bash
go build -o bin/loadgen ./tools/loadgen
```

## Usage

```bash
# Low-pressure profile (all day): 10 QPS for 24h
bin/loadgen --url http://go-api:8080 --rps 10 --duration 24h

# Daily spike profile: 50 QPS for 1h
bin/loadgen --url http://go-api:8080 --rps 50 --duration 1h --concurrency 16

# Drive the FULL api-comparison case set (68+ methods, the same YAML the
# comparator runs - identical traffic as the consistency tests)
bin/loadgen --url http://go-api:8080 --rps 20 --duration 10m \
    --cases tests/api-comparison/test_cases.yaml
```

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--url` | `http://localhost:8080` | JSON-RPC base URL |
| `--rps` | `10` | target requests per second |
| `--duration` | `1m` | run length (interruptible with Ctrl-C) |
| `--concurrency` | `8` | worker pool size |
| `--cases` | *(embedded)* | api-comparison YAML path; empty uses the built-in read-only P0 set |
| `--timeout` | `30s` | per-request HTTP timeout |

Case-set format: the `tests/api-comparison/test_cases.yaml` schema
(`test_cases:` entries with `api` + `params`, positional arrays for
`condenser_api.*`, single objects for `bridge.*`). Any subset or
hand-written list in that format works.

## Reading the output

```
--- loadgen summary ---
requests: 6000 over 10m0s (10.0 rps achieved)
errors: 3 (0.05%)
latency: min 3ms p50 18ms p90 42ms p95 55ms p99 130ms max 410ms
method                                            count   errors      p50      p99
bridge.get_profile                                  1012        1      21ms     98ms
...
```

- **achieved RPS below target** plus `scheduling drops (workers saturated)`
  means the harness (not the server) was the bottleneck - raise
  `--concurrency`.
- **errors** counts transport failures, non-200 responses, and JSON-RPC
  error envelopes alike; the 7-day window targets < 0.1% (05-deploy.md
  section 3).
- Exit code is always 0 after a completed run; gate on the printed numbers
  (or wrap with `grep`) when using this in scripts.

## Smoke-testing the harness itself

Point it at the comparator stub server (no hivemind/DB needed):

```bash
python3 tests/api-comparison/selftest/stub_server.py \
    --port 18500 --script tests/api-comparison/selftest/stub_responses_go.json &
bin/loadgen --url http://localhost:18500 --rps 5 --duration 30s
```

## Stability-window scheduling (reference)

During the observation window the harness runs from the comparison rig:

- continuously: `--rps 10 --duration 24h` (restarted daily or via systemd)
- once per day:  `--rps 50 --duration 1h` (the spike)

Summaries are captured to files (`--duration 1h | tee day3-spike.log`) and
checked against the 05-deploy.md section 3 thresholds (error rate < 0.1%,
no latency-trend anomaly).
