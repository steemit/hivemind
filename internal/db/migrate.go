package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // pgx5 driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/steemit/hivemind/pkg/logging"
	"go.uber.org/zap"
)

//go:embed all:migrations
var migrationsFS embed.FS

// migrationsSubdir is the directory inside the embed.FS holding the .sql files.
const migrationsSubdir = "migrations"

// ErrAlreadyAtLatest is returned by RunMigrations when the database is already
// at the newest version; it is not a failure condition.
var ErrAlreadyAtLatest = migrate.ErrNoChange

// RunMigrations applies all pending schema migrations embedded in the binary.
//
// Migrations live under internal/db/migrations/*.sql and are versioned by the
// numeric prefix (0001_init_schema.up.sql, 0001_init_schema.down.sql, ...).
//
// # Bootstrapping an existing database
//
// Existing databases provisioned by the Python legacy (DB_VERSION up to 29)
// already contain the baseline schema but have NO migrate bookkeeping table.
// For those, set forceVersion to the baseline version (1) before first run:
// the migrator creates its `schema_migrations` table and stamps that version
// without executing any DDL. New/empty databases should pass forceVersion <= 0
// so the baseline migration builds the schema from scratch.
//
// forceVersion > 0  → stamp the given version (no DDL), then apply pending.
// forceVersion == 0 → normal forward migration from whatever version is set.
func RunMigrations(dbURL string, forceVersion int) error {
	d, err := iofs.New(migrationsFS, migrationsSubdir)
	if err != nil {
		return fmt.Errorf("failed to open embedded migrations: %w", err)
	}

	// golang-migrate uses the database driver name from the URL scheme or the
	// explicit source. The pgx/v5 driver registers under "pgx5".
	dbAddr, err := normalizeMigrateAddr(dbURL)
	if err != nil {
		return err
	}

	m, err := migrate.NewWithSourceInstance("iofs", d, dbAddr)
	if err != nil {
		return fmt.Errorf("failed to init migrator: %w", err)
	}
	defer m.Close()

	if forceVersion > 0 {
		// Stamp the version without running migrations (baseline an existing DB).
		if err := m.Force(forceVersion); err != nil {
			return fmt.Errorf("failed to force migration version to %d: %w", forceVersion, err)
		}
		logging.GetLogger().Info("Forced migration baseline version",
			zap.Int("version", forceVersion))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_ = ctx // migrate.Up is not context-aware; timeout is advisory for the caller.

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	version, dirty, vErr := m.Version()
	if vErr != nil {
		return fmt.Errorf("failed to read migration version: %w", vErr)
	}
	logging.GetLogger().Info("Migrations applied",
		zap.Uint("version", version),
		zap.Bool("dirty", dirty))
	return nil
}

// Version reports the current schema migration version, or 0 if migrations
// have never run (no schema_migrations table).
func Version(dbURL string) (uint, bool, error) {
	d, err := iofs.New(migrationsFS, migrationsSubdir)
	if err != nil {
		return 0, false, err
	}
	dbAddr, err := normalizeMigrateAddr(dbURL)
	if err != nil {
		return 0, false, err
	}
	m, err := migrate.NewWithSourceInstance("iofs", d, dbAddr)
	if err != nil {
		return 0, false, err
	}
	defer m.Close()
	return m.Version()
}

// MigrationsList returns the embedded migration directory entries, useful for
// diagnostics and tests.
func MigrationsList() ([]fs.DirEntry, error) {
	return fs.ReadDir(migrationsFS, migrationsSubdir)
}

// normalizeMigrateAddr converts a HIVE_DATABASE_URL into the
// "pgx5://..." form expected by the pgx/v5 database driver of golang-migrate.
// It accepts postgres://, postgresql://, and pgx5:// schemes.
func normalizeMigrateAddr(dbURL string) (string, error) {
	if dbURL == "" {
		return "", fmt.Errorf("database url is empty")
	}
	for _, scheme := range []string{"postgres://", "postgresql://", "pgx5://"} {
		if len(dbURL) >= len(scheme) && dbURL[:len(scheme)] == scheme {
			return "pgx5://" + dbURL[len(scheme):], nil
		}
	}
	// Fall back: assume the URL is already driver-appropriate.
	return dbURL, nil
}
