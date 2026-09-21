# Pitfalls — Incident-Derived Traps and Prevention Rules

Each entry follows the same shape: **what happened → why it happened → the
rule that prevents the class**. These are not theoretical; every item traces
to a real defect found in this codebase (2026-09 review) or a real incident
(2026-08-16 security audit). Severity-ordered.

## 1. Self-delegating RPC stubs → stack overflow

**What**: `ranked.go` once had `case "feed": return r.GetAccountPosts(ctx,
params)` — the forwarded arguments were unchanged, so the dispatch variable
could never take a different value, so the call recursed infinitely. Go
stack overflow is a runtime **fatal** error: `gin.Recovery()` cannot catch
it, and a single unauthenticated request killed the entire process
(2026-08-16 security audit; fixed in #395's aftermath).

**Why**: an "approximate placeholder" was used instead of an explicit
not-implemented error.

**Rule**: unimplemented branches return
`apierrors.PublicError("... is not implemented")`. A safe forward must
*inject a constant* (`getDiscussionsBySort(ctx, "trending", params)`) or map
through a fixed whitelist — never re-send the caller's variable unchanged.
Self-check before shipping dispatch code:

```bash
grep -rnE 'return [a-zA-Z]+\.[A-Z][A-Za-z]*\(ctx, params\)' internal/api/ | grep -v _test
```

## 2. custom_json chain-plugin envelopes

**What**: on-chain custom_json payloads for the `follow`/`community`/
`notify` plugins are **array envelopes** — `["follow", {"follower": …,
"following": …, "what": […]}]`, `["subscribe", {"community": …}]`,
`["setLastRead", {"date": "2016-03-24T16:05:00"}]`. The Go code parsed them
as bare objects (`op["type"]`) or skipped the array branch entirely
(`cmd == "follow" → continue`), so **every real follow/community/notify op
was dropped or errored** — follows, community subscriptions, roles, pins,
mutes, and last-read markers never landed in the database.

**Why**: the payload *semantics* were ported but the legacy *unpacking* code
(`custom_op.py::_validate_raw_op` / `valid_op_json`) was not read.

**Rule**: before parsing anything that comes from the chain, read the legacy
unpacking path first. Test fixtures must use real chain shapes (grab samples
from a block explorer or legacy tests), not shapes invented to match the
implementation. Note also: chain timestamps have **no timezone suffix** —
parse with `"2006-01-02T15:04:05"`, never `time.RFC3339`.

## 3. Fake-provider contract drift → green tests, broken production

**What**: `steem.Client.GetContentBatch` sent a single
`condenser_api.get_content` call whose params were an array of
`[author, permlink]` pairs — a contract violation against real steemd (which
wants two strings; legacy uses a true JSON-RPC batch). Every test passed
because `fakeProvider` implemented the *intended* semantics, and the broken
method sat on the main cache-flush path (`cached_post.go::updateBatchTuples`).

**Why**: the fake was written from the same (wrong) mental model as the
client; nothing pinned the client to steemd's actual behavior — `internal/
steem/client.go` has zero tests.

**Rule**: a fake simulates the **real upstream contract**, including its
strictness. Wrappers around external protocols get contract tests (even
one that replays a captured real response). When you add a fake method,
write down where its behavior was verified against the real peer.

## 4. Stateful components constructed twice

**What**: `CustomOpProcessor` constructed its own `NewFollowIndexer(repo,
logger)` while `BlockProcessor` constructed another; sync flushed only the
latter. The former's pending count deltas were never flushed (feature
silently dead) **and** its maps grew without bound (memory leak).

**Rule**: any component with in-memory state (dirty queues, deltas, caches)
is constructed **once** and injected everywhere it is used. Never `New` a
stateful collaborator inside another component's constructor; take it as a
parameter. A cheap guard: a test asserting both accessors return the same
pointer.

## 5. Mixing block-tx and pool connections

**What**: inside the per-block transaction, several indexers read through
`repo` (pool connection) while writing through `tx`: double-promotions in
one block lose updates (stale read); reblog/feed-cache/notification writes
commit immediately and orphan on rollback; accounts registered later in the
same block are invisible to earlier custom ops.

**Rule**: everything the block logically owns reads and writes through the
same `tx`. Extend repositories with a `WithTx(tx)` derivation instead of
adding new pool reads inside block processing. (Legacy wraps the whole
batch in one transaction; the Go per-block transaction is the equivalent
boundary.)

## 6. Swallowing errors without classifying them

**What**: op-processing errors are logged and skipped while the block
commits — a transient statement-timeout permanently loses that operation
(head advances, nothing replays it). Legacy fails the batch, rolls back,
and retries.

**Rule**: in the write path, distinguish **infrastructure errors** (DB,
network, steemd — must fail the block so it retries) from **data errors**
(chain oddities — skip with a warning, as legacy does). When in doubt, fail
the block: a stuck-but-loud indexer beats a silently-diverging one. On the
API side the mirror rule holds: never convert a DB error into an empty
result (`return []{}, nil`).

## 7. Unstable pagination cursors

**What**: single-key `ORDER BY score DESC` with `score <= seek` pagination
duplicates or drops rows across pages when scores tie (they do: massive
zero-payout ties). Legacy explicitly orders by `(field, post_id)` and seeks
with `(field < v OR (field = v AND post_id > id))` — its comments name this
exact edge case.

**Rule**: every cursor query has a deterministic tiebreaker and a compound
seek predicate. Same family: follow lists order by **follow time**, not
account id.

## 8. Validation and limit asymmetry

**What**: input validation landed only on the endpoint family that had
previously caused an incident (#396 fixed `get_discussion`, not its
siblings); several paths clamp only the *upper* bound of `limit`, so
`limit: -1` (or a float overflow during `int(l)`) reaches GORM
`.Limit(-1)` = **no limit** → full-table scans on 2M-row tables.

**Rule**: validation lives in the shared layers (`apierrors` validators at
param parse; `ClampLimit` at every SQL LIMIT — both bounds). New endpoints
reuse them; a handler hand-rolling its own bound check is a review flag.

## 9. Config/deployment name drift

**What**: docker-compose and README used look-alike env names
(`HIVE_STEEM_URL` vs the real `HIVE_STEEMD_URL`, `HIVE_TELEMETRY_PROMETHEUS_*
` vs `HIVE_PROMETHEUS_*`, …) that silently no-op; some declared capabilities
(Prometheus endpoint, W3C propagation extraction, health semantics) did not
match the code.

**Rule**: env names are derived mechanically from snake keys via
`toEnvKey()` — when editing compose/README, derive, don't recall. Config
knobs are registered in **four** places (struct/Load/setDefaults/Validate,
see infrastructure.md). Docs that describe a capability must link to the
code that provides it or mark it planned.

## 10. Mode flags that evaporate

**What**: `isInitialSync` is threaded through every signature but the
producer hard-codes `false`; initial-sync detection keys off "feed cache is
empty", which normal operation also (briefly) satisfies — so a crash mid
initial sync silently changes the program into steady-state mode and the
repair passes (`RecoverMissingPosts`, feed rebuild, force recount) never
run again.

**Rule**: operational mode is **persisted state** (`hive_state`), not an
inferred property of mutable tables or a constructor argument nobody sets.

## 11. Things that look wired up but are not (dead-on-arrival inventory)

Found during the 2026-09 review — verify before relying on any of these,
delete or finish them when touched:

- `CachedPost.DirtyPaidouts` — defined, never called (payout sweep dead).
- `CachedPost.UpdatePromotedAmount` — production path never calls it
  (promoted amounts never reach the cache).
- `StateRepository.Update` — never called (`hive_state` never advances).
- `AccountIndexer.DirtyOldest` — never called (rank/profile refresh dead).
- `api/errors.go` `Error` struct — dead code.
- `-32602/-32663` JSON-RPC codes — defined, never emitted.
- Prometheus metrics recording — works, but **no endpoint exposes it**.
- `HIVE_TRAIL_BLOCKS` — validated, never consumed (Strategy B).
- `SetMaxRetry` in the vendored SDK — dead code upstream (see SDK note in
  infrastructure.md).

## 12. Open known gaps (client-visible)

Tracked here so agents do not accidentally "fix" tests to match them or
promise them to clients; strike entries when fixed:

- JSON-RPC batch requests (arrays) and CORS headers unsupported;
  notifications (no `id`) incorrectly get responses.
- `/health` always 200 (legacy: 500 on DB failure / head age > 1 h);
  `/head_age` endpoint missing.
- Response-shape divergences: condenser post object missing 6 fields and
  curator-payout split; follow lists missing `reputation`/fixed-length
  `what`; `get_account_reputations` key `account` vs legacy `name`;
  `get_profile` flat vs legacy nested; deleted-post sentinels.
- `bridge.normalize_post` is a field-copying placeholder (should be explicit
  not-implemented or a full port).
- Router misses ~10 legacy alias method names (tags_api/follow_api).
- Ranked lists: bridge trending/hot/promoted include comments (no
  `depth = 0`), author-level moderation (`list_type=3`) unfiltered,
  community-page pinned prepend / `tag='all'/'my'` / payout time window
  unimplemented.
- `hive_state` price feeds and `db_version` unmaintained.

## 13. Hygiene debt

`gofmt -l` lists 12 files; CI builds images but never runs `make check`;
several files log without a `component` field. Before finishing any change:
`make fmt lint test`, and keep new code formatted from the first commit —
review diffs of reformatted legacy files separately from semantic changes.
