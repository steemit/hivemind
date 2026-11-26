package steem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/steemit/hivemind/pkg/config"
)

// ---------------------------------------------------------------------------
// Block-batch contract tests (GetBlocksRange over JSON-RPC batch;
// blocks_batch.go). The transport, the retry loop and the chunk pool are
// shared with GetContentBatch and pinned by the TestContractContentBatch*
// cases; what follows pins the get_block half: wire shape and chunking,
// per-item validation, the block-specific error verdicts, and above all
// that the batch path returns exactly what the single-block path returns.
// ---------------------------------------------------------------------------

// blockBatchResponseBody builds the JSON-RPC batch response for a request of
// blocks [from..to] the way steemd answers it: one response object per
// request, in request order, echoing the client's per-chunk 1-based ids,
// every result being the trimmed blockOneResult. results, when non-empty,
// supplies the per-item raw results instead (it must hold to-from+1 values).
func blockBatchResponseBody(from, to int64, results ...string) string {
	items := make([]string, 0, to-from+1)
	for i, num := 0, from; num <= to; i, num = i+1, num+1 {
		result := blockOneResult
		if len(results) > 0 {
			result = results[i]
		}
		items = append(items, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, i+1, result))
	}
	return "[" + strings.Join(items, ",") + "]"
}

// assertBlockBatch checks, in the test goroutine, that call is one JSON-RPC
// batch POST requesting exactly blocks [from..to] of condenser_api.get_block
// with per-chunk 1-based ids.
func assertBlockBatch(t *testing.T, call recordedRPCCall, from, to int64) {
	t.Helper()
	n := int(to - from + 1)
	if !call.IsBatch {
		t.Fatalf("request is not a JSON-RPC batch POST (method %q, params %s)", call.Method, call.Params)
	}
	if len(call.Batch) != n {
		t.Fatalf("batch has %d items, want %d (blocks %d-%d)", len(call.Batch), n, from, to)
	}
	for i, item := range call.Batch {
		if item.Method != "condenser_api.get_block" {
			t.Fatalf("batch item %d method = %q, want condenser_api.get_block", i, item.Method)
		}
		num, ok := item.ParamInt(0)
		if !ok || num != from+int64(i) {
			t.Fatalf("batch item %d requests block %d (ok=%v), want %d (params %s)", i, num, ok, from+int64(i), item.Params)
		}
		if got := string(item.ID); got != fmt.Sprint(i+1) {
			t.Fatalf("batch item %d id = %s, want %d (per-chunk 1-based ids)", i, got, i+1)
		}
	}
}

// TestBlockBatchWireShapeAndChunking pins the wire contract of the range
// fetch: one request-object array per max_batch-sized chunk, the same
// method and single positional block-number param as the single-block path,
// results reassembled in request order across chunks fetched concurrently.
func TestBlockBatchWireShapeAndChunking(t *testing.T) {
	// Chunks overlap, so the response is derived from the request itself.
	s := newScriptedSteemd(t, func(call recordedRPCCall, seq int) rpcScriptResponse {
		if !call.IsBatch || len(call.Batch) == 0 {
			return rpcScriptResponse{status: http.StatusBadRequest, body: `harness: expected a get_block batch`}
		}
		return rpcScriptResponse{body: blockBatchResponseBody(1, int64(len(call.Batch)))}
	})
	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        s.URL(),
		MaxBatch:   50,
		MaxWorkers: 4,
		MaxRetries: 1,
		RPCTimeout: 5 * time.Second,
	})

	blocks, err := c.GetBlocksRange(context.Background(), 1, 120)
	if err != nil {
		t.Fatalf("GetBlocksRange(1,120) failed: %v", err)
	}
	if len(blocks) != 120 {
		t.Fatalf("got %d blocks, want 120", len(blocks))
	}
	for i, b := range blocks {
		if got := b["block_num"]; got != float64(i+1) {
			t.Fatalf("block %d out of order: block_num = %v, want %d", i, got, i+1)
		}
	}
	// 120 blocks at max_batch 50: three POSTs of 50, 50 and 20 items. The
	// chunks are fetched concurrently, so the arrival order is not
	// deterministic: check each POST against the chunk it actually carries
	// (its first block number), then the multiset of chunk starts.
	if got := s.callCount(); got != 3 {
		t.Fatalf("got %d batch POSTs, want 3", got)
	}
	starts := make([]int64, 0, s.callCount())
	for i := 0; i < s.callCount(); i++ {
		call := s.call(i)
		if !call.IsBatch || len(call.Batch) == 0 {
			t.Fatalf("POST %d is not a non-empty get_block batch", i)
		}
		from, ok := call.Batch[0].ParamInt(0)
		if !ok {
			t.Fatalf("POST %d: first item has no block param", i)
		}
		assertBlockBatch(t, call, from, from+int64(len(call.Batch))-1)
		starts = append(starts, from)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	if !reflect.DeepEqual(starts, []int64{1, 51, 101}) {
		t.Fatalf("chunk starts = %v, want [1 51 101] (max_batch-sized chunks in range order)", starts)
	}
}

