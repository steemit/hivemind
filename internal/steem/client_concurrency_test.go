package steem

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steemit/hivemind/pkg/config"
)

// Concurrency-bound contract tests (planned PR#440, KR6):
//
//   - the #401-registered MaxWorkers parallelism test: GetContentBatch's
//     chunk pool must fetch chunks concurrently (in-flight workers overlap)
//     while never exceeding max_workers (delay + timing assertions);
//   - the 06-quality section 4 gap for GetBlocksRange: a TRANSIENT per-block
//     failure inside a range (429 then success) is retried per block by the
//     SDK transport with backoff and the whole range still completes — the
//     existing matrix pinned single-block retry and permanent range failure,
//     but not the transient-inside-range recovery.

// TestContractContentBatchParallelismBoundedByMaxWorkers pins the chunk-pool
// concurrency model (PR#401 registration for #440): requests are split into
// max_batch-sized chunks and the chunks are fetched on a max_workers-bounded
// pool. With 3 chunks of 2 posts, max_workers 2 and a per-request delay:
//
//   - overlap: the max observed in-flight requests must be >= 2 (a serial
//     fetcher never overlaps) — and == max_workers for full utilization;
//   - bound: the max observed in-flight requests must never exceed
//     max_workers (the pool ceiling holds);
//   - timing: the wall clock must land well under the serial lower bound
//     (3 x delay), corroborating the overlap with a delay-based assertion.
//
// The in-flight ceiling is measured server-side with an atomic counter around
// the handler body (the scriptedSteemd harness serializes only its respond
// callback, not the response delays, but cannot report in-flight counts).
func TestContractContentBatchParallelismBoundedByMaxWorkers(t *testing.T) {
	const (
		maxBatch   = 2
		maxWorkers = 2
		chunks     = 3
		delay      = 250 * time.Millisecond
	)
	// Every chunk POST carries exactly maxBatch items with 1-based ids, so
	// one fixed well-formed batch response body serves all of them; the
	// per-item author only needs to exist (validation), not match the
	// requested tuple.
	body := `[{"jsonrpc":"2.0","id":1,"result":{"author":"a1","permlink":"p"}},` +
		`{"jsonrpc":"2.0","id":2,"result":{"author":"a2","permlink":"p"}}]`

	var inFlight, maxInFlight atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			seen := maxInFlight.Load()
			if cur <= seen || maxInFlight.CompareAndSwap(seen, cur) {
				break
			}
		}
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        srv.URL,
		MaxBatch:   maxBatch,
		MaxWorkers: maxWorkers,
		MaxRetries: 1,
		RPCTimeout: 5 * time.Second,
	})

	posts := make([][]string, 0, maxBatch*chunks)
	for i := 0; i < maxBatch*chunks; i++ {
		posts = append(posts, []string{fmt.Sprintf("auth%02d", i), "p"})
	}

	start := time.Now()
	got, err := c.GetContentBatch(context.Background(), posts)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetContentBatch failed against a delayed-but-healthy server: %v", err)
	}
	if len(got) != maxBatch*chunks {
		t.Fatalf("got %d results, want %d", len(got), maxBatch*chunks)
	}

	maxSeen := maxInFlight.Load()
	if maxSeen > maxWorkers {
		t.Fatalf("in-flight requests peaked at %d, want <= max_workers (%d): the worker-pool ceiling is violated", maxSeen, maxWorkers)
	}
	if maxSeen < 2 {
		t.Fatalf("in-flight requests peaked at %d, want >= 2: chunks were fetched serially (no overlap => MaxWorkers not wired to fetch concurrency)", maxSeen)
	}
	// Timing corroboration (delay-based assertion per the registration): the
	// serial lower bound is 3 x 250ms = 750ms; the 2-worker schedule completes
	// in ~2 x 250ms. 600ms sits between the two with margin on both sides.
	if elapsed >= 600*time.Millisecond {
		t.Fatalf("GetContentBatch over 3 delayed chunks took %v; want < 600ms (serial would be >= 750ms): no observable parallel speedup", elapsed)
	}
	t.Logf("3 chunks x %v delay, max_workers=%d: elapsed=%v, peak in-flight=%d",
		delay, maxWorkers, elapsed, maxSeen)
}

// TestContractGetBlocksRangeTransientRetryRecovers pins the recovery twin of
// TestContractRangePartialFailureNeverSkipsBlocks: when ONE block of the range
// answers a transient 429 and succeeds on its retry, the SDK's per-block
// transport retry rides it out inside the range fetch — the call returns ALL
// blocks in order after exactly one extra request and a 429 backoff, instead
// of failing the whole all-or-nothing range.
func TestContractGetBlocksRangeTransientRetryRecovers(t *testing.T) {
	// Attempts are keyed per block (range fetches run concurrently, so the
	// arrival order is not deterministic; respond runs under the harness
	// record lock, so the map needs no extra locking).
	attempts := map[int64]int{}
	s := newScriptedSteemd(t, func(call recordedRPCCall, seq int) rpcScriptResponse {
		if call.Method != "condenser_api.get_block" {
			return rpcScriptResponse{status: http.StatusInternalServerError, body: "harness: unexpected method " + call.Method}
		}
		num, ok := call.ParamInt(0)
		if !ok {
			return rpcScriptResponse{status: http.StatusInternalServerError, body: "harness: missing block param"}
		}
		attempts[num]++
		if num == 2 && attempts[num] == 1 {
			return rpcScriptResponse{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`}
		}
		return rpcScriptResponse{body: rpcOK(blockOneResult)}
	})
	c := newTestClient(t, s.URL(), 3)

	start := time.Now()
	blocks, err := c.GetBlocksRange(context.Background(), 1, 3)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetBlocksRange failed on a transient per-block 429: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3 (transient failure must not shrink the range)", len(blocks))
	}
	for i, b := range blocks {
		if got := b["block_num"]; got != float64(i+1) {
			t.Fatalf("block %d out of order: block_num = %v, want %d", i, got, i+1)
		}
	}
	// Blocks 1 and 3 once; block 2 twice (429 + success).
	if got := s.callCount(); got != 4 {
		t.Fatalf("got %d requests, want 4 (blocks 1,3 once; block 2 retried once)", got)
	}
	// The 429 backoff (api/v2 doubles for rate limiting: 100ms base doubled
	// = 200ms) must have elapsed before the retry; a lower bound below that
	// stays robust to timer granularity.
	if elapsed < 150*time.Millisecond {
		t.Fatalf("retry came back too fast (%v); 429 backoff not applied inside the range fetch", elapsed)
	}

	// After-the-fact param verification (test goroutine).
	nums := make([]int, 0, s.callCount())
	for i := 0; i < s.callCount(); i++ {
		num, ok := s.call(i).ParamInt(0)
		if !ok {
			t.Fatalf("request %d has no parsable block param", i)
		}
		nums = append(nums, int(num))
	}
	sort.Ints(nums)
	if fmt.Sprint(nums) != "[1 2 2 3]" {
		t.Fatalf("requested block numbers = %v, want [1 2 2 3]", nums)
	}
}
