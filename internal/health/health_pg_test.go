package health

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver

	"github.com/steemit/hivemind/internal/db"
)

// newHealthTestDB creates the dedicated hive_health_test database (the
// per-package convention; see internal/api/bridge tests).
func newHealthTestDB(t *testing.T) string {
	t.Helper()
	baseURL := os.Getenv("HIVE_TEST_DATABASE_URL")

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse db url: %v", err)
	}
	u.Path = "/postgres"
	maintDSN := u.String()

	raw, err := sql.Open("pgx", maintDSN)
	if err != nil {
		t.Fatalf("open maintenance conn: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP DATABASE IF EXISTS hive_health_test;`); err != nil {
		t.Fatalf("drop test db: %v", err)
	}
	if _, err := raw.Exec(`CREATE DATABASE hive_health_test;`); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	u.Path = "/hive_health_test"
	return u.String()
}

// TestDBHeadQuerierAgainstPG exercises the real head query behind /health
// and /head_age: a stale head must fail a reader's health check and be
// reported numerically by /head_age; a fresh head must pass.
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is unset:
//
//	HIVE_TEST_DATABASE_URL=postgresql://test:test@localhost:5433/postgres go test -race ./internal/health/...
func TestDBHeadQuerierAgainstPG(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping health PG integration test")
	}
	dbURL := newHealthTestDB(t)

	if err := db.RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	raw, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer raw.Close()

	insertHead := func(t *testing.T, num int64, age time.Duration) {
		t.Helper()
		if _, err := raw.Exec(
			`INSERT INTO hive_blocks (num, hash, created_at) VALUES ($1, $2, NOW() - $3::interval)`,
			num,
			fmt.Sprintf("hash%09d", num),
			fmt.Sprintf("%d seconds", int(age.Seconds())),
		); err != nil {
			t.Fatalf("insert head block: %v", err)
		}
	}

	querier, err := NewDBHeadQuerier(dbURL)
	if err != nil {
		t.Fatalf("NewDBHeadQuerier: %v", err)
	}
	defer querier.Close()

	// Empty index: the baseline migration seeds the 2016 genesis block, so
	// clear the table to reach the no-head branch a fresh/wiped writer DB
	// would present.
	if _, err := raw.Exec(`DELETE FROM hive_blocks;`); err != nil {
		t.Fatalf("clear hive_blocks: %v", err)
	}
	if _, err := querier.HeadState(context.Background()); err == nil {
		t.Fatal("expected ErrNoHeadBlock on empty hive_blocks")
	}

	// Stale head (2h): reader /health must 500, sync service must 200,
	// /head_age must report ~7200s. Deterministic monotonic nums: the head
	// query is ORDER BY num DESC, so the fresh head inserted later must
	// carry the larger num.
	insertHead(t, 1_000_000, 2*time.Hour)
	reader := NewChecker(querier, time.Hour, false)
	rec := httptest.NewRecorder()
	reader.HandleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("stale head: /health = %d, want 500", rec.Code)
	}

	rec = httptest.NewRecorder()
	NewChecker(querier, time.Hour, true).HandleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stale head, sync service: /health = %d, want 200", rec.Code)
	}

	rec = httptest.NewRecorder()
	reader.HandleHeadAge(rec, httptest.NewRequest(http.MethodGet, "/head_age", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stale head: /head_age = %d, want 200", rec.Code)
	}
	age, err := strconv.Atoi(rec.Body.String())
	if err != nil {
		t.Fatalf("/head_age body %q not numeric: %v", rec.Body.String(), err)
	}
	if age < 7100 || age > 7300 {
		t.Errorf("/head_age = %d, want ~7200 for a 2h-old head", age)
	}

	// Fresh head (5s): /health must pass with the head snapshot embedded.
	insertHead(t, 2_000_000, 5*time.Second)
	rec = httptest.NewRecorder()
	reader.HandleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh head: /health = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}