// TestBlockBatchItemValidationFailsWholeChunk pins the fail-loud per-item
// contract of a get_block batch: any malformed or failing item fails the
// WHOLE range with no results, after ONE POST (a protocol-level failure is
// deterministic and is not retried). The single-error-object case is what a
// jussi front answers when a batch exceeds its size limit (code 1600).
func TestBlockBatchItemValidationFailsWholeChunk(t *testing.T) {
	good := func(i int) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, i, blockOneResult)
	}
	cases := map[string]struct {
		body    string
		wantErr error
	}{
		"null body": {body: `null`},
		"single error object instead of an array (jussi batch-size limit, code 1600)": {
			body: `{"jsonrpc":"2.0","id":null,"error":{"code":1600,"message":"Batch size 51 exceeds limit 50"}}`,
		},
		"length mismatch (2 results for 3 blocks)": {body: "[" + good(1) + "," + good(2) + "]"},
		"id misalignment": {
			body: "[" + good(1) + `,` + strings.Replace(good(2), `"id":2`, `"id":3`, 1) + `,` + good(3) + "]",
		},
		"item JSON-RPC error": {
			body: "[" + good(1) + `,{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"boom"}},` + good(3) + "]",
		},
		"null result (block beyond head)": {
			body:    "[" + good(1) + `,{"jsonrpc":"2.0","id":2,"result":null},` + good(3) + "]",
			wantErr: ErrBlockNotFound,
		},
		"missing result": {body: "[" + good(1) + `,{"jsonrpc":"2.0","id":2},` + good(3) + "]"},
		"undecodable result (array where a block object is expected)": {
			body: "[" + good(1) + `,{"jsonrpc":"2.0","id":2,"result":[1,2,3]},` + good(3) + "]",
		},
		"trailing data after the array": {body: "[" + good(1) + "," + good(2) + "," + good(3) + "][]"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: tc.body}))
			c := newTestClient(t, s.URL(), 3)

			blocks, err := c.GetBlocksRange(context.Background(), 1, 3)
			if err == nil {
				t.Fatal("GetBlocksRange succeeded on a malformed batch response; want error")
			}
			if blocks != nil {
				t.Fatalf("GetBlocksRange returned %d blocks alongside the error; want nil", len(blocks))
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("GetBlocksRange error does not wrap the expected sentinel: %v", err)
			}
			if got := s.callCount(); got != 1 {
				t.Fatalf("a protocol-level failure must not be retried; got %d POSTs, want 1", got)
			}
		})
	}
}

