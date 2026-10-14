package db

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/steemit/hivemind/pkg/config"
)

// withStateRepoDB provisions a migrated database for hive_state tests and
// returns a StateRepository on a live pool. Skips without
// HIVE_TEST_DATABASE_URL (same convention as the migration tests).
func withStateRepoDB(t *testing.T) (*StateRepository, func()) {
	t.Helper()
	dbURL := os.Getenv("HIVE_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping sync-flags integration test")
	}
	resetSchema(t, dbURL)
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	database, err := New(&config.DatabaseConfig{
		URL:              dbURL,
		MaxOpenConns:     2,
		MaxIdleConns:     1,
		ConnMaxLifetime:  time.Hour,
		ConnMaxIdleTime:  10 * time.Minute,
		StatementTimeout: 30 * time.Second,
	}, "ERROR")
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	return NewStateRepository(NewRepository(database.DB)), func() { _ = database.Close() }
}

// TestSyncFlagsLegacySingleRow pins the PR#407 review fix: a legacy database
// carries exactly ONE hive_state row at its real head (block_num > 0), and
// neither the defensive seed INSERT of GetSyncFlags nor UpdateSyncFlags may
// create or target a second row. The pre-fix INSERT ... VALUES (0, ...)
// silently appended a block_num=0 row next to the legacy one, splitting
// flag reads/writes across two divergent rows.
func TestSyncFlagsLegacySingleRow(t *testing.T) {
	stateRepo, cleanup := withStateRepoDB(t)
	defer cleanup()
	ctx := context.Background()

	// Reshape the seeded row into the legacy shape: one row at head 5,
	// markers unwritten (NULL) exactly as the Python provisioner left them.
	if err := stateRepo.db.WithContext(ctx).Exec(
		"DELETE FROM hive_state").Error; err != nil {
		t.Fatalf("clear hive_state: %v", err)
	}
	if err := stateRepo.db.WithContext(ctx).Exec(`INSERT INTO hive_state
		(block_num, db_version, steem_per_mvest, usd_per_steem, sbd_per_steem, dgpo)
		VALUES (5, 29, 0, 0, 0, '')`).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	countRows := func() int64 {
		var n int64
		stateRepo.db.WithContext(ctx).
			Raw("SELECT COUNT(*) FROM hive_state").Scan(&n)
		return n
	}

	// Read: no defensive second row appears; the flags come from the legacy row.
	flags, err := stateRepo.GetSyncFlags(ctx)
	if err != nil {
		t.Fatalf("GetSyncFlags: %v", err)
	}
	if n := countRows(); n != 1 {
		t.Fatalf("hive_state rows after GetSyncFlags = %d, want 1 (defensive INSERT must not seed a second row next to the legacy one)", n)
	}
	if flags.IsInitialSync.Valid {
		t.Errorf("is_initial_sync on legacy row = %+v, want NULL (unwritten)", flags.IsInitialSync)
	}

	// Write: the flags land on the legacy row (block_num = MIN = 5), and no
	// second row appears.
	if err := stateRepo.UpdateSyncFlags(ctx,
		sql.NullBool{Bool: true, Valid: true}, sql.NullBool{}); err != nil {
		t.Fatalf("UpdateSyncFlags: %v", err)
	}
	if n := countRows(); n != 1 {
		t.Errorf("hive_state rows after UpdateSyncFlags = %d, want 1", n)
	}
	var blockNum int64
	var isInitial sql.NullBool
	row := stateRepo.db.WithContext(ctx).
		Raw("SELECT block_num, is_initial_sync FROM hive_state").Row()
	if err := row.Scan(&blockNum, &isInitial); err != nil {
		t.Fatalf("read back flags: %v", err)
	}
	if blockNum != 5 {
		t.Errorf("flagged row block_num = %d, want 5 (the legacy row)", blockNum)
	}
	if !isInitial.Valid || !isInitial.Bool {
		t.Errorf("is_initial_sync on legacy row = %+v, want true (written in place)", isInitial)
	}

	// ClearRecountDone also targets the same single row.
	if err := stateRepo.UpdateSyncFlags(ctx,
		sql.NullBool{}, sql.NullBool{Bool: true, Valid: true}); err != nil {
		t.Fatalf("UpdateSyncFlags (recount): %v", err)
	}
	if err := stateRepo.ClearRecountDone(ctx); err != nil {
		t.Fatalf("ClearRecountDone: %v", err)
	}
	var recount sql.NullBool
	row2 := stateRepo.db.WithContext(ctx).
		Raw("SELECT recount_done FROM hive_state WHERE block_num = 5").Row()
	if err := row2.Scan(&recount); err != nil {
		t.Fatalf("read back recount_done: %v", err)
	}
	if recount.Valid {
		t.Errorf("recount_done after ClearRecountDone = %+v, want NULL", recount)
	}
	if n := countRows(); n != 1 {
		t.Errorf("hive_state rows after ClearRecountDone = %d, want 1", n)
	}
}

// TestSyncFlagsFreshDBSeedsOnce covers the empty-database side of the same
// fix: the defensive INSERT still seeds exactly one row when hive_state is
// empty, and repeated reads stay single-row.
func TestSyncFlagsFreshDBSeedsOnce(t *testing.T) {
	stateRepo, cleanup := withStateRepoDB(t)
	defer cleanup()
	ctx := context.Background()

	// The migration already seeded one row; empty it to prove the INSERT.
	if err := stateRepo.db.WithContext(ctx).
		Exec("DELETE FROM hive_state").Error; err != nil {
		t.Fatalf("clear hive_state: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := stateRepo.GetSyncFlags(ctx); err != nil {
			t.Fatalf("GetSyncFlags (call %d): %v", i+1, err)
		}
	}
	var n int64
	stateRepo.db.WithContext(ctx).
		Raw("SELECT COUNT(*) FROM hive_state").Scan(&n)
	if n != 1 {
		t.Errorf("hive_state rows after 3 reads on empty table = %d, want 1 (seeded once)", n)
	}
}
