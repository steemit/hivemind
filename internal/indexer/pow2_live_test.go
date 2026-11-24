package indexer

import (
	"context"
	"os"
	"testing"

	"github.com/steemit/hivemind/internal/steem"
	"github.com/steemit/hivemind/pkg/config"
)

// Live regression for the 2026-11-23 poison-block incident, gated on BOTH
// HIVE_TEST_DATABASE_URL (the DB-backed harness; it resets the schema, so
// never point it at a sync database) and HIVE_TEST_STEEMD_URL (a node whose
// chain covers block 4.14M):
//
//	HIVE_TEST_DATABASE_URL=postgresql://test:test@localhost:5433/postgres \
//	HIVE_TEST_STEEMD_URL=http://127.0.0.1:8091 \
//	go test ./internal/indexer/ -run TestLivePow2 -count=1
//
// The first 0->20M Go sync died at block 4,143,571 with
// "hive_posts_fk1" (hive_posts.author -> hive_accounts.name): the post's
// author @movietrailers is a pow2-mined account created at block 4,132,598,
// and every pow2 account was silently missing. steemutil's POW2Operation
// model did not match condenser_api.get_block's static_variant array form
// (`"work":[0,{...}]`), so work.value.input.worker_account never survived
// SDK decoding. The 0-250k smoke run could not catch it: the pow2 era
// starts at ~4.1M.
//
// Both blocks are fetched from the real node through the real client (the
// same GetBlock -> blockToMap path GetBlocksRange uses) and pushed through
// the real block processor, so the test covers the seam the unit tests on
// either side leave open: SDK-decoded map shape <-> indexer op dispatch.
func TestLivePow2MinedAccountRegisteredBeforeFirstPost(t *testing.T) {
	nodeURL := os.Getenv("HIVE_TEST_STEEMD_URL")
	if nodeURL == "" {
		t.Skip("HIVE_TEST_STEEMD_URL not set; skipping live pow2 regression")
	}
	database, repo, cleanup := withMigratedDB(t)
	defer cleanup()

	client, err := steem.New(&config.SteemConfig{URL: nodeURL})
	if err != nil {
		t.Fatalf("steem client: %v", err)
	}
	ctx := context.Background()

	// keepOnly reduces a real block to the transactions satisfying keep,
	// preserving the transactions/transaction_ids index alignment the
	// trxid insert relies on. Unrelated transactions in the same block
	// reference accounts absent from a fresh test database and would fail
	// hive_posts_fk1 for reasons unrelated to this regression.
	keepOnly := func(block map[string]interface{}, keep func(opType string) bool) {
		txs := block["transactions"].([]interface{})
		ids, _ := block["transaction_ids"].([]interface{})
		keptTx := make([]interface{}, 0, 1)
		keptIDs := make([]interface{}, 0, 1)
		for i, tx := range txs {
			txMap := tx.(map[string]interface{})
			for _, op := range txMap["operations"].([]interface{}) {
				if keep(op.([]interface{})[0].(string)) {
					keptTx = append(keptTx, tx)
					if ids != nil && i < len(ids) {
						keptIDs = append(keptIDs, ids[i])
					}
					break
				}
			}
		}
		if len(keptTx) == 0 {
			t.Fatalf("block %v: no transaction matched the filter", block["block_num"])
		}
		block["transactions"] = keptTx
		if ids != nil {
			block["transaction_ids"] = keptIDs
		}
		// The test database only holds block 0; rebind the real `previous`.
		block["previous"] = genesisHash
	}

	regBlock, err := client.GetBlock(ctx, 4132598)
	if err != nil {
		t.Fatalf("get block 4132598: %v", err)
	}
	keepOnly(regBlock, func(opType string) bool { return opType == "pow2_operation" })

	postBlock, err := client.GetBlock(ctx, 4143571)
	if err != nil {
		t.Fatalf("get block 4143571: %v", err)
	}
	keepOnly(postBlock, func(opType string) bool { return opType == "comment_operation" })

	bp := NewBlockProcessor(database, repo, &fakeSteemProvider{})
	for i, block := range []map[string]interface{}{regBlock, postBlock} {
		// Initial-sync semantics: no cache writes, no steemd content calls.
		if err := bp.ProcessBlock(ctx, block, true); err != nil {
			if i == 1 {
				t.Fatalf("processing the first @movietrailers post failed -- this is the original incident if the error mentions hive_posts_fk1: %v", err)
			}
			t.Fatalf("processing pow2 registration block: %v", err)
		}
	}

	if n := countWhere(t, database, "hive_accounts", "name = ?", "movietrailers"); n != 1 {
		t.Errorf("hive_accounts rows for movietrailers = %d, want 1 (pow2 worker_account not registered)", n)
	}
	if n := countWhere(t, database, "hive_posts", "author = ? AND depth = 0", "movietrailers"); n != 1 {
		t.Errorf("hive_posts root rows for movietrailers = %d, want 1", n)
	}
}
