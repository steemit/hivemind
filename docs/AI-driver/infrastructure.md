# Infrastructure (config / logging / telemetry / steem client / lifecycle)

## Configuration — `pkg/config`

Viper with:

- env prefix `HIVE_`, keys snake_case; conversion in `toEnvKey()`
  (`http_read_timeout` → `HIVE_HTTP_READ_TIMEOUT`),
- lookup order: viper (optional config file `./config.yaml`,
  `$HOME/.hivemind`, `/etc/hivemind` — **file overrides env** via viper
  precedence) → direct `os.Getenv` → literal default passed to the getter.

### The four-place registration rule

A new knob is registered in **four** places, or viper lookups and struct
reads silently diverge:

1. field on the appropriate `Config` sub-struct (`Config.Steem`, …);
2. `Load()` — `getXxx("key", default)`;
3. `setDefaults()` — `viper.SetDefault("key", default)` (same default!);
4. `Validate()` — range check (ports, non-negative ints, enumerated
   strings).

Then update the tables in `AGENTS.md` and `README.md`. Known drift: several
keys historically missed `setDefaults()`; keep the set in sync when touching
config.

`Config` groups: `Database` (URL, migrate flags, pool 25/10/1h/10m,
statement timeout 30 s), `Steem` (`steemd_url`, max_batch 50, max_workers 4,
muted-accounts URL), `Redis` (URL; empty = disabled), `Server` (8080,
timeouts 30/30/120 s), `Indexer` (trail_blocks 2, sync_interval, jobs
interval), `Logging` (INFO/json/scalyr), `Telemetry` (enabled,
traces_endpoint `localhost:4318`, prometheus flags).

**Env-name discipline**: the canonical names are exactly `HIVE_` + snake key
(`HIVE_STEEMD_URL`, `HIVE_MAX_BATCH`, `HIVE_PROMETHEUS_PORT`). Deployment
files have historically introduced look-alikes (`HIVE_STEEM_URL`,
`HIVE_TELEMETRY_PROMETHEUS_PORT`) that silently no-op — when editing
compose/README, derive names from `toEnvKey`, not memory.

## Logging — `pkg/logging`

- `logging.InitLogger(cfg)` installs the global zap logger
  (`logging.GetLogger()`; safe fallback if uninitialized).
- Two encoders: JSON (with optional Scalyr-compat shape:
  timestamp/level/message/logger/file/line + flattened fields) and console
  text for development.
- Convention: obtain per-component loggers with
  `logging.GetLogger().With(zap.String("component", "block-processor"))`.
  Component strings in use: `api-router`, `jsonrpc`, `indexer`,
  `block-processor`, `steem-client`, `cache`, etc.

## Telemetry — `pkg/telemetry`

- `telemetry.Init(cfg) (shutdown func(), err)`: OTLP/HTTP trace exporter +
  BatchSpanProcessor + resource (service name/version) + W3C
  TraceContext/Baggage propagators. Shutdown flushes with a per-exporter 3 s
  / total 5 s budget. Disabled telemetry yields no-ops.
- Span helpers: `StartSpanWithName(ctx, name)`, `RecordSpanError(span, err)`,
  `SetSpanSuccess(span)`, `RecordSpanParams(span, params)` (params as span
  events — keep large arrays out of hot paths).
- Metrics: `metrics.go` declares promauto families (requests, db, cache,
  posts, follows, communities, notifications) recorded via `telemetry.Record*`
  helpers. **The exporter endpoint (`/metrics` HTTP listener) is a known open
  gap** — recording works, exposure does not (see pitfalls).
- Two tracer entry points coexist (`telemetry.StartSpanWithName` with an
  internal tracer, and `otel.Tracer("hivemind")` in middleware/steem) — both
  end up at the same provider; prefer `StartSpanWithName` in new code.

## steemd client — `internal/steem`

Three layers:

1. `provider.go` — the `Provider` interface (8 methods) everything depends
   on; `*Client` satisfies it; `internal/indexer/fake_provider_test.go`
   provides the test double. Compile-time assertion pins the interface.
2. `client.go` — thin wrapper adding spans and translating SDK types into
   `map[string]interface{}` block shapes (`operations: [[type, value], …]`,
   op types suffixed `_operation`) so downstream code matches legacy JSON
   shapes.
3. `steemgosdk`/`steemutil` (upstream SDK) — JSON-RPC over HTTP, 30 s
   hard-coded timeout, per-request goroutines for `GetBlocks`.

Contract facts an agent must know:

- `GetBlocksRange(ctx, from, to)` fetches blocks `[from, to]` inclusive via
  the SDK; the SDK fires one HTTP request per block (not a JSON-RPC batch),
  retries indefinitely with **no backoff and no context cancellation** on
  transport failure (upstream limitation — see pitfalls before relying on
  retry behavior; upgrade path: newer SDK with context support).
- Only `condenser_api.*` calls are used against steemd.
- `Mutes` (mutes.go): process-wide singleton (`SetSharedMutes` /
  `SharedMutes`, never nil) wrapping the "irredeemables" remote list;
  hourly refresh, **fail-open** on fetch errors, per-account tag caching.
  Designed to be consulted by API object builders for `stats.hide` /
  blacklists and vote filtering.

## Binary lifecycle

Common order: `config.Load()` → `logging.InitLogger()` → `telemetry.Init()`
→ (`db.New` + optional migrations) → run.

- **Server** (`cmd/server/main.go`): installs shared mutes, optional steemd
  client (lazy; server starts even if steemd is down), optional Redis, Gin
  with `Recovery → Tracing → Metrics` middleware, `POST /` + health routes;
  SIGINT/SIGTERM → `srv.Shutdown(10 s)`.
- **Indexer** (`cmd/indexer/main.go`): db + migrations → mutes → steemd
  client → `indexer.NewSync` → `Run(ctx)` in a goroutine; signal cancels the
  context. `Run` = initial-sync detection → fast sync → background CacheSync
  / jobs goroutines → irreversible polling loop.

`logger.Fatal` skips deferred cleanups (zap calls `os.Exit`); anything that
must flush (telemetry, DB close) belongs on the normal return path.

## Deployment (docker-compose)

Services: `postgres:15` (pg_isready healthcheck), `redis:7` (optional but
currently a hard dependency of the server service definition), `server`
(port 8080; wget `/health` healthcheck), `indexer`, `jaeger` all-in-one
(UI 16686; OTLP enabled). Named volumes persist postgres/redis. When editing
compose, env names must match `toEnvKey` output (see config section).

## Make targets

`make build / build-server / build-indexer / run-server / run-indexer /
test / fmt / lint / check (fmt+lint+test) / clean` → `bin/hivemind-{server,
indexer}`. Lint requires golangci-lint installed. CI historically only
builds Docker images on master — `make check` is the local gate.
