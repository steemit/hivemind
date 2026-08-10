package db

import (
	"os"
	"testing"

	"github.com/steemit/hivemind/internal/models"
)

// TestModelColumnsExistInSchema verifies that every column referenced by a
// GORM model tag actually exists in the migrated database schema. This catches
// drift between the Go model definitions and the baseline migration — the
// core acceptance criterion of KR1 ("Go models 100% compatible with Python
// schema").
//
// Run with:
//
//	HIVE_TEST_DATABASE_URL=postgresql://test:test@localhost:5433/hive go test -run TestModelColumnsExistInSchema ./internal/db/...
func TestModelColumnsExistInSchema(t *testing.T) {
	dbURL := os.Getenv("HIVE_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping model/schema alignment test")
	}
	resetSchema(t, dbURL)
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	raw, err := openRawForTest(dbURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer raw.Close()

	expected := map[string][]string{
		"hive_state":            columnNames(models.State{}),
		"hive_blocks":           columnNames(models.Block{}),
		"hive_accounts":         columnNames(models.Account{}),
		"hive_posts":            columnNames(models.Post{}),
		"hive_post_tags":        columnNames(models.PostTag{}),
		"hive_follows":          columnNames(models.Follow{}),
		"hive_reblogs":          columnNames(models.Reblog{}),
		"hive_payments":         columnNames(models.Payment{}),
		"hive_feed_cache":       columnNames(models.FeedCache{}),
		"hive_posts_cache":      columnNames(models.PostCache{}),
		"hive_posts_cache_temp": columnNames(models.PostCacheTemp{}),
		"hive_communities":      columnNames(models.Community{}),
		"hive_roles":            columnNames(models.Role{}),
		"hive_subscriptions":    columnNames(models.Subscription{}),
		"hive_notifs":           columnNames(models.Notification{}),
		"hive_posts_status":     columnNames(models.PostStatus{}),
		"hive_trxid_block_num":  columnNames(models.TransactionBlock{}),
	}

	for table, wantCols := range expected {
		rows, err := raw.Query(`SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=$1`, table)
		if err != nil {
			t.Fatalf("query columns for %s: %v", table, err)
		}
		gotSet := map[string]bool{}
		for rows.Next() {
			var c string
			rows.Scan(&c)
			gotSet[c] = true
		}
		rows.Close()

		for _, col := range wantCols {
			if !gotSet[col] {
				t.Errorf("table %s: model references column %q but it is missing from the DB schema", table, col)
			}
		}
	}
}

// TestSchemaColumnsCoveredByModels is the reverse check: every column in the
// DB schema should be represented in a model (catches missing model fields).
func TestSchemaColumnsCoveredByModels(t *testing.T) {
	dbURL := os.Getenv("HIVE_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set")
	}
	resetSchema(t, dbURL)
	if err := RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	raw, err := openRawForTest(dbURL)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer raw.Close()

	modelCols := map[string]map[string]bool{
		"hive_state":            toSet(columnNames(models.State{})),
		"hive_blocks":           toSet(columnNames(models.Block{})),
		"hive_accounts":         toSet(columnNames(models.Account{})),
		"hive_posts":            toSet(columnNames(models.Post{})),
		"hive_post_tags":        toSet(columnNames(models.PostTag{})),
		"hive_follows":          toSet(columnNames(models.Follow{})),
		"hive_reblogs":          toSet(columnNames(models.Reblog{})),
		"hive_payments":         toSet(columnNames(models.Payment{})),
		"hive_feed_cache":       toSet(columnNames(models.FeedCache{})),
		"hive_posts_cache":      toSet(columnNames(models.PostCache{})),
		"hive_posts_cache_temp": toSet(columnNames(models.PostCacheTemp{})),
		"hive_communities":      toSet(columnNames(models.Community{})),
		"hive_roles":            toSet(columnNames(models.Role{})),
		"hive_subscriptions":    toSet(columnNames(models.Subscription{})),
		"hive_notifs":           toSet(columnNames(models.Notification{})),
		"hive_posts_status":     toSet(columnNames(models.PostStatus{})),
		"hive_trxid_block_num":  toSet(columnNames(models.TransactionBlock{})),
	}

	for table, cols := range modelCols {
		rows, err := raw.Query(`SELECT column_name FROM information_schema.columns WHERE table_schema='public' AND table_name=$1`, table)
		if err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		for rows.Next() {
			var c string
			rows.Scan(&c)
			if !cols[c] {
				t.Errorf("table %s: DB column %q has no corresponding model field", table, c)
			}
		}
		rows.Close()
	}
}

func toSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}
