package db

import (
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for ad-hoc test connections
	"github.com/steemit/hivemind/pkg/config"
)

// newRawDB opens a one-off *sql.DB against dbURL and runs q. Used by the
// migration tests for setup/teardown that bypasses the migrator itself.
func newRawDB(dbURL, q string) (sql.Result, error) {
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	return db.Exec(q)
}

// resetSchema drops and recreates the public schema, giving each integration
// test a clean slate regardless of execution order.
func resetSchema(t *testing.T, dbURL string) {
	t.Helper()
	if _, err := newRawDB(dbURL, "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
}

// latestMigrationVersion computes the expected version from the embedded
// migration file count (each migration is an up+down pair, so version = pairs).
func latestMigrationVersion(t *testing.T) uint {
	t.Helper()
	entries, err := MigrationsList()
	if err != nil {
		t.Fatalf("MigrationsList: %v", err)
	}
	v := uint(len(entries) / 2)
	if v == 0 {
		v = 1
	}
	return v
}

// TestRunMigrations_FreshDB applies the baseline migration to an empty
// database and verifies the resulting schema. It requires a live Postgres
// instance pointed at by HIVE_TEST_DATABASE_URL and is skipped otherwise.
//
// Run with:
//
//	HIVE_TEST_DATABASE_URL=postgresql://test:test@localhost:5433/hive go test -run TestRunMigrations_FreshDB ./internal/db/...
func TestRunMigrations_FreshDB(t *testing.T) {
	dbURL := os.Getenv("HIVE_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping migration integration test")
	}
	resetSchema(t, dbURL)

	// Apply baseline from scratch.
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("RunMigrations failed on fresh DB: %v", err)
	}

	// Running again should be a no-op (ErrNoChange is swallowed internally).
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("RunMigrations (idempotent re-run) failed: %v", err)
	}

	v, dirty, err := Version(dbURL)
	if err != nil {
		t.Fatalf("Version() failed: %v", err)
	}
	if dirty {
		t.Errorf("schema is marked dirty after migration")
	}
	if want := latestMigrationVersion(t); v != want {
		t.Errorf("migration version = %d, want %d", v, want)
	}
}

// TestRunMigrations_ForceBaseline simulates pointing the migrator at an
// existing database already provisioned by the Python legacy (schema tables
// present but no schema_migrations bookkeeping). forceVersion=1 stamps the
// baseline without re-running DDL, then pending migrations apply.
//
// This test is self-contained: it builds the schema itself first, so it does
// NOT depend on TestRunMigrations_FreshDB running before it.
//
// The key invariant: the baseline's CREATE TABLE / seed INSERTs must NOT
// execute against a DB that already has them (they would error on duplicate
// tables / primary keys).
func TestRunMigrations_ForceBaseline(t *testing.T) {
	dbURL := os.Getenv("HIVE_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping migration integration test")
	}
	resetSchema(t, dbURL)

	// Self-fixture: build the schema normally first (simulates a fully-migrated
	// legacy DB), then drop bookkeeping to mimic "migrator has never seen it".
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("fixture: RunMigrations failed: %v", err)
	}
	if _, err := newRawDB(dbURL, "DROP TABLE schema_migrations"); err != nil {
		t.Fatalf("drop bookkeeping: %v", err)
	}

	// Snapshot seed account count before forcing.
	raw, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	var before int
	if err := raw.QueryRow("SELECT count(*) FROM hive_accounts").Scan(&before); err != nil {
		raw.Close()
		t.Fatalf("count before: %v", err)
	}
	raw.Close()

	// Force-stamp version 1 (baseline), then apply pending.
	if err := RunMigrations(dbURL, 1); err != nil {
		t.Fatalf("RunMigrations(force=1) failed: %v", err)
	}

	// Seed rows unchanged: forcing skipped the baseline DDL/seed.
	raw, err = sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer raw.Close()
	var after int
	if err := raw.QueryRow("SELECT count(*) FROM hive_accounts").Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Errorf("seed account count changed after force: before=%d after=%d (baseline DDL should have been skipped)", before, after)
	}

	// Version should now be at latest.
	v, _, err := Version(dbURL)
	if err != nil {
		t.Fatalf("Version() failed: %v", err)
	}
	if want := latestMigrationVersion(t); v != want {
		t.Errorf("after force, version = %d, want %d", v, want)
	}

	// A subsequent normal run should be a no-op.
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("RunMigrations(re-run after force) failed: %v", err)
	}
}

// TestVersion_NeverMigrated verifies Version() returns (0, false, nil) when
// the schema_migrations table does not exist, rather than an error.
func TestVersion_NeverMigrated(t *testing.T) {
	dbURL := os.Getenv("HIVE_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set")
	}
	resetSchema(t, dbURL)
	// No migration run — schema_migrations absent.
	v, dirty, err := Version(dbURL)
	if err != nil {
		t.Fatalf("Version() on unmigrated DB should not error: %v", err)
	}
	if v != 0 || dirty {
		t.Errorf("Version() on unmigrated DB = (%d, %v), want (0, false)", v, dirty)
	}
}

// TestNormalizeMigrateAddr checks URL scheme handling for the pgx5 driver.
func TestNormalizeMigrateAddr(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"postgres://u:p@h:5432/db", "pgx5://u:p@h:5432/db", false},
		{"postgresql://u:p@h:5432/db", "pgx5://u:p@h:5432/db", false},
		{"pgx5://u:p@h:5432/db", "pgx5://u:p@h:5432/db", false},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := normalizeMigrateAddr(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeMigrateAddr(%q): expected error for empty URL", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeMigrateAddr(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeMigrateAddr(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBuildDSN verifies statement_timeout injection into the connection URL.
func TestBuildDSN(t *testing.T) {
	// With statement timeout: DSN gains an options param.
	cfg := &config.DatabaseConfig{
		URL:              "postgres://u:p@localhost:5432/hive",
		StatementTimeout: 30 * time.Second,
	}
	got := buildDSN(cfg)
	if !strings.Contains(got, "statement_timeout") {
		t.Errorf("buildDSN missing statement_timeout: %q", got)
	}
	if !strings.Contains(got, "?") {
		t.Errorf("buildDSN should add query separator: %q", got)
	}

	// URL that already has a query string should append with &.
	cfg.URL = "postgres://u:p@localhost:5432/hive?sslmode=disable"
	got = buildDSN(cfg)
	if !strings.Contains(got, "&options=") {
		t.Errorf("buildDSN should append with &: %q", got)
	}

	// Zero timeout bypasses injection (timeout disabled).
	cfg.URL = "postgres://u:p@localhost:5432/hive"
	cfg.StatementTimeout = 0
	got = buildDSN(cfg)
	if got != cfg.URL {
		t.Errorf("buildDSN with zero timeout should return URL unchanged: got %q", got)
	}
}
