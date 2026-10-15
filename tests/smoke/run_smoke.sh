#!/usr/bin/env bash
# End-to-end smoke test for the hivemind API server image (planned PR#432,
# 06-quality.md section 3.4). Scripts the Q3 verification report section 4
# flow: start the container image against a fresh PostgreSQL, seed the
# deterministic fixture (seed.sql: accounts, three-level discussion tree,
# cache rows with a votes CSV, fresh head block), then curl-assert:
#
#   1. GET /health                       -> 200, status "OK"
#   2. condenser_api.get_content         -> author + 2 active_votes
#   3. condenser_api.get_discussions_by_trending -> >=2 posts, alice first
#   4. no.such.method                    -> JSON-RPC -32601 (error path)
#
# Requirements: docker; jq; curl; psql is run through a throwaway
# postgres:15-alpine container (no local client needed).
#
# Usage:
#   tests/smoke/run_smoke.sh                       # builds the image first
#   IMAGE=hivemind-server:smoke tests/smoke/run_smoke.sh
#
# Environment:
#   IMAGE          server image to test (default: hivemind-server:smoke;
#                  built from Dockerfile.server when not present)
#   SMOKE_DB_URL   target database DSN. The database is DROPPED and
#                  RECREATED on every run - never point it at anything you
#                  care about. Default: postgresql://test:test@localhost:5432/hivemind_smoke
#   SMOKE_ADMIN_URL DSN of the maintenance database used to drop/create the
#                  target (default: same host, db "postgres")
#   HTTP_PORT      host port the container publishes 8080 on (default 8080)
#   PSQL_IMAGE     image used for the psql client (default postgres:15-alpine)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(dirname "$(dirname "$SCRIPT_DIR")")"

IMAGE="${IMAGE:-hivemind-server:smoke}"
SMOKE_DB_URL="${SMOKE_DB_URL:-postgresql://test:test@localhost:5432/hivemind_smoke}"
SMOKE_ADMIN_URL="${SMOKE_ADMIN_URL:-$(dirname "$SMOKE_DB_URL")/postgres}"
HTTP_PORT="${HTTP_PORT:-8080}"
PSQL_IMAGE="${PSQL_IMAGE:-postgres:15-alpine}"
CONTAINER=hivemind-smoke-server

log() { echo "[smoke] $*"; }
fail() { echo "[smoke] FAIL: $*" >&2; exit 1; }

command -v docker >/dev/null 2>&1 || fail "docker is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v curl >/dev/null 2>&1 || fail "curl is required"

# psql runs via a throwaway container on the host network, so it reaches
# any PostgreSQL the host can reach (compose :5432, shared test PG :5433,
# CI service container :5432).
psql_exec() {
    docker run --rm --network host "$PSQL_IMAGE" psql "$1" -v ON_ERROR_STOP=1 "${@:2}"
}

