package steem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/steemit/hivemind/pkg/config"
)

// ---------------------------------------------------------------------------
// Scriptable steemd harness (fake↔real contract-test skeleton, 06-quality §3.2)
//
// This harness impersonates a steemd JSON-RPC endpoint behind an
// httptest.Server so the REAL *Client can be tested against scripted
// responses: bad JSON, HTTP 429/5xx, null results, delays. The behaviors
// pinned here are the contract that test doubles (FakeProvider in this
// package and fakeSteemProvider in internal/indexer) must mirror.
//
// Extension points for follow-up PRs:
//   - PR#401: get_content quirks (single request rejecting a params array of
//     pairs, batch response id/length alignment) — script responses per
//     method/params via a custom respond closure and assert on recorded calls.
//   - Any future case: add a TestContract* function; the harness needs no
//     changes for sequence-, params-, or delay-driven scenarios.
// ---------------------------------------------------------------------------

// rpcScriptResponse is one scripted HTTP response.
type rpcScriptResponse struct {
	status int           // HTTP status code; 0 means 200
	body   string        // raw response body, written verbatim
	delay  time.Duration // optional sleep before responding (in-flight tests)
}

// recordedRPCCall captures one JSON-RPC request received by the harness.
type recordedRPCCall struct {
	Method string
	Params json.RawMessage
}

// ParamInt returns the i-th positional param coerced to int64, and whether
// the param exists and is a number. It deliberately takes no *testing.T:
// it is also called from respond closures running on the HTTP handler
// goroutine, where Fatalf/FailNow would terminate the wrong goroutine
// (runtime.Goexit, leaving the test hanging). Handlers should answer 500
// when !ok; parameter assertions belong to the test goroutine after the
// call, via the recorded requests.
func (c recordedRPCCall) ParamInt(i int) (int64, bool) {
	var params []interface{}
	if err := json.Unmarshal(c.Params, &params); err != nil {
		return 0, false
	}
	if i >= len(params) {
		return 0, false
	}
	num, ok := params[i].(float64)
	if !ok {
		return 0, false
	}
	return int64(num), true
}

// scriptedSteemd is an httptest server impersonating a steemd JSON-RPC
// endpoint. Every request is recorded and answered from the script function.
type scriptedSteemd struct {
	t       *testing.T
	server  *httptest.Server
	respond func(call recordedRPCCall, seq int) rpcScriptResponse

	mu    sync.Mutex
	calls []recordedRPCCall
}

// newScriptedSteemd starts the fake steemd; the server is closed on test
// cleanup. respond is consulted (under the record lock, so it must not call
// back into the harness) once per request with the zero-based arrival
// sequence number.
func newScriptedSteemd(t *testing.T, respond func(call recordedRPCCall, seq int) rpcScriptResponse) *scriptedSteemd {
	t.Helper()
	s := &scriptedSteemd{t: t, respond: respond}
	s.server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.server.Close)
	return s
}