// TestBlockBatchTransientFailures pins the shared transport retry on the
// block path: a 5xx chunk answer is retried with backoff and the range
// completes; a chunk that keeps answering 5xx exhausts the bounded retries
// and the whole range errors.
func TestBlockBatchTransientFailures(t *testing.T) {
	t.Run("503 then success", func(t *testing.T) {
		s := newScriptedSteemd(t, seqScript(
			rpcScriptResponse{status: http.StatusServiceUnavailable, body: `maintenance`},
			rpcScriptResponse{body: blockBatchResponseBody(1, 3)},
		))
		c := newTestClient(t, s.URL(), 3)

		blocks, err := c.GetBlocksRange(context.Background(), 1, 3)
		if err != nil {
			t.Fatalf("GetBlocksRange failed on a transient 503: %v", err)
		}
		if len(blocks) != 3 {
			t.Fatalf("got %d blocks, want 3", len(blocks))
		}
		if got := s.callCount(); got != 2 {
			t.Fatalf("got %d POSTs, want 2 (503, then success)", got)
		}
		assertBlockBatch(t, s.call(1), 1, 3)
	})

	t.Run("exhaustion", func(t *testing.T) {
		s := newScriptedSteemd(t, seqScript(
			rpcScriptResponse{status: http.StatusServiceUnavailable, body: `maintenance`},
		))
		c := newTestClient(t, s.URL(), 1) // 1 retry

		blocks, err := c.GetBlocksRange(context.Background(), 1, 3)
		if err == nil {
			t.Fatal("GetBlocksRange succeeded through persistent 503s; want error")
		}
		if blocks != nil {
			t.Fatalf("GetBlocksRange returned %d blocks alongside the error; want nil", len(blocks))
		}
		if got := s.callCount(); got != 2 {
			t.Fatalf("got %d POSTs, want 2 (initial + 1 retry)", got)
		}
	})
}

// TestBlockBatchParallelismBoundedByMaxWorkers is the block-path twin of
// TestContractContentBatchParallelismBoundedByMaxWorkers: with 3 chunks of
// 2 blocks, max_workers 2 and a per-POST delay, in-flight POSTs must
// overlap (>= 2), never exceed max_workers, and finish well under the
// serial lower bound (3 x delay).
func TestBlockBatchParallelismBoundedByMaxWorkers(t *testing.T) {
	const (
		maxBatch   = 2
		maxWorkers = 2
		delay      = 250 * time.Millisecond
	)
	body := blockBatchResponseBody(1, maxBatch)

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

	start := time.Now()
	blocks, err := c.GetBlocksRange(context.Background(), 1, 6)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetBlocksRange failed against a delayed-but-healthy server: %v", err)
	}
	if len(blocks) != 6 {
		t.Fatalf("got %d blocks, want 6", len(blocks))
	}
	maxSeen := maxInFlight.Load()
	if maxSeen > maxWorkers {
		t.Fatalf("in-flight POSTs peaked at %d, want <= max_workers (%d)", maxSeen, maxWorkers)
	}
	if maxSeen < 2 {
		t.Fatalf("in-flight POSTs peaked at %d, want >= 2: chunks were fetched serially", maxSeen)
	}
	if elapsed >= 3*delay-150*time.Millisecond {
		t.Fatalf("GetBlocksRange over 3 delayed chunks took %v; serial would be >= %v: no observable parallel speedup",
			elapsed, 3*delay)
	}
}

// TestBlockBatchCallerDeadlineNotRetried pins ctx pass-through on the block
// path: a caller deadline cuts the in-flight POST short and surfaces as a
// context error without a retry storm.
func TestBlockBatchCallerDeadlineNotRetried(t *testing.T) {
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
		delay: 800 * time.Millisecond,
		body:  blockBatchResponseBody(1, 3),
	}))
	c := newTestClient(t, s.URL(), 3)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	blocks, err := c.GetBlocksRange(ctx, 1, 3)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("GetBlocksRange succeeded despite caller deadline; want context error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetBlocksRange error = %v; want context.DeadlineExceeded in the chain", err)
	}
	if blocks != nil {
		t.Fatalf("GetBlocksRange returned %d blocks alongside the error; want nil", len(blocks))
	}
	if elapsed >= time.Second {
		t.Fatalf("cancellation did not abort the in-flight POST (%v)", elapsed)
	}
	if got := s.callCount(); got != 1 {
		t.Fatalf("a caller deadline must not be retried; got %d POSTs, want 1", got)
	}
}

