// Package health implements the real /health and /head_age endpoints
// (planned PR#430, 05-deploy.md section 1.2).
//
// Semantics follow the Python legacy (hive/server/serve.py health and
// head_age):
//
//   - /health answers 500 when the database is not reachable, and -- for
//     reader processes (isSyncService=false) -- when the indexed head block
//     is older than maxHeadAge (legacy max_head_age, 1h: "while 1 hr is a
//     bit stale, such a condition is a symptom of a writer issue, *not* a
//     reader node issue"). Sync-service processes (the indexer) are exempt
//     from the head-lag criterion, mirroring the legacy is_syncer branch:
//     a writer lagging behind during a multi-hour catch-up is working as
//     intended and must not get its instance recycled by the health check.
//   - /head_age answers the db_head_age value in seconds as plain text so
//     external monitors can sample sync lag at any granularity. Unlike the
//     legacy endpoint (a syncer-only probe that answers 500 when age > 15s)
//     it answers 200 whenever the database is reachable: the KR5
//     observation plan scrapes the VALUE continuously, including -- most
//     importantly -- while the node is behind. Only database failure is a
//     500.
package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"go.uber.org/zap"

	"github.com/steemit/hivemind/pkg/logging"
)

// headQueryTimeout bounds every health probe's database work. Legacy
// used a 2s pool-acquire timeout on the isolated health engine so probes
// stay responsive; here the bound covers the whole query.
const headQueryTimeout = 3 * time.Second

// HeadState is the db head snapshot (legacy db_head_state).
type HeadState struct {
	DbHeadBlock int64
	DbHeadTime  time.Time
	// DbHeadAge is seconds since the head block's created_at.
	DbHeadAge int64
}

// ErrNoHeadBlock reports an empty hive_blocks table (fresh database, the
// indexer has not written anything yet).
var ErrNoHeadBlock = errors.New("no head block: hive_blocks is empty")

// HeadQuerier reads the head state. The interface keeps the handlers
// testable without a database.
type HeadQuerier interface {
	HeadState(ctx context.Context) (*HeadState, error)
}

// Checker evaluates and serves process health.
type Checker struct {
	querier       HeadQuerier
	maxHeadAge    time.Duration
	isSyncService bool
}

// NewChecker builds a Checker. maxHeadAge <= 0 disables the head-lag
// criterion (catch-up deployments); isSyncService marks writer processes
// (the indexer), which legacy exempts from the lag check.
func NewChecker(querier HeadQuerier, maxHeadAge time.Duration, isSyncService bool) *Checker {
	return &Checker{
		querier:       querier,
		maxHeadAge:    maxHeadAge,
		isSyncService: isSyncService,
	}
}

// healthResponse is the JSON body of /health, shaped after the legacy
// serve.py response (state, result, status, sync_service, ...).
type healthResponse struct {
	State        *headStateJSON `json:"state"`
	Result       string         `json:"result"`
	Status       string         `json:"status"` // "OK" | "WARN"
	SyncService  bool           `json:"sync_service"`
	SourceCommit string         `json:"source_commit"`
	SchemaHash   string         `json:"schema_hash"`
	DockerTag    string         `json:"docker_tag"`
	Timestamp    string         `json:"timestamp"`
}

// headStateJSON mirrors the legacy db_head_state dict keys.
type headStateJSON struct {
	DbHeadBlock int64  `json:"db_head_block"`
	DbHeadTime  string `json:"db_head_time"`
	DbHeadAge   int64  `json:"db_head_age"`
}