func (s *scriptedSteemd) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "harness: failed to read request body", http.StatusBadRequest)
		return
	}
	var req struct {
		JsonRpc string          `json:"jsonrpc"`
		ID      int             `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		s.t.Logf("harness: unparsable request body: %v", err)
	}

	resp := s.record(recordedRPCCall{Method: req.Method, Params: req.Params})

	if resp.delay > 0 {
		time.Sleep(resp.delay)
	}
	status := resp.status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprint(w, resp.body)
}

// record appends the call and consults the script under the record lock.
// The unlock is deferred (not a plain call after respond): a runtime.Goexit
// inside respond — e.g. a testing helper calling FailNow on the handler
// goroutine — would otherwise skip the unlock and deadlock every later
// request. Deferring inside record (rather than handle) also keeps the lock
// from being held while the response is delayed/written, so concurrent
// range fetches still overlap in the harness.
func (s *scriptedSteemd) record(call recordedRPCCall) rpcScriptResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := len(s.calls)
	s.calls = append(s.calls, call)
	return s.respond(call, seq)
}

// URL returns the harness endpoint.
func (s *scriptedSteemd) URL() string { return s.server.URL }

// callCount returns the number of requests received so far.
func (s *scriptedSteemd) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

// call returns the i-th recorded request.
func (s *scriptedSteemd) call(i int) recordedRPCCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i < 0 || i >= len(s.calls) {
		s.t.Fatalf("harness: no call %d (total %d)", i, len(s.calls))
	}
	return s.calls[i]
}

// seqScript builds the common "replay responses in order, repeat the last
// one once exhausted" script. Suitable for deterministic single-call tests;
// concurrent range fetches should script per-request params instead.
func seqScript(responses ...rpcScriptResponse) func(call recordedRPCCall, seq int) rpcScriptResponse {
	return func(call recordedRPCCall, seq int) rpcScriptResponse {
		if len(responses) == 0 {
			return rpcScriptResponse{body: "{}"}
		}
		if seq >= len(responses) {
			return responses[len(responses)-1]
		}
		return responses[seq]
	}
}

// rpcOK wraps a raw result value into a JSON-RPC success body.
func rpcOK(result string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"result":%s}`, result)
}

// badJSONBody simulates a broken gateway: valid JSON prefix, truncated
// payload. The transport-level json.Decode must fail on it.
const badJSONBody = `{"jsonrpc":"2.0","id":1,"result":{"block_id":"00000001`

// blockOneResult is a trimmed but realistic condenser_api.get_block result
// (genesis-ish block 1 with a single vote operation).
const blockOneResult = `{
  "previous": "0000000000000000000000000000000000000000",
  "timestamp": "2016-03-24T16:05:00",
  "witness": "initminer",
  "transaction_merkle_root": "0000000000000000000000000000000000000000",
  "extensions": [],
  "witness_signature": "1f5dbd0adcf1a54d6c5f3a1a3d7c1e6b9b4d3a2c1b0a9988776655443322110ff0",
  "transactions": [
    {
      "ref_block_num": 0,
      "ref_block_prefix": 0,
      "expiration": "2016-03-24T16:05:30",
      "operations": [
        ["vote", {"voter": "alice", "author": "bob", "permlink": "first-post", "weight": 10000}]
      ],
      "extensions": [],
      "signatures": []
    }
  ],
  "block_id": "0000000100d5b6b65a4c1e8ee0d7b0eb1c01bc2c",
  "signing_key": "STM5ABCDEF0123456789",
  "transaction_ids": ["f68142ba1a0d6924ba0b32be1bdafe0a29d78c85"]
}`

