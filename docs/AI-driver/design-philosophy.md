# Design Philosophy

The rules below are the load-bearing decisions of this codebase. They exist
either because the legacy Python implementation encodes them (parity rules)
or because violating them has already caused incidents (discipline rules).
An agent that understands *why* each rule exists will apply it correctly in
situations the rule does not literally cover.

## 1. Legacy Python is the behavioral specification

The Go codebase is a rewrite, not a redesign. The Python reference at
`/home/ety001/workspace/hivemind_legacy` defines expected behavior, business
logic, and (via its SQL) even query semantics.

Consequences for agents:

- Before implementing any handler or indexer path, locate the legacy
  counterpart (module mapping table in `AGENTS.md`; e.g.
  `internal/api/condenser/` ↔ `hive/server/condenser_api/`). Port the
  behavior — including edge cases like *which* empty shape is returned
  (`[]` vs `{}` vs a sentinel object like `{id: 0, author: '', permlink: ''}`),
  *which* error text the client sees, and sort/tiebreak order.
- When Go and legacy genuinely must diverge (e.g. a legacy bug that is
  deliberately fixed, like the swapped role-ID map), record the divergence in
  a code comment *at the divergence site*.
- "Same result shape" is part of parity: field names (`name` vs `account`),
  fixed-length arrays (`what: ['', '']`), timestamp formats, and null-vs-
  empty-string choices are all client-visible contracts.

## 2. Explicit not-implemented over silent approximation

Every unimplemented branch must fail loudly:

```go
return nil, apierrors.PublicError("get_state is not implemented; use bridge/condenser methods instead")
```

Never bridge the gap with "roughly similar" logic, and **never forward the
original `params` to the same or a sibling entry point**. The canonical
incident: `ranked.go` once had `case "feed": return r.GetAccountPosts(ctx,
params)` — the argument was unchanged, so the dispatch variable could never
change, so the call recursed infinitely. Go stack overflow is a runtime fatal
error that `gin.Recovery()` cannot catch: **one unauthenticated request killed
the whole process** (2026-08-16 security audit).

Self-check before shipping any dispatch code:

```bash
grep -rnE 'return [a-zA-Z]+\.[A-Z][A-Za-z]*\(ctx, params\)' internal/api/ | grep -v _test
```

A safe forward *injects a constant* (`getDiscussionsBySort(ctx, "trending", params)`)
or maps through a whitelist (`sortMap`); an unsafe one re-sends the caller's
variable unchanged.

## 3. Single source-of-truth registries

Discovery of "what exists" is never inferred by scanning; it lives in
enumerated registries:

| Concern | Registry |
|---------|----------|
| RPC methods ↔ handlers | `internal/api/router.go::registerMethods()` |
| Chain operation dispatch | `internal/indexer/block_processor.go::processOperation()` `switch` |
| custom_json operation IDs | `internal/indexer/custom_op.go::ProcessOps()` |
| Community actions | `internal/indexer/community_indexer.go::ProcessCommunityOp()` |
| DB schema | `internal/db/migrations/*.sql` (embedded; models must track them) |

When adding a capability, extend the registry rather than building a parallel
path (a second map, a naming convention, reflection). When a registry entry is
missing, that is a bug in itself — e.g. an implemented handler that was never
registered in `router.go` does not exist for clients.

## 4. Transaction discipline in the indexer

The block processor runs **one DB transaction per block**. Three rules follow:

