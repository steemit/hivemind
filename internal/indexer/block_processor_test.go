package indexer

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx driver for raw test DB access

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/config"
)

// withMigratedDB ensures the schema is present (idempotent) and returns a
// *db.DB + repository + cleanup. Used by DB-backed indexer integration tests.
// Skips when HIVE_TEST_DATABASE_URL is unset. Resets the schema per test for
// isolation.
func withMigratedDB(t *testing.T) (*db.DB, *db.Repository, func()) {
	t.Helper()
	url := os.Getenv("HIVE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping DB-backed indexer test")
	}
	raw, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.Exec("DROP SCHEMA public CASCADE; CREATE SCHEMA public;"); err != nil {
		raw.Close()
		t.Fatalf("reset schema: %v", err)
	}
	raw.Close()

	if err := db.RunMigrations(url, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.DatabaseConfig{
		URL:              url,
		MaxOpenConns:     5,
		MaxIdleConns:     2,
		ConnMaxLifetime:  time.Hour,
		ConnMaxIdleTime:  10 * time.Minute,
		StatementTimeout: 30 * time.Second,
	}
	database, err := db.New(cfg, "ERROR")
	if err != nil {
		t.Fatalf("db.New: %v", err)
	}
	repo := db.NewRepository(database.DB)
	return database, repo, func() { database.Close() }
}

// TestExtractAccountName exercises the account-name extraction branches of
// processOperation for the pow/account_create op types. These branches are
// pure map traversal (no DB writes) and are unit-testable without a DB.
func TestExtractAccountName(t *testing.T) {
	cases := []struct {
		name    string
		opType  string
		opValue map[string]interface{}
		want    string
	}{
		{
			name:   "pow_operation extracts worker_account",
			opType: "pow_operation",
			opValue: map[string]interface{}{
				"worker_account": "alice",
			},
			want: "alice",
		},
		{
			name:   "account_create_operation extracts new_account_name",
			opType: "account_create_operation",
			opValue: map[string]interface{}{
				"new_account_name": "bob",
			},
			want: "bob",
		},
		{
			name:   "create_claimed_account_operation extracts new_account_name",
			opType: "create_claimed_account_operation",
			opValue: map[string]interface{}{
				"new_account_name": "carol",
			},
			want: "carol",
		},
		{
			name:   "pow2_operation extracts nested worker_account",
			opType: "pow2_operation",
			opValue: map[string]interface{}{
				"work": map[string]interface{}{
					"value": map[string]interface{}{
						"input": map[string]interface{}{
							"worker_account": "dave",
						},
					},
				},
			},
			want: "dave",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			accountNames := make(map[string]bool)
			extractAccountName(c.opType, c.opValue, accountNames)
			if !accountNames[c.want] {
				t.Errorf("expected account %q to be registered, got %v", c.want, accountNames)
			}
		})
	}
}

// TestExtractAccountName_NoExtraction verifies that ops with no recognizable
// account field do not pollute the accountNames set.
func TestExtractAccountName_NoExtraction(t *testing.T) {
	accountNames := make(map[string]bool)
	// An op type we do extract from, but missing the field.
	extractAccountName("account_create_operation", map[string]interface{}{}, accountNames)
	if len(accountNames) != 0 {
		t.Errorf("expected no accounts, got %v", accountNames)
	}
	// An op type we don't handle.
	extractAccountName("transfer_operation", map[string]interface{}{"from": "x"}, accountNames)
	if len(accountNames) != 0 {
		t.Errorf("transfer should not register accounts via extractAccountName, got %v", accountNames)
	}
}

// TestProcessBlock_CommentOp_Integration runs a full ProcessBlock against a
// real (migrated) Postgres: inserts a block + comment op, then verifies the
// post was written to hive_posts.
//
// Run with:
//
//	HIVE_TEST_DATABASE_URL=postgresql://test:test@localhost:5433/hive go test -run Integration ./internal/indexer/...
func TestProcessBlock_CommentOp_Integration(t *testing.T) {
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	accountRepo := db.NewAccountRepository(repo)
	ctx := context.Background()
	if existing, _ := accountRepo.GetByName(ctx, "alice"); existing == nil {
		tx := database.DB.WithContext(ctx).Begin()
		tx.Create(&models.Account{Name: "alice", CreatedAt: time.Unix(1458845200, 0).UTC(), Reputation: 25.0})
		tx.Commit()
	}

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	block := map[string]interface{}{
		"block_num":    float64(1),
		"block_id":     "0000000000000000000000000000000000000001",
		"previous":     "0000000000000000000000000000000000000000",
		"timestamp":    "2016-03-24T16:05:00",
		"transactions": []interface{}{},
	}
	block["transactions"] = []interface{}{
		map[string]interface{}{
			"transaction_id": "abc123",
			"operations": []interface{}{
				[]interface{}{
					"comment_operation",
					map[string]interface{}{
						"parent_author":   "",
						"parent_permlink": "test",
						"author":          "alice",
						"permlink":        "first-post",
						"title":           "Hello",
						"body":            "world",
						"json_metadata":   `{"tags":["test"]}`,
					},
				},
			},
		},
	}

	if err := bp.ProcessBlock(ctx, block, true); err != nil {
		t.Fatalf("ProcessBlock failed: %v", err)
	}

	postRepo := db.NewPostRepository(repo)
	post, err := postRepo.GetByAuthorPermlink(ctx, "alice", "first-post")
	if err != nil || post == nil {
		t.Fatalf("expected post alice/first-post to exist, got post=%v err=%v", post, err)
	}
	if post.Depth != 0 {
		t.Errorf("root post depth = %d, want 0", post.Depth)
	}
	if post.Author != "alice" || post.Permlink != "first-post" {
		t.Errorf("post = %+v, want author=alice permlink=first-post", post)
	}
}
