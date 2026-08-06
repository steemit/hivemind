package db

import (
	"database/sql"
	"os"
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

	// Apply baseline from scratch.
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("RunMigrations failed on fresh DB: %v", err)
	}

	// Running again should be a no-op (ErrNoChange is swallowed internally).
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("RunMigrations (idempotent re-run) failed: %v", err)
	}

	// Version should be stamped at 1 (the baseline migration).
	v, dirty, err := Version(dbURL)
	if err != nil {
		t.Fatalf("Version() failed: %v", err)
	}
	if dirty {
		t.Errorf("schema is marked dirty after migration")
	}
	if v != 1 {
		t.Errorf("migration version = %d, want 1 (baseline)", v)
	}

	// Verify the embedded migration files exist.
	entries, err := MigrationsList()
	if err != nil {
		t.Fatalf("MigrationsList() error: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("expected at least 2 migration files (up+down), got %d", len(entries))
	}
}

// TestRunMigrations_ForceBaseline simulates pointing the migrator at an
// existing database already provisioned by the Python legacy (no
// schema_migrations table). forceVersion=1 stamps the version without
// executing DDL, after which a second run is a no-op.
func TestRunMigrations_ForceBaseline(t *testing.T) {
	dbURL := os.Getenv("HIVE_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping migration integration test")
	}

	// Simulate a legacy DB: drop bookkeeping but keep the schema tables.
	// (FreshDB test already populated them, so the schema is intact.)
	if _, err := newRawDB(dbURL, "DROP TABLE IF EXISTS schema_migrations"); err != nil {
		t.Fatalf("cleanup failed: %v", err)
	}

	// Force-stamp version 1 without running DDL.
	if err := RunMigrations(dbURL, 1); err != nil {
		t.Fatalf("RunMigrations(force=1) failed: %v", err)
	}
	v, _, err := Version(dbURL)
	if err != nil {
		t.Fatalf("Version() failed: %v", err)
	}
	if v != 1 {
		t.Fatalf("after force=1, version = %d, want 1", v)
	}

	// A subsequent normal run should be a no-op (no DDL, since at latest).
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("RunMigrations(re-run after force) failed: %v", err)
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
	if !contains(got, "statement_timeout") {
		t.Errorf("buildDSN missing statement_timeout: %q", got)
	}
	if !contains(got, "?") {
		t.Errorf("buildDSN should add query separator: %q", got)
	}

	// URL that already has a query string should append with &.
	cfg.URL = "postgres://u:p@localhost:5432/hive?sslmode=disable"
	got = buildDSN(cfg)
	if !contains(got, "&options=") {
		t.Errorf("buildDSN should append with &: %q", got)
	}

	// Zero timeout bypasses injection.
	cfg.URL = "postgres://u:p@localhost:5432/hive"
	cfg.StatementTimeout = 0
	got = buildDSN(cfg)
	if got != cfg.URL {
		t.Errorf("buildDSN with zero timeout should return URL unchanged: got %q", got)
	}
}
