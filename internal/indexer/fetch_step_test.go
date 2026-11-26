package indexer

// Fetch-step tests for Phase 1 of syncTxBatch: how many blocks one
// GetBlocksRange call fetches, and why (see fetchStepSize).
//
// GetBlocksRange sends max_batch-sized JSON-RPC batch POSTs on a
// max_workers pool. Stepping the fetch loop by max_batch alone leaves that
// pool with a single chunk per call -- one serial POST -- which measured as
// a regression on dense ranges against the single-block fan-out it replaced
// (113 against 143 blocks/s, fetch only, 4 workers). The step must span one
// chunk per worker for the pool to engage.

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/steemit/hivemind/pkg/config"
)

// TestFetchStepSize pins the step arithmetic: one chunk per worker by
// default, one plain chunk when the worker count is unset, and always
// bounded by the tx batch size.
func TestFetchStepSize(t *testing.T) {
	cases := map[string]struct {
		maxBatch, maxWorkers, txBatch int
		want                          int64
	}{
		"defaults: one chunk per worker":             {50, 4, 1000, 200},
		"worker count unset: one chunk per call":     {50, 0, 1000, 50},
		"more workers than the batch has chunks":     {50, 64, 1000, 1000},
		"capped by a smaller tx batch":               {50, 4, 100, 100},
		"degenerate 1-block tx batch":                {50, 4, 1, 1},
		"max_batch unset falls back to the tx batch": {0, 4, 1000, 1000},
	}
	for name, tc := range cases {
		s := &Sync{config: &config.Config{
			Steem:   config.SteemConfig{MaxWorkers: tc.maxWorkers},
			Indexer: config.IndexerConfig{MaxBatch: tc.maxBatch, TxBatchBlocks: tc.txBatch},
		}}
		if got := s.fetchStepSize(); got != tc.want {
			t.Errorf("%s: fetchStepSize() = %d, want %d", name, got, tc.want)
		}
	}
}

// TestFetchCallsSpanOneChunkPerWorker pins the fetch loop itself against a
// recording provider: with max_batch 2 and 4 workers, syncing 20 blocks
// issues 8-block calls (four chunks each, so four POSTs in flight); with
// the worker count unset it steps by the plain 2-block chunk.
func TestFetchCallsSpanOneChunkPerWorker(t *testing.T) {
	plainChunks := make([][2]int64, 0, 10)
	for from := int64(1); from <= 20; from += 2 {
		plainChunks = append(plainChunks, [2]int64{from, from + 1})
	}
	cases := map[string]struct {
		maxWorkers int
		want       [][2]int64
	}{
		"4 workers: 8-block calls":     {4, [][2]int64{{1, 8}, {9, 16}, {17, 20}}},
		"workers unset: 2-block calls": {0, plainChunks},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			database, _, cleanup := withMigratedDB(t)
			defer cleanup()

			blocks := make(map[int64]map[string]interface{}, 20)
			for n := int64(1); n <= 20; n++ {
				blocks[n] = syncBlockFixture(n, fmt.Sprintf("2026-01-01T00:%02d:%02d", n*3/60, n*3%60))
			}
			provider := txBatchTestProvider(blocks, 20)
			serve := provider.getBlocksRange
			var calls [][2]int64
			provider.getBlocksRange = func(ctx context.Context, from, to int64) ([]map[string]interface{}, error) {
				calls = append(calls, [2]int64{from, to})
				return serve(ctx, from, to)
			}

			cfg := &config.Config{
				Steem:   config.SteemConfig{MaxWorkers: tc.maxWorkers},
				Indexer: config.IndexerConfig{MaxBatch: 2},
			}
			s, err := NewSync(cfg, database, provider)
			if err != nil {
				t.Fatalf("NewSync: %v", err)
			}
			if err := s.syncTxBatch(context.Background(), 1, 20, true); err != nil {
				t.Fatalf("syncTxBatch(1,20): %v", err)
			}

			if !reflect.DeepEqual(calls, tc.want) {
				t.Fatalf("GetBlocksRange calls = %v, want %v", calls, tc.want)
			}
			// Whatever the step, the whole range lands contiguously.
			if head := s.headBlockNum(context.Background()); head != 20 {
				t.Fatalf("head after the batch = %d, want 20", head)
			}
		})
	}
}