// TestBlockBatchResponseBodyCapped is the block-path twin of
// TestContractContentBatchResponseBodyCapped: a 200 body larger than
// item count x per-item bound (+slack) is rejected after ONE POST and never
// buffered whole.
func TestBlockBatchResponseBodyCapped(t *testing.T) {
	huge := strings.Repeat("x", 6<<20)
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
		body: `[{"jsonrpc":"2.0","id":1,"result":{"junk":"` + huge + `"}}]`,
	}))
	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        s.URL(),
		MaxWorkers: 1,
		MaxRetries: 1,
		MaxBatch:   1, // cap = 1 x 4 MiB + 64 KiB envelope slack
		RPCTimeout: 5 * time.Second,
	})

	blocks, err := c.GetBlocksRange(context.Background(), 1, 1)
	if err == nil {
		t.Fatal("GetBlocksRange succeeded on an over-cap response body; want malformed error")
	}
	if blocks != nil {
		t.Fatalf("GetBlocksRange returned blocks alongside the error; want nil")
	}
	if got := s.callCount(); got != 1 {
		t.Fatalf("an over-cap body must not be retried; got %d POSTs, want 1", got)
	}
}

// TestBatchPOSTIsMarkedReplayable pins the Idempotency-Key header on the
// batch path, for both batch RPCs. It is what lets net/http itself replay
// a POST that failed with "server closed idle connection" (steemd closes
// idle keep-alive connections; the transport auto-retries that failure only
// for GET/HEAD or for requests carrying the key). Without it the error
// fell through to the application-level retry with its backoff on a few
// percent of chunk POSTs. The key must be non-empty and unique per POST.
func TestBatchPOSTIsMarkedReplayable(t *testing.T) {
	var mu sync.Mutex
	keys := make(map[string]bool) // key -> seen

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var items []struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(body, &items); err != nil || len(items) == 0 {
			http.Error(w, "expected a request-object array", http.StatusBadRequest)
			return
		}
		mu.Lock()
		keys[r.Header.Get("Idempotency-Key")] = true
		mu.Unlock()

		resps := make([]string, 0, len(items))
		for _, item := range items {
			result := `{"author":"a","permlink":"p"}`
			if item.Method == "condenser_api.get_block" {
				result = blockOneResult
			}
			resps = append(resps, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, item.ID, result))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, "["+strings.Join(resps, ",")+"]")
	}))
	defer srv.Close()

	// Two chunks each, so several POSTs of both kinds are observed.
	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        srv.URL,
		MaxBatch:   2,
		MaxWorkers: 2,
		MaxRetries: 1,
		RPCTimeout: 5 * time.Second,
	})
	if _, err := c.GetBlocksRange(context.Background(), 1, 4); err != nil {
		t.Fatalf("GetBlocksRange failed: %v", err)
	}
	if _, err := c.GetContentBatch(context.Background(),
		[][]string{{"a", "p"}, {"a", "p"}, {"a", "p"}, {"a", "p"}}); err != nil {
		t.Fatalf("GetContentBatch failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// 2 block chunks + 2 content chunks: four distinct keys, none empty.
	if len(keys) != 4 {
		t.Fatalf("got %d distinct Idempotency-Key values (%v), want 4 (one unique key per POST)", len(keys), keys)
	}
	if keys[""] {
		t.Fatalf("a batch POST went out without an Idempotency-Key header")
	}
}

// goldenBlocks are real mainnet blocks recorded from a frozen steemd 0.23.1
// node (testdata/block_<num>.json, one condenser_api.get_block result each).
// They are chosen for operation coverage: 4,132,598 carries the pow2 that
// created @movietrailers; 4,143,571 is the block whose post by that account
// failed the first Go sync (foreign key on the author); 4,298,248 carries a
// pow2 whose nonce (732916850) steemd serializes as a JSON number — the
// string-only model in steemutil v0.0.35 failed to decode it and the sync
// restart-looped on the first such block (4,298,001); 18,871,894 and
// 19,000,000 are dense blocks (46 custom_json ops; comment_options /
// account_update / claim_reward_balance / transfers); 4,132,599 and
// 4,132,600 make 4,132,598..600 a consecutive triple.
var goldenBlocks = []int64{4132598, 4132599, 4132600, 4143571, 4298248, 18871894, 19000000}

func loadGoldenBlock(t *testing.T, num int64) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", fmt.Sprintf("block_%d.json", num)))
	if err != nil {
		t.Fatalf("read golden block %d: %v", num, err)
	}
	return string(raw)
}