cleanup() {
    log "stopping container $CONTAINER"
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ---------------------------------------------------------------------
# 1. Image
# ---------------------------------------------------------------------
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
    log "image $IMAGE not found; building from Dockerfile.server"
    docker build -t "$IMAGE" -f "$REPO_ROOT/Dockerfile.server" "$REPO_ROOT"
fi

# ---------------------------------------------------------------------
# 2. Fresh database (drop + create), migrations land in step 3
# ---------------------------------------------------------------------
db_name="$(basename "$SMOKE_DB_URL")"; db_name="${db_name%%\?*}"
[ -n "$db_name" ] || fail "could not derive database name from SMOKE_DB_URL"
log "recreating database $db_name"
psql_exec "$SMOKE_ADMIN_URL" -c "DROP DATABASE IF EXISTS $db_name;" >/dev/null
psql_exec "$SMOKE_ADMIN_URL" -c "CREATE DATABASE $db_name;" >/dev/null

# ---------------------------------------------------------------------
# 3. Start the server container (applies migrations on boot).
#
# Host networking on purpose: the DSN addresses the database the way the
# HOST sees it (localhost:5432 CI service container / localhost:5433 shared
# dev PG); with bridge networking "localhost" inside the container would
# not reach it. The server therefore listens directly on $HTTP_PORT
# (HIVE_HTTP_SERVER_PORT) instead of a published port.
# ---------------------------------------------------------------------
log "starting $IMAGE (port $HTTP_PORT)"
docker run -d --name "$CONTAINER" \
    --network host \
    -e "HIVE_DATABASE_URL=$SMOKE_DB_URL" \
    -e HIVE_TELEMETRY_ENABLED=false \
    -e HIVE_PROMETHEUS_ENABLED=false \
    -e HIVE_HTTP_SERVER_HOST=0.0.0.0 \
    -e "HIVE_HTTP_SERVER_PORT=$HTTP_PORT" \
    "$IMAGE" >/dev/null

log "waiting for migrations (hive_accounts table)"
ok=""
for i in $(seq 1 60); do
    if psql_exec "$SMOKE_DB_URL" -Atc "SELECT 1 FROM hive_accounts LIMIT 1;" >/dev/null 2>&1; then
        ok=1; break
    fi
    if ! docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null | grep -q true; then
        docker logs "$CONTAINER" || true
        fail "container exited before migrations completed"
    fi
    sleep 2
done
[ -n "$ok" ] || { docker logs "$CONTAINER" || true; fail "migrations did not complete in time"; }
log "migrations applied"

# ---------------------------------------------------------------------
# 4. Seed + wait for healthy (the head-lag criterion passes on the fresh
#    head block seeded below; before it the genesis block keeps /health
#    at 500 by design)
# ---------------------------------------------------------------------
log "applying seed.sql"
docker run --rm --network host -v "$SCRIPT_DIR:/seed:ro" "$PSQL_IMAGE" \
    psql "$SMOKE_DB_URL" -v ON_ERROR_STOP=1 -f /seed/seed.sql >/dev/null

log "waiting for /health 200"
ok=""
for i in $(seq 1 30); do
    code="$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:$HTTP_PORT/health" || true)"
    [ "$code" = "200" ] && { ok=1; break; }
    sleep 2
done
[ -n "$ok" ] || { docker logs "$CONTAINER" || true; fail "/health did not return 200"; }

rpc() {
    curl -s -X POST "http://localhost:$HTTP_PORT/" \
        -H 'Content-Type: application/json' -d "$1"
}

# ---------------------------------------------------------------------
# 5. Assertions
# ---------------------------------------------------------------------
log "assert 1/4: /health body"
body="$(curl -s "http://localhost:$HTTP_PORT/health")"
echo "$body" | jq -e '.status == "OK" and .state.db_head_block == 50000000' >/dev/null \
    || fail "health body unexpected: $body"
log "  OK: status OK, head block 50000000"

log "assert 2/4: condenser_api.get_content"
body="$(rpc '{"jsonrpc":"2.0","id":1,"method":"condenser_api.get_content","params":["alice","root-post"]}')"
echo "$body" | jq -e '.result.author == "alice" and .result.permlink == "root-post" and (.result.active_votes | length) == 2' >/dev/null \
    || fail "get_content unexpected: $body"
log "  OK: author/permlink + 2 active_votes"

log "assert 3/4: condenser_api.get_discussions_by_trending"
body="$(rpc '{"jsonrpc":"2.0","id":1,"method":"condenser_api.get_discussions_by_trending","params":[{"limit":10}]}')"
echo "$body" | jq -e '(.result | length) >= 2 and .result[0].author == "alice" and .result[0].permlink == "root-post"' >/dev/null \
    || fail "get_discussions_by_trending unexpected: $body"
log "  OK: >=2 posts, alice/root-post ranked first"

log "assert 4/4: no.such.method error path"
body="$(rpc '{"jsonrpc":"2.0","id":1,"method":"no.such.method","params":[]}')"
echo "$body" | jq -e '.error.code == -32601' >/dev/null \
    || fail "unknown method unexpected: $body"
log "  OK: -32601 method not found"

log "PASS: all smoke assertions green"
