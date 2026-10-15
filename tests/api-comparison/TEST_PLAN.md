# Hivemind API Consistency Test Plan

## Overview

This document describes how the Go rewrite of the Hivemind API is verified
against the legacy Python API for behavioral consistency (2026-Q4 KR4).

Source of truth for scope and priorities: `2026-Q4/04-comparison.md` §二
(the authoritative comparison matrix). This plan restates it for day-to-day
use; when the two disagree, §二 wins.

## Goals

1. Verify that the Go API returns the same structures, fields and values
   as the Python API.
2. Ensure every endpoint in the comparison matrix behaves correctly.
3. Discover and fix behavioral differences; register the ones that are
   deliberate (expected differences) instead of silently passing them.
4. Provide evidence and confidence for the production migration.

## Architecture

```
┌─────────────────┐     ┌─────────────────┐
│  Python server  │     │   Go server     │
│  (legacy)       │     │   (rewrite)     │
│  Port: 8081     │     │  Port: 8082     │
└────────┬────────┘     └────────┬────────┘
         │                       │
         └──────────┬────────────┘
                    │
            ┌───────▼────────┐
            │  Comparator    │
            │  (Python)      │
            │  reports diff  │
            └────────────────┘
```

### Shared database

Both servers connect to the SAME PostgreSQL database so responses are
deterministic for a given block state. In the 2026-Q4 runs this is the
test-environment hivemind RDS (04 §三); for local smoke testing,
`docker-compose.test.yml` brings up one shared PostgreSQL.

## Endpoint Matrix (authoritative, 68 legacy-registered method names)

The legacy server (`hive/server/serve.py build_methods`) registers exactly
68 method names; cross-service comparison is only possible for those. The
Go-only surface (below) is verified on a separate track (decision D4).

### Hive core (1)

| API method | Purpose | Priority |
|------------|---------|----------|
| `hive.db_head_state` | DB head block/time/age | P0 |

### Condenser API (26)

| API method | Purpose | Priority |
|------------|---------|----------|
| `condenser_api.get_followers` | Followers list | P0 |
| `condenser_api.get_following` | Following list | P0 |
| `condenser_api.get_follow_count` | Follow counts | P0 |
| `condenser_api.get_reblogged_by` | Rebloggers of a post | P0 |
| `condenser_api.get_followers_by_page` | Followers, page window (EOL) | P1 |
| `condenser_api.get_following_by_page` | Following, page window (EOL) | P1 |
| `condenser_api.get_content` | Single post | P0 |
| `condenser_api.get_content_replies` | Direct replies | P1 |
| `condenser_api.get_state` | Retired-frontend state monolith | P2 (expected difference) |
| `condenser_api.get_discussions_by_trending` | Trending discussions | P0 |
| `condenser_api.get_discussions_by_hot` | Hot discussions | P0 |
| `condenser_api.get_discussions_by_created` | Newest discussions | P0 |
| `condenser_api.get_discussions_by_promoted` | Promoted discussions | P0 |
| `condenser_api.get_discussions_by_blog` | Account blog | P0 |
| `condenser_api.get_discussions_by_feed` | Account feed | P0 |
| `condenser_api.get_discussions_by_comments` | Comments by author | P1 |
| `condenser_api.get_replies_by_last_update` | Replies to author | P1 |
| `condenser_api.get_discussions_by_author_before_date` | Author history (date ignored) | P1 |
| `condenser_api.get_post_discussions_by_payout` | Posts by payout | P1 |
| `condenser_api.get_comment_discussions_by_payout` | Comments by payout | P1 |
| `condenser_api.get_blog` | Blog entries (full posts) | P0 |
| `condenser_api.get_blog_entries` | Blog entries (lite) | P1 |
| `condenser_api.get_trending_tags` | Trending tags | P1 |
| `condenser_api.get_account_reputations` | Account reputations | P1 |
| `condenser_api.get_transaction` | Transaction lookup (needs steemd) | P2 (env-gated) |
| `condenser_api.get_account_votes` | Dummy (both sides error) | P2 |