1. **Everything the block logically owns writes through `tx`.** A write that
   bypasses the transaction (via the repository's pool connection) commits
   immediately: if the block later rolls back, the write is orphaned; and it
   cannot see uncommitted writes from earlier ops in the same block.
2. **Read-modify-write sequences must read through the same connection they
   write on.** Reading via the pool and saving via `tx` loses updates when
   the same row is touched twice in one block (e.g. two transfers promoting
   the same post).
3. **Deferred state is queued, not skipped.** Expensive `steemd` round-trips
   (post cache cores, account refreshes, follow recounts) are queued as dirty
   entries and flushed *after* the batch commits — mirroring legacy
   `process_multi`'s flush points. Never do a steemd call inside the block
   transaction.

Repository methods currently take a context and use the pool; the block path
passes `tx` explicitly to the ops that support it. Extending tx-awareness to
the remaining repositories is an accepted (and tracked) piece of debt — new
indexer code must not add new pool writes inside block processing.

## 5. Fail closed on infrastructure, fail open on chain oddities

- Transient infrastructure errors (DB timeouts, connection drops) during op
  processing must **fail the block so it retries** — legacy rolls the whole
  batch back and re-processes. Silently skipping an op makes the divergence
  permanent: the block row commits, head advances, and nothing will ever
  replay that operation.
- Malformed chain data (a weird custom_json, a vote on a missing post) is
  skipped with a warning, as legacy does — the chain is the input, not the
  enemy.
- The distinction is *where the error came from*, and current code does not
  yet classify it perfectly (see pitfalls); when in doubt, prefer failing the
  block over losing data.

## 6. Validation at the boundary, sanitization at the exit

- User input entering SQL is validated with the `apierrors` validators
  (`ValidAccount`, `ValidPermlink`, `ValidLimit`) ported from legacy
  `helpers.py`; cursor queries clamp limits with `ClampLimit` before
  `LIMIT`.
- Error messages leaving the process are sanitized in exactly one place:
  `jsonrpc.go::sendError` puts a message into `error.data` **only** if the
  error is an `apierrors.PublicError`. Internal errors (SQL text, driver
  details) reach the client as a bare `-32000 Server error` while the full
  error is logged and spanned.
- Therefore: anything a client is *meant* to read (validation refusals,
  "account not found", not-implemented notices) must be constructed with
  `apierrors.PublicError/Publicf`, not `fmt.Errorf`.

## 7. Derived data is disposable; facts are not

`hive_posts_cache`, `hive_feed_cache`, tag rows, and count columns exist to
make reads fast. Everything derived can in principle be rebuilt from
`steemd` + the fact tables — the initial-sync rebuild, `RecoverMissingPosts`,
and the audit jobs all rely on this. Practical corollaries:

- New API read paths should read derived tables (that is where the wide,
  sorted data lives), not compute from facts per-request.
- New indexer write paths must keep fact writes transactional and queue the
  derived-table updates through the existing flush machinery; a fact written
  without its derived updates will simply look stale until an audit repairs
  it, but a derived update written for a rolled-back fact corrupts reads.

## 8. Observability is part of the interface

- Every RPC method gets a span automatically from the dispatcher; handlers
  may add children but must not create duplicate same-named spans.
- Loggers are obtained via `logging.GetLogger().With(zap.String("component",
  "..."))`. A log line without a component is a bug.
- Metrics go through `telemetry.Record*` helpers, never direct instrument
  mutation, so naming stays centralized.

## 9. Conventions with teeth

- **Language**: English for code comments, docs, commit messages, error
  strings. (Conversation with the user: Chinese.)
- **Style**: `gofmt` + `golangci-lint`; `make fmt lint` before commit.
- **Commits**: conventional-commit prefixes (`feat(api): …`), never commit
  without explicit user approval; PRs target `master`, work happens on
  feature branches.
- **Config**: new knobs are registered in **four** places — struct field,
  `Load()` getter-with-default, `setDefaults()` viper default, `Validate()`
  range check — plus the `AGENTS.md`/README tables. A knob registered in
  only one place silently diverges between viper lookups and struct reads.

## 10. What "done" means

A feature is done when: the legacy counterpart was consulted, the registry
entries were added, tests exercise the behavior (not just the happy path),
`make check` passes, docs touched by the change are updated, and any known
gap left behind is written down (TODO comment with intent + a
`pitfalls.md`/`AGENTS.md` note), never silently approximated.