// newTestClient builds a real Client against the harness URL with explicit
// retry/timeout knobs so tests stay fast and deterministic.
//
// Gotcha: maxRetries <= 0 does NOT disable or pin retries — the SDK's
// WithMaxRetry falls back to its default (3) for n <= 0. Always pass an
// explicit positive value (1 keeps exhaustion tests fast) unless the SDK
// default is deliberately what is under test.
func newTestClient(t *testing.T, url string, maxRetries int) *Client {
	t.Helper()
	cfg := &config.SteemConfig{
		URL:        url,
		MaxWorkers: 4,
		MaxRetries: maxRetries,
		RPCTimeout: 5 * time.Second,
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	return client
}

// ---------------------------------------------------------------------------
// Contract cases
// ---------------------------------------------------------------------------

// TestContractBadJSONIsErrorNotEmptyBlock pins the PR#400 acceptance case:
// a malformed (truncated) JSON response must surface as an error — for the
// single-block fetch, the range fetch, AND the head/irreversible probes that
// drive sync bounds — never as an empty result the indexer could mistake for
// a real (empty) block and advance head on.
func TestContractBadJSONIsErrorNotEmptyBlock(t *testing.T) {
	t.Run("get_block", func(t *testing.T) {
		s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: badJSONBody}))
		c := newTestClient(t, s.URL(), 3)

		block, err := c.GetBlock(context.Background(), 1)
		if err == nil {
			t.Fatal("GetBlock succeeded on a bad JSON response; want error")
		}
		if block != nil {
			t.Fatalf("GetBlock returned a block map alongside the error; want nil, got %v", block)
		}
		if s.callCount() != 1 {
			t.Fatalf("bad JSON must not be retried (non-transient); got %d requests, want 1", s.callCount())
		}
	})

	t.Run("get_blocks_range", func(t *testing.T) {
		s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: badJSONBody}))
		c := newTestClient(t, s.URL(), 3)

		blocks, err := c.GetBlocksRange(context.Background(), 1, 3)
		if err == nil {
			t.Fatal("GetBlocksRange succeeded on a bad JSON response; want error")
		}
		if blocks != nil {
			t.Fatalf("GetBlocksRange returned blocks alongside the error; want nil, got %d blocks", len(blocks))
		}
	})

	t.Run("dynamic_global_properties", func(t *testing.T) {
		s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: badJSONBody}))
		c := newTestClient(t, s.URL(), 3)

		if _, err := c.GetDynamicGlobalProperties(context.Background()); err == nil {
			t.Fatal("GetDynamicGlobalProperties succeeded on a bad JSON response; want error")
		}
		// The sync loop derives its fetch window from LastIrreversible; on a
		// bad response it must error (and retry the round), not see 0.
		if _, err := c.LastIrreversible(context.Background()); err == nil {
			t.Fatal("LastIrreversible succeeded on a bad JSON response; want error")
		}
		if _, err := c.HeadBlock(context.Background()); err == nil {
			t.Fatal("HeadBlock succeeded on a bad JSON response; want error")
		}
	})
}

// TestContractNullBlockIsErrBlockNotFound pins the api/v2 semantics upgrade:
// steemd answering `result: null` for get_block (block beyond head) is an
// ErrBlockNotFound error, never a zero-value/empty block.
func TestContractNullBlockIsErrBlockNotFound(t *testing.T) {
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: rpcOK(`null`)}))
	c := newTestClient(t, s.URL(), 3)

	block, err := c.GetBlock(context.Background(), 1_000_000_000)
	if err == nil {
		t.Fatal("GetBlock succeeded on a null result; want ErrBlockNotFound")
	}
	if !errors.Is(err, ErrBlockNotFound) {
		t.Fatalf("GetBlock error does not wrap ErrBlockNotFound: %v", err)
	}
	if block != nil {
		t.Fatalf("GetBlock returned a block map alongside the error; want nil, got %v", block)
	}
}