// newGoldenSteemd starts a harness that answers get_block for the golden
// blocks from the recorded results, for both wire shapes: a single request
// (what GetBlock sends through the SDK) and a request-object array (what
// GetBlocksRange sends). Any other block number gets `result: null`.
func newGoldenSteemd(t *testing.T, fixtures map[int64]string) *scriptedSteemd {
	t.Helper()
	return newScriptedSteemd(t, func(call recordedRPCCall, seq int) rpcScriptResponse {
		result := func(num int64) string {
			if fixture, ok := fixtures[num]; ok {
				return fixture
			}
			return `null`
		}
		if !call.IsBatch {
			return rpcScriptResponse{body: fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, call.ID, result(callBlockNum(call)))}
		}
		items := make([]string, 0, len(call.Batch))
		for i, item := range call.Batch {
			items = append(items, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, i+1, result(callBlockNum(item))))
		}
		return rpcScriptResponse{body: "[" + strings.Join(items, ",") + "]"}
	})
}

// callBlockNum returns the block number a recorded get_block call asked for,
// or -1 when it does not parse.
func callBlockNum(call recordedRPCCall) int64 {
	if num, ok := call.ParamInt(0); ok {
		return num
	}
	return -1
}

// TestBlockBatchMatchesSingleGetBlock pins the equivalence that makes the
// batch transport a pure transport change: for every recorded real block,
// fetching it through the batch path returns exactly the map the
// single-block path (SDK GetBlock -> blockToMap) returns — same operation
// tuples, same types, same field set. The consecutive triple is also
// fetched as one range split across chunk sizes, so multi-item id
// alignment and chunk reassembly are covered with distinct results.
func TestBlockBatchMatchesSingleGetBlock(t *testing.T) {
	fixtures := make(map[int64]string, len(goldenBlocks))
	for _, num := range goldenBlocks {
		fixtures[num] = loadGoldenBlock(t, num)
	}
	s := newGoldenSteemd(t, fixtures)
	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        s.URL(),
		MaxBatch:   2, // the triple 4,132,598..600 spans a full chunk and a 1-item tail
		MaxWorkers: 2,
		MaxRetries: 1,
		RPCTimeout: 5 * time.Second,
	})

	for _, num := range goldenBlocks {
		single, err := c.GetBlock(context.Background(), num)
		if err != nil {
			t.Fatalf("GetBlock(%d) failed against the golden fixture: %v", num, err)
		}
		batched, err := c.GetBlocksRange(context.Background(), num, num)
		if err != nil {
			t.Fatalf("GetBlocksRange(%d,%d) failed against the golden fixture: %v", num, num, err)
		}
		if len(batched) != 1 {
			t.Fatalf("GetBlocksRange(%d,%d) returned %d blocks, want 1", num, num, len(batched))
		}
		if !reflect.DeepEqual(batched[0], single) {
			t.Fatalf("block %d: batch path and single path disagree\n batch: %v\n single: %v", num, batched[0], single)
		}
		if single["block_num"] != float64(num) {
			t.Fatalf("block %d: block_num = %v", num, single["block_num"])
		}
	}

	// The consecutive triple as one range at max_batch 2: chunks [598,599]
	// and [600], reassembled in order.
	blocks, err := c.GetBlocksRange(context.Background(), 4132598, 4132600)
	if err != nil {
		t.Fatalf("GetBlocksRange(4132598,4132600) failed: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(blocks))
	}
	for i, b := range blocks {
		single, err := c.GetBlock(context.Background(), 4132598+int64(i))
		if err != nil {
			t.Fatalf("GetBlock(%d) failed: %v", 4132598+int64(i), err)
		}
		if !reflect.DeepEqual(b, single) {
			t.Fatalf("block %d in the 3-block range differs from the single path:\n batch: %v\n single: %v", 4132598+int64(i), b, single)
		}
	}

	// A gap in the golden set answers null: the whole range must fail with
	// ErrBlockNotFound rather than skip the hole.
	if _, err := c.GetBlocksRange(context.Background(), 4132600, 4132602); !errors.Is(err, ErrBlockNotFound) {
		t.Fatalf("range over a missing block: error = %v, want ErrBlockNotFound", err)
	}
}

