package steem

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/steemit/hivemind/pkg/config"
)

// Live pow2 regression test against a real steemd node (env-gated so CI,
// which has no node, skips it):
//
// HIVE_TEST_STEEMD_URL=http://127.0.0.1:8091 go test ./internal/steem/ -run TestLivePOW2 -count=1
//
// Block 4,132,598 (mainnet 2016-08-16) carries the pow2_operation that
// created @movietrailers. steemgosdk/steemutil's flat POW2Operation model
// (pre-v0.0.35 local fix) silently dropped work.value.input.worker_account,
// so the indexer missed every pow2-mined account and died at block 4,143,571
// on hive_posts_fk1 (author -> hive_accounts.name). This pins the SDK fix at
// the exact block that surfaced it, end-to-end through GetBlock (the same
// call the indexer's GetBlocksRange batches use).
func TestLivePOW2WorkerAccountSurvives(t *testing.T) {
	url := os.Getenv("HIVE_TEST_STEEMD_URL")
	if url == "" {
		t.Skip("HIVE_TEST_STEEMD_URL not set; skipping live pow2 test")
	}

	cfg := &config.SteemConfig{URL: url}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	block, err := client.GetBlock(ctx, 4132598)
	if err != nil {
		t.Fatalf("get block: %v", err)
	}

	found := false
	raw, _ := json.Marshal(block)
	var parsed struct {
		Transactions []struct {
			Operations []json.RawMessage `json:"operations"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("re-parse block: %v", err)
	}
	for _, tx := range parsed.Transactions {
		for _, op := range tx.Operations {
			var pair []json.RawMessage
			if err := json.Unmarshal(op, &pair); err != nil || len(pair) != 2 {
				continue
			}
			var opType string
			json.Unmarshal(pair[0], &opType)
			if opType != "pow2_operation" && opType != "pow2" {
				continue
			}
			var data map[string]any
			if err := json.Unmarshal(pair[1], &data); err != nil {
				t.Fatalf("pow2 op data: %v", err)
			}
			work, _ := data["work"].(map[string]any)
			value, _ := work["value"].(map[string]any)
			input, _ := value["input"].(map[string]any)
			worker, _ := input["worker_account"].(string)
			t.Logf("pow2 op: worker_account=%q", worker)
			if worker != "movietrailers" {
				t.Errorf("worker_account = %q, want movietrailers (SDK dropped the nested pow2 work payload)", worker)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no pow2 operation found in block 4132598")
	}
}