// TestContractRateLimitedBacksOffAndRetries pins the PR#400 acceptance case:
// an HTTP 429 response is retried after a backoff (doubled for rate limiting
// in steemgosdk api/v2) and succeeds once the limit clears.
func TestContractRateLimitedBacksOffAndRetries(t *testing.T) {
	s := newScriptedSteemd(t, seqScript(
		rpcScriptResponse{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`},
		rpcScriptResponse{body: rpcOK(blockOneResult)},
	))
	c := newTestClient(t, s.URL(), 3)

	start := time.Now()
	block, err := c.GetBlock(context.Background(), 1)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetBlock failed on transient 429: %v", err)
	}

	if s.callCount() != 2 {
		t.Fatalf("got %d requests, want exactly 2 (one 429, one success)", s.callCount())
	}
	if got := s.call(0).Method; got != "condenser_api.get_block" {
		t.Fatalf("first request method = %q, want condenser_api.get_block", got)
	}
	// Parameter assertions run here in the test goroutine (after the fact),
	// never inside the HTTP handler goroutine.
	if got, ok := s.call(0).ParamInt(0); !ok || got != 1 {
		t.Fatalf("first request block param = %d (ok=%v), want 1", got, ok)
	}
	// api/v2 doubles the backoff for 429: 100ms base << 0, doubled = 200ms.
	// Assert a lower bound below that to stay robust to timer granularity.
	if elapsed < 150*time.Millisecond {
		t.Fatalf("retry came back too fast (%v); exponential backoff for 429 not applied", elapsed)
	}

	// The retried response must be fully converted end-to-end.
	if got := block["block_num"]; got != float64(1) {
		t.Fatalf("block_num = %v, want 1", got)
	}
	if got := block["timestamp"]; got != "2016-03-24T16:05:00" {
		t.Fatalf("timestamp = %v, want 2016-03-24T16:05:00", got)
	}
	txs, ok := block["transactions"].([]interface{})
	if !ok || len(txs) != 1 {
		t.Fatalf("transactions = %#v, want one converted transaction", block["transactions"])
	}
	txMap, _ := txs[0].(map[string]interface{})
	ops, ok := txMap["operations"].([]interface{})
	if !ok || len(ops) != 1 {
		t.Fatalf("operations = %#v, want one converted operation", txMap["operations"])
	}
	opTuple, ok := ops[0].([]interface{})
	if !ok || len(opTuple) != 2 {
		t.Fatalf("operation = %#v, want a [type, data] tuple", ops[0])
	}
	if opTuple[0] != "vote_operation" {
		t.Fatalf("operation type = %v, want vote_operation", opTuple[0])
	}
}

// TestContractRangePartialFailureNeverSkipsBlocks pins the all-or-nothing
// semantics of GetBlocksRange: when one block in the range keeps failing
// after bounded retries, the whole call errors — the caller must never
// receive a partial/empty success it could advance past.
func TestContractRangePartialFailureNeverSkipsBlocks(t *testing.T) {
	// Script by request params (range fetches run concurrently): block 2
	// answers 500 on every attempt, blocks 1 and 3 answer real blocks. The
	// respond closure must not fail the test itself (it runs on a handler
	// goroutine); on an unusable param it answers 500 so the request can
	// never masquerade as a good block. Params are asserted afterwards,
	// below.
	s := newScriptedSteemd(t, func(call recordedRPCCall, seq int) rpcScriptResponse {
		if num, ok := call.ParamInt(0); ok && call.Method == "condenser_api.get_block" && num == 2 {
			return rpcScriptResponse{status: http.StatusInternalServerError, body: `boom`}
		}
		if call.Method == "condenser_api.get_block" {
			if _, ok := call.ParamInt(0); !ok {
				return rpcScriptResponse{status: http.StatusInternalServerError, body: `harness: missing/unparsable block param`}
			}
		}
		return rpcScriptResponse{body: rpcOK(blockOneResult)}
	})
	c := newTestClient(t, s.URL(), 1) // 1 retry: block 2 fails after 2 attempts

	blocks, err := c.GetBlocksRange(context.Background(), 1, 3)
	if err == nil {
		t.Fatal("GetBlocksRange succeeded with a persistently failing block; want error")
	}
	if blocks != nil {
		t.Fatalf("GetBlocksRange returned %d blocks alongside the error; want nil (never partial)", len(blocks))
	}
	// 3 distinct blocks; block 2 attempted twice (initial + 1 retry).
	if got := s.callCount(); got != 4 {
		t.Fatalf("got %d requests, want 4 (blocks 1,3 once; block 2 twice)", got)
	}
	// After-the-fact param verification (test goroutine): the four requests
	// must be exactly blocks 1, 2 (twice), 3 of condenser_api.get_block.
	nums := make([]int, 0, s.callCount())
	for i := 0; i < s.callCount(); i++ {
		call := s.call(i)
		if call.Method != "condenser_api.get_block" {
			t.Fatalf("request %d method = %q, want condenser_api.get_block", i, call.Method)
		}
		num, ok := call.ParamInt(0)
		if !ok {
			t.Fatalf("request %d has no parsable block param (params: %s)", i, call.Params)
		}
		nums = append(nums, int(num))
	}
	sort.Ints(nums)
	if !reflect.DeepEqual(nums, []int{1, 2, 2, 3}) {
		t.Fatalf("requested block numbers = %v, want [1 2 2 3]", nums)
	}
}

// TestContractContextCancelAbortsInFlight pins ctx pass-through: a caller
// deadline must cut an in-flight RPC short (no outer retry storm) and
// surface as a context error.
func TestContractContextCancelAbortsInFlight(t *testing.T) {
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
		delay: 800 * time.Millisecond,
		body:  rpcOK(blockOneResult),
	}))
	c := newTestClient(t, s.URL(), 3)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.GetBlock(ctx, 1)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("GetBlock succeeded despite caller deadline; want context error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetBlock error = %v; want context.DeadlineExceeded in the chain", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("cancellation did not abort the in-flight request (%v); ctx is not passed through", elapsed)
	}
}

// TestContractGetContentBatchShapeRecordsCurrentBehavior documents (does not
// endorse) the pre-PR#401 wire shape: GetContentBatch still sends ONE
// condenser_api.get_content request whose params are an array of
// [author, permlink] pairs. Real steemd rejects that shape; real batch
// semantics plus the quirk contract cases land in PR#401, which extends
// this harness.
func TestContractGetContentBatchShapeRecordsCurrentBehavior(t *testing.T) {
	// One result per requested post: since the PR#400-review length guard,
	// a short/empty response is an error, so the shape-recording script must
	// answer with a length-matching array.
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
		body: rpcOK(`[{"author":"alice"},{"author":"bob"}]`),
	}))
	// Explicit 1, not 0: WithMaxRetry(0) would silently fall back to the
	// SDK default (3) — see newTestClient.
	c := newTestClient(t, s.URL(), 1)

	posts, err := c.GetContentBatch(context.Background(), [][]string{
		{"alice", "p1"}, {"bob", "p2"},
	})
	if err != nil {
		t.Fatalf("GetContentBatch failed against permissive harness: %v", err)
	}
	if len(posts) != 2 {
		t.Fatalf("got %d posts, want 2", len(posts))
	}
	if got := s.callCount(); got != 1 {
		t.Fatalf("got %d requests, want 1 (single non-batch request until PR#401)", got)
	}
	if got := s.call(0).Method; got != "condenser_api.get_content" {
		t.Fatalf("method = %q, want condenser_api.get_content", got)
	}
}

// TestContractNullAndEmptyDGPAreErrors pins the PR#400-review case: the SDK
// decodes `result:null` and `result:{}` for get_dynamic_global_properties
// into a zero-value struct with a NIL error (json.Unmarshal of "null" is a
// no-op). The wrapper must reject such responses — a zero-value DGP would
// make HeadBlock()/LastIrreversible() report 0 with err==nil, and the
// initial-sync check (sync.go) would conclude the chain is empty and skip
// syncing. Mirrors legacy client.py's `assert 'time' in ret`.
func TestContractNullAndEmptyDGPAreErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result string
	}{
		{name: "null_result", result: `null`},
		{name: "empty_object_result", result: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: rpcOK(tc.result)}))
			c := newTestClient(t, s.URL(), 1)

			props, err := c.GetDynamicGlobalProperties(context.Background())
			if err == nil {
				t.Fatal("GetDynamicGlobalProperties succeeded on a null/empty result; want malformed-response error")
			}
			if props != nil {
				t.Fatalf("GetDynamicGlobalProperties returned properties alongside the error; want nil, got %v", props)
			}
			// The sync loop drives its bounds through these two helpers;
			// both must error on the same zero-value hazard instead of
			// reporting 0.
			if _, err := c.HeadBlock(context.Background()); err == nil {
				t.Fatal("HeadBlock succeeded on a null/empty DGP result; want error")
			}
			if _, err := c.LastIrreversible(context.Background()); err == nil {
				t.Fatal("LastIrreversible succeeded on a null/empty DGP result; want error")
			}
			// A 200 with a well-formed body is not retried: exactly one
			// request per helper call must have hit the harness.
			if got := s.callCount(); got != 3 {
				t.Fatalf("got %d requests, want 3 (one per DGP-derived helper, no retries)", got)
			}
		})
	}
}

// TestContractRateLimitExhaustionIsError pins the failure twin of the
// recovery case (TestContractRateLimitedBacksOffAndRetries): when the node
// keeps answering 429 until the bounded transport retries are exhausted,
// the wrapper must surface an error — never a zero-value/empty block the
// indexer could mistake for chain state — after exactly maxRetries+1
// attempts.
func TestContractRateLimitExhaustionIsError(t *testing.T) {
	s := newScriptedSteemd(t, seqScript(
		rpcScriptResponse{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`},
	))
	c := newTestClient(t, s.URL(), 1) // initial attempt + 1 retry

	block, err := c.GetBlock(context.Background(), 1)
	if err == nil {
		t.Fatal("GetBlock succeeded after retries were exhausted; want error")
	}
	if block != nil {
		t.Fatalf("GetBlock returned a block map alongside the error; want nil, got %v", block)
	}
	if got := s.callCount(); got != 2 {
		t.Fatalf("got %d requests, want 2 (initial attempt + 1 retry)", got)
	}
}