// TestLiveBlockBatchMatchesSingleGetBlock is the live twin of
// TestBlockBatchMatchesSingleGetBlock against a real node (skipped unless
// HIVE_TEST_STEEMD_URL points at one whose chain covers the windows):
//
//	HIVE_TEST_STEEMD_URL=http://127.0.0.1:8091 go test ./internal/steem/ -run TestLiveBlockBatch -count=1 -v
//
// Windows span the sparse early chain, the pow2 era and dense 2017/2018
// blocks; every block fetched as part of a multi-chunk batch range must
// equal what the single-block path returns for it.
func TestLiveBlockBatchMatchesSingleGetBlock(t *testing.T) {
	nodeURL := os.Getenv("HIVE_TEST_STEEMD_URL")
	if nodeURL == "" {
		t.Skip("HIVE_TEST_STEEMD_URL not set; skipping live block-batch equivalence test")
	}
	c, err := New(&config.SteemConfig{URL: nodeURL, MaxBatch: 50, MaxWorkers: 4, MaxRetries: 3, RPCTimeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	ctx := context.Background()

	windows := []struct {
		name  string
		from  int64
		count int64
	}{
		{"early sparse chain", 1_000_000, 120},
		{"pow2 era", 4_132_500, 120},
		{"dense 2017-02", 18_871_800, 120},
		{"dense 2018-01", 18_980_800, 120},
	}
	compared, opTypes := 0, map[string]int{}
	for _, w := range windows {
		to := w.from + w.count - 1
		batched, err := c.GetBlocksRange(ctx, w.from, to)
		if err != nil {
			t.Fatalf("%s: GetBlocksRange(%d,%d) failed: %v", w.name, w.from, to, err)
		}
		if len(batched) != int(w.count) {
			t.Fatalf("%s: got %d blocks, want %d", w.name, len(batched), w.count)
		}
		for i, b := range batched {
			num := w.from + int64(i)
			single, err := c.GetBlock(ctx, num)
			if err != nil {
				t.Fatalf("%s: GetBlock(%d) failed: %v", w.name, num, err)
			}
			if !reflect.DeepEqual(b, single) {
				a, _ := json.Marshal(b)
				s, _ := json.Marshal(single)
				t.Fatalf("%s: block %d differs between the batch path and the single path\n batch : %s\n single: %s",
					w.name, num, a, s)
			}
			if b["block_num"] != float64(num) {
				t.Fatalf("%s: block %d out of order: block_num = %v", w.name, num, b["block_num"])
			}
			if txs, ok := b["transactions"].([]interface{}); ok {
				for _, tx := range txs {
					for _, op := range tx.(map[string]interface{})["operations"].([]interface{}) {
						opTypes[op.([]interface{})[0].(string)]++
					}
				}
			}
			compared++
		}
	}
	t.Logf("compared %d blocks across %d windows; operation types seen: %d", compared, len(windows), len(opTypes))
}