### follow_api / tags_api aliases (21)

Every alias binds the same legacy handler as its condenser counterpart
(one reachability case per alias; deep re-runs are wasteful):

- `follow_api.*` (9): `get_followers`, `get_following`,
  `get_followers_by_page`, `get_following_by_page`, `get_follow_count`,
  `get_account_reputations`, `get_blog`, `get_blog_entries`,
  `get_reblogged_by` — P1 (dummy `tags_api.get_account_votes` is P2).
- `tags_api.*` (12): `get_discussion` (alias of `get_content`),
  `get_content_replies`, `get_discussions_by_trending`, `_hot`,
  `_promoted`, `_created`, `_blog`, `_comments`,
  `_author_before_date`, `get_post_discussions_by_payout`,
  `get_comment_discussions_by_payout`, `get_account_votes` — P1 (the
  account-votes dummy is P2).

### Bare `call` (1)

| API method | Purpose | Priority |
|------------|---------|----------|
| `call` | Legacy `["condenser_api", method, params]` adapter | P1 |

### Bridge API (19)

| API method | Purpose | Priority |
|------------|---------|----------|
| `bridge.get_post` | Single post (bridge shape) | P0 |
| `bridge.get_discussion` | Full discussion tree | P0 |
| `bridge.get_ranked_posts` | Ranked posts (trending/hot/created/promoted/payout/muted) | P0 |
| `bridge.get_account_posts` | Account posts (posts/comments/replies/...) | P0 |
| `bridge.get_profile` | Account profile | P0 |
| `bridge.normalize_post` | Normalize a steemd post object | P1 (expected difference until baseline #23) |
| `bridge.get_post_header` | Minimal post header | P1 |
| `bridge.get_trending_topics` | Trending topics | P1 |
| `bridge.get_payout_stats` | Payout statistics | P1 |
| `bridge.get_community` | Community by name | P0 |
| `bridge.get_community_context` | Account's role/subscription in a community | P1 |
| `bridge.list_communities` | Community list | P0 |
| `bridge.list_pop_communities` | Popular communities | P1 |
| `bridge.list_community_roles` | Community roles | P1 |
| `bridge.list_subscribers` | Community subscribers | P1 |
| `bridge.list_all_subscriptions` | Account's subscriptions | P1 |
| `bridge.account_notifications` | Account notifications | P0 |
| `bridge.unread_notifications` | Unread counts | P0 |
| `bridge.post_notifications` | Notifications about a post | P1 |

### Go-only methods (9) — decision D4, separate track

Legacy never registers `hive_api.*` nor `bridge.list_top_communities`;
they cannot be compared cross-service. They are covered by Go-side
real-PG integration tests plus a manual cross-read against
`hivemind_legacy/hive/server/hive_api/{public,objects}.py` (W5 memo), and
one reachability case each lives in `test_cases_go_only.yaml` for Go-vs-Go
self-regression runs (point both `--python-url` and `--go-url` at the Go
server).

### Priority definitions

- **P0** (core; the W7 acceptance gate requires 100%): `get_content`,
  `get_discussions_by_{trending,hot,created,promoted,blog,feed}`,
  `get_blog`, `get_followers`/`get_following`/`get_follow_count`,
  `get_reblogged_by`, `bridge.get_post`/`get_discussion`/
  `get_ranked_posts`/`get_account_posts`/`get_profile`,
  `bridge.list_communities`/`get_community`,
  `bridge.account_notifications`/`unread_notifications`,
  `hive.db_head_state` — 22 methods.
- **P1**: the remaining condenser query methods, the remaining bridge
  methods, and the alias reachability cases.
- **P2**: registered expected differences (`get_state`,
  `bridge.normalize_post` while baseline #23 is open), dummy-method error
  text (`get_account_votes`), and env-gated `get_transaction` (requires a
  configured steemd endpoint on both servers).

## Test Environment

### Environment variables

```bash
# Python (legacy) API
PYTHON_API_URL=http://localhost:8081

# Go API
GO_API_URL=http://localhost:8082

# Shared database (both servers)
DATABASE_URL=postgresql://user:pass@localhost:5432/hivemind

# Tool settings
TEST_TIMEOUT=30
TEST_PRIORITY=            # e.g. P0 or P0,P1
```

### Docker Compose

`tests/api-comparison/docker-compose.test.yml` brings up PostgreSQL,
Redis, both API servers and the runner. The legacy checkout path is
parameterized (`LEGACY_CONTEXT`, 04 §一 1.8) so the compose file carries
no machine-specific absolute paths:

```bash
LEGACY_CONTEXT=/path/to/hivemind_legacy \
  docker compose -f tests/api-comparison/docker-compose.test.yml up -d
```

## Execution Flow

1. **Prepare**: start the shared database and both API servers; verify
   `/health` on each.
2. **P0 pass**: run the P0 subset (`--priority P0`).
3. **P1 pass**: `--priority P1`.
4. **P2 pass**: `--priority P2`.
5. **Reports**: JSON/HTML reports and per-case detail files land in
   `results/` (git-ignored; the directory is kept via `results/.gitkeep`).

Full-matrix run: `bash run_tests.sh --python-url ... --go-url ...`
(no `--priority`) executes all cases.

## Comparison Strategy

### Exact matching

Scalars and lists compare exactly:

- numbers: `==` (within epsilon, see below)
- strings: `==` (with the normalizations below)
- booleans: `==` (bool never equals 1)
- null: `==`

### Tolerant matching

Numeric, timestamp and amount drift compares within tolerances
(04 §五):

- numbers: `float_epsilon` (default 0.001)
- timestamps: normalized to epoch, compared within `timestamp_seconds`
  (default 2s); ISO8601 variants (with/without Z, offsets, naive, space
  separator, date-only) and epoch seconds/milliseconds are recognized.
  Numeric fields compare as timestamps only at time-ish field names
  (`created`, `db_head_age`, `cashout_time`, ...); compound counters
  (`created_block`, `read_count`) stay plain numbers.
- amount strings: parsed to (value, unit) and compared numerically within
  `float_epsilon` ("1.500 SBD" == "1.5 SBD"); the currency must match
  (case-insensitive; "$" matches only "$"); amount strings never bridge
  to bare numbers.
- JSON-valued strings: when both sides parse as JSON and at least one is
  an object/array, the parsed values are compared recursively (community
  `settings` payloads render key order differently: Go alphabetical,
  legacy insertion-ordered — registered normalization, planned PR#421).

### Path-aware ignores

`ignore_fields` patterns are path-aware (see `IgnoreMatcher`):
a bare name matches that key at any depth (use sparingly); a dotted path
matches one exact path; `*` and `**` wildcards are supported; list
indices normalize to `*`. Nothing is ignored globally today — on a shared
database business ids must match.

### Deep comparison

Nested objects/arrays compare recursively; top-level lists default to
strict positional comparison with optional per-case `unordered` (sort by
a business key, group equal keys, maximum bipartite matching within each
group) and `lenient_list` (length + maximum-bipartite one-to-one
matching). Both modes use maximum matching rather than greedy pairing so
a valid pairing is never false-redded (planned PR#421 ⑥). Use them only
where legacy itself is unordered, with a per-case justification comment.

### Error comparison

When both endpoints return JSON-RPC application-layer errors, the error
**messages** are compared. Registered whitelist (planned PR#421): neither
`error.code` nor `error.data` is compared — legacy (debug dispatch)
always carries `data` such as `"ApiError: <msg>"` on -32000 responses
while Go omits data on -32000; the message layer is the contract.
Transport-level failures (connection refused, timeout, HTTP status
error, malformed response) are never judged green, even when identical.

### Expected differences (`expected_diff: true`)

Cases whose difference is REGISTERED (get_state: legacy serves the
retired-frontend monolith while Go answers -32000 not-implemented;
normalize_post: Go binds the post object at the wrong level, baseline
#23) are marked `expected_diff: true` in the YAML. The comparator counts
them in a separate bucket — they never count as failures and never fail
the exit code — and reports each as "diffing" or "RESOLVED" (a resolved
entry means the registry can be pruned).

## Test Case Configuration

Case files: `tests/api-comparison/test_cases.yaml` (the 68-method
cross-service matrix) and `tests/api-comparison/test_cases_go_only.yaml`
(D4 Go-vs-Go track).

Parameter shapes follow the legacy wire protocol: condenser-family and
`call` cases use positional arrays (discussion queries in the steemd
nested-query form `[query]`); bridge cases use kwargs objects whose keys
match the legacy signatures. Sample parameters (1.10) are
protocol-legal typical samples (steemit/firstpost, hive-139531); cursor
cases use seek semantics so they stay deterministic on any shared
database — adjust samples to real rows when a run needs non-empty page-N
results.

```yaml
config:
  ignore_fields: []            # path-aware
  tolerance:
    float_epsilon: 0.001
    timestamp_seconds: 2

test_cases:
  - id: "condenser-get-content"          # stable id (detail file names)
    name: "condenser_api.get_content steemit/firstpost"
    api: "condenser_api.get_content"
    priority: "P0"
    params: ["steemit", "firstpost"]     # legacy wire form
    # expected_diff: true                # registered, counted separately
    # ignore_fields: [...]               # per-case override (replaces)
    # tolerance: {...}                   # per-case override (merges)
    # unordered: true / unordered_key: "author"
    # lenient_list: true
```

Tool self-tests (no real services needed):

```bash
cd tests/api-comparison
python3 -m pytest test_api_comparator.py    # comparator core + matrix coverage
python3 selftest/run_selftest.py            # stub end-to-end smoke incl.
                                            # full-matrix reachability
```

### Exit codes

`api_comparator.py` and `run_tests.sh` share the contract:

- `0` = no unexpected failure (registered expected-diff cases are
  reported separately and never fail the run)
- `1` = at least one unexpected failure
- `2` = no cases executed (e.g. an empty `--priority` filter — zero
  evidence must never read as "all green")

## Failure Handling

### Failure categories

1. **Connection failure**: an API endpoint is unreachable.
2. **Structural difference**: response shapes differ.
3. **Value difference**: values differ beyond tolerance.
4. **Type difference**: value types differ.
5. **Timeout**: response too slow.

### Failure reports

Each failed case records: the API method, the request params, the
expected (Python) and actual (Go) responses (full payloads in the detail
file), the difference path, and the duration. Differences are triaged
into Go bug / tool noise / legacy quirk (04 §三); deliberate divergences
are registered as `expected_diff` with a reason, never silently ignored.

## Continuous Integration

CI does not run cross-service comparison (it depends on an internal
database). The comparator's pure comparison core and the stub-driven
selftest (including full-matrix reachability) run in CI instead; a
Go-vs-Go `--smoke` nightly is an optional W8 addition.

## Success Criteria

Phased targets (04 §三 judgment ladder):

- **W4 smoke**: P0 subset green except the registered expected-red list.
- **W6 full run**: difference inventory over the full 68-method matrix,
  classified by priority and root cause.
- **W7 acceptance**: P0 100% AND overall >95% (P0 100% with overall
  90-95% and non-core >90% escalates to decision point D6).

Release bar: all P0/P1 endpoints 100% (P1 expected-diff entries resolved
or re-registered through review); P2 at least 95%; performance not worse
than 80% of the Python server.