// TestContractContentBatchLengthMismatchIsError pins the PR#400-review
// length guard: callers index the batch response by request position
// (cached_post.go updateBatchTuples, jobs.go audit_cache_undelete do
// posts[i]), so a response whose length differs from the number of
// requested posts must be an explicit error — never a short/long slice
// that would panic with index-out-of-range or silently shift entries.
func TestContractContentBatchLengthMismatchIsError(t *testing.T) {
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
		body: rpcOK(`[{"author":"alice"}]`), // 1 result for 2 requested posts
	}))
	c := newTestClient(t, s.URL(), 1)

	posts, err := c.GetContentBatch(context.Background(), [][]string{
		{"alice", "p1"}, {"bob", "p2"},
	})
	if err == nil {
		t.Fatal("GetContentBatch succeeded on a length-mismatched response; want error")
	}
	if posts != nil {
		t.Fatalf("GetContentBatch returned results alongside the error; want nil, got %d", len(posts))
	}

	t.Run("malformed_tuple_rejected_before_request", func(t *testing.T) {
		s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: rpcOK(`[]`)}))
		c := newTestClient(t, s.URL(), 1)

		if _, err := c.GetContentBatch(context.Background(), [][]string{{"authorless"}}); err == nil {
			t.Fatal("GetContentBatch accepted a malformed [author] tuple; want input validation error")
		}
		if got := s.callCount(); got != 0 {
			t.Fatalf("malformed tuples must fail before any request is sent; got %d requests", got)
		}
	})
}

// TestContractAccountsLengthMismatchIsError mirrors legacy client.py's
// `assert len(requested) == len(response)`: steemd answers get_accounts
// with exactly one entry per requested name, in order; anything else is a
// malformed response and must not become a short/shifted success.
func TestContractAccountsLengthMismatchIsError(t *testing.T) {
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
		body: rpcOK(`[{"name":"alice"}]`), // 1 result for 2 requested names
	}))
	c := newTestClient(t, s.URL(), 1)

	accts, err := c.GetAccounts(context.Background(), []string{"alice", "bob"})
	if err == nil {
		t.Fatal("GetAccounts succeeded on a length-mismatched response; want error")
	}
	if accts != nil {
		t.Fatalf("GetAccounts returned accounts alongside the error; want nil, got %d", len(accts))
	}
}