// HandleHealth implements GET /health (and /.well-known/healthcheck.json).
func (c *Checker) HandleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), headQueryTimeout)
	defer cancel()

	logger := logging.GetLogger().With(zap.String("component", "health"))

	state, err := c.querier.HeadState(ctx)

	resp := healthResponse{
		SyncService:  c.isSyncService,
		SourceCommit: os.Getenv("SOURCE_COMMIT"),
		SchemaHash:   os.Getenv("SCHEMA_HASH"),
		DockerTag:    os.Getenv("DOCKER_TAG"),
		Timestamp:    time.Now().UTC().Format("2006-01-02T15:04:05.000"),
	}

	status := http.StatusOK
	switch {
	case err != nil && errors.Is(err, ErrNoHeadBlock):
		// Empty head: the database answers but nothing is indexed. A reader
		// serving an empty index is not healthy; a fresh writer is simply
		// not done yet, but reporting 500 here would keep EB recycling the
		// indexer during initial sync -- so only readers fail.
		if !c.isSyncService {
			status = http.StatusInternalServerError
			resp.Result = "no head block (database not indexed)"
		} else {
			resp.Result = "no head block yet (initial sync in progress)"
		}
		resp.Status = healthWord(status)
		logger.Warn("health check: empty head block", zap.Bool("sync_service", c.isSyncService))
	case err != nil:
		// Legacy: 'db not available'.
		status = http.StatusInternalServerError
		resp.Result = "db not available"
		resp.Status = "WARN"
		logger.Warn("health check: database unavailable", zap.Error(err))
	case !c.isSyncService && c.maxHeadAge > 0 && time.Duration(state.DbHeadAge)*time.Second > c.maxHeadAge:
		// Legacy: 'head block age (%s) > max (%s); head block num: %s'.
		status = http.StatusInternalServerError
		resp.Result = fmt.Sprintf("head block age (%d) > max (%d); head block num: %d",
			state.DbHeadAge, int64(c.maxHeadAge.Seconds()), state.DbHeadBlock)
		resp.Status = "WARN"
		logger.Warn("health check: head block lag", zap.Int64("age", state.DbHeadAge),
			zap.Duration("max", c.maxHeadAge), zap.Int64("head_block", state.DbHeadBlock))
	default:
		resp.Result = fmt.Sprintf("head block age is %d, head block num is %d",
			state.DbHeadAge, state.DbHeadBlock)
		resp.Status = "OK"
	}

	if state != nil {
		resp.State = &headStateJSON{
			DbHeadBlock: state.DbHeadBlock,
			DbHeadTime:  state.DbHeadTime.UTC().Format("2006-01-02 15:04:05"),
			DbHeadAge:   state.DbHeadAge,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		logger.Error("health check: failed to encode response", zap.Error(err))
	}
}

func healthWord(status int) string {
	if status == http.StatusOK {
		return "OK"
	}
	return "WARN"
}

// HandleHeadAge implements GET /head_age: the db head age in seconds as
// plain text (200 while the database answers; 500 only on database
// failure or an empty index -- see the package comment for the deviation
// from the legacy 15s threshold).
func (c *Checker) HandleHeadAge(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), headQueryTimeout)
	defer cancel()

	logger := logging.GetLogger().With(zap.String("component", "health"))

	state, err := c.querier.HeadState(ctx)
	if err != nil {
		status := http.StatusInternalServerError
		result := "db not available"
		if errors.Is(err, ErrNoHeadBlock) {
			result = "no head block (database not indexed)"
		}
		logger.Warn("head_age: head state unavailable", zap.Error(err))
		http.Error(w, result, status)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(strconv.FormatInt(state.DbHeadAge, 10)))
}

// DBHeadQuerier reads the head block from PostgreSQL through a dedicated
// single-connection pool, isolated from the application pool. This is the
// legacy health engine pattern (hive/server/db.py: a maxsize=1 engine so a
// saturated main pool cannot starve /health and get the instance killed by
// the ELB). Connections are established lazily on first query.
type DBHeadQuerier struct {
	db *sql.DB
}

// NewDBHeadQuerier opens the isolated health pool (1 connection) against
// dbURL. No connection attempt is made until the first query; the caller
// should still verify reachability via an initial HeadState call if a
// fail-fast startup check is wanted.
func NewDBHeadQuerier(dbURL string) (*DBHeadQuerier, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open health pool: %w", err)
	}
	// The legacy isolated engine: maxsize=1. The pool bound doubles as the
	// serialization point for concurrent /health and /head_age probes.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return &DBHeadQuerier{db: db}, nil
}

// Close closes the isolated health pool.
func (q *DBHeadQuerier) Close() error {
	return q.db.Close()
}

// HeadState runs the legacy health query:
// SELECT num, created_at FROM hive_blocks ORDER BY num DESC LIMIT 1.
func (q *DBHeadQuerier) HeadState(ctx context.Context) (*HeadState, error) {
	var (
		num       int64
		createdAt time.Time
	)
	err := q.db.QueryRowContext(ctx,
		`SELECT num, created_at FROM hive_blocks ORDER BY num DESC LIMIT 1`,
	).Scan(&num, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoHeadBlock
	}
	if err != nil {
		return nil, err
	}
	return &HeadState{
		DbHeadBlock: num,
		DbHeadTime:  createdAt,
		DbHeadAge:   int64(time.Since(createdAt).Seconds()),
	}, nil
}
