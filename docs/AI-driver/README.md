# AI-Driver Docs — Engineering Reference for AI Agents

This directory is the **authoritative orientation layer for AI agents (and new
contributors) developing features in the Go hivemind codebase**. Its purpose:

1. Preserve the intended architecture so that agent-generated changes do not
   silently break it.
2. Point at the components that are *designed for reuse*, so agents extend
   instead of re-inventing.
3. Record the traps that have already been hit (some of them
   process-crashing), so they are not re-hit.

> **Read order for any agent starting work here:**
> `design-philosophy.md` → `architecture-overview.md` → the module doc that
> matches the change → `pitfalls.md` before writing the final diff.
>
> **Non-negotiable companion:** the repository root `AGENTS.md` (local,
> untracked). Where the two disagree, `AGENTS.md` wins and this directory must
> be fixed.

## Document map

| Document | What it covers |
|----------|----------------|
| [design-philosophy.md](design-philosophy.md) | The rules the codebase is built on: legacy-parity-first, explicit-not-implemented, single source-of-truth registries, transaction discipline, observability conventions, language/comment/commit conventions |
| [architecture-overview.md](architecture-overview.md) | The two binaries, the read path, the write path, the derived-data model (facts vs cache), the dirty-queue/flush pipeline, deployment topology |
| [api-layer.md](api-layer.md) | JSON-RPC dispatcher, method registry, the three params forms, error sanitization (`apierrors.PublicError`), the dual post-object shapes, how to add an RPC method |
| [indexer.md](indexer.md) | Sync loop (Strategy B), BlockProcessor transaction model, entity indexers, CachedPost dual-write and flush levels, follow deltas, account flush, cache audit jobs, how to add support for a new chain operation |
| [data-layer.md](data-layer.md) | GORM setup, repository pattern (and its documented escape hatches), migration framework and baseline mechanism, model↔schema alignment tests, Redis cache, how to add a table |
| [infrastructure.md](infrastructure.md) | Config system (Viper, `HIVE_` env prefix, the double-registration rule), steemd client and its known SDK limits, telemetry/logging, binary lifecycle, docker-compose |
| [pitfalls.md](pitfalls.md) | Incident-derived traps: the ranked self-recursion crash, custom_json envelope formats, tx-vs-pool connection mixing, mass-assignment, stale cursors, and the checklist that prevents each class |

## The five ground rules (short form)

These are elaborated in [design-philosophy.md](design-philosophy.md); they are
repeated here because they gate every change:

1. **Legacy Python is the behavioral spec.** The reference lives at
   `/home/ety001/workspace/hivemind_legacy`. When Go behavior is unclear,
   read the Python, do not guess. Behavioral parity beats local elegance.
2. **Unimplemented means explicitly unimplemented.** Return a
   `not implemented` error (or a documented, legacy-agreed empty shape).
   Never approximate with "similar logic", and never forward raw `params` to
   a sibling entry point — that is how the
   [self-recursion stack-overflow incident](pitfalls.md#1-self-delegating-rpc-stubs--stack-overflow)
   happened.
3. **One registry per concern.** RPC methods: `internal/api/router.go::
   registerMethods()`. Chain ops: the `switch` in `block_processor.go::
   processOperation()`. Schema: `internal/db/migrations/`. Add to the
   registry; do not create parallel discovery paths.
4. **Writes inside the block transaction, reads outside it must never be
   mixed naively.** The block processor runs one DB transaction per block;
   repository calls that use the pool connection cannot see (and can
   partially duplicate or orphan) writes made inside that transaction. See
   [pitfalls.md](pitfalls.md#4-mixing-tx-and-pool-connections-in-block-processing).
5. **English for code comments, docs, commit messages, and error strings.**
   User-facing conversation defaults to Chinese. Conventions live in
   `AGENTS.md` and `.cursor/rules/core.mdc`.

## Status caveat

These docs describe the `next` branch as of **2026-09-21**. Known open gaps
(missing endpoints, legacy divergences under repair) are tracked in
`pitfalls.md` § "Open known gaps" rather than being papered over here; when a
gap is fixed, update that section in the same PR.
