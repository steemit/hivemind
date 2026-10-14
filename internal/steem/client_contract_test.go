package steem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

// ParamInt returns the i-th positional param coerced to int64. It fails the
// test when the param is absent or not a number.
func (c recordedRPCCall) ParamInt(t *testing.T, i int) int64 {
	t.Helper()
	var params []interface{}
	if err := json.Unmarshal(c.Params, &params); err != nil {
		t.Fatalf("harness: params of %s are not a positional array: %v", c.Method, err)
	}
	if i >= len(params) {
		t.Fatalf("harness: %s has no param %d (params: %s)", c.Method, i, c.Params)
	}
	num, ok := params[i].(float64)
	if !ok {
		t.Fatalf("harness: param %d of %s is not a number: %s", i, c.Method, c.Params)
	}
	return int64(num)
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

	s.mu.Lock()
	call := recordedRPCCall{Method: req.Method, Params: req.Params}
	seq := len(s.calls)
	s.calls = append(s.calls, call)
	resp := s.respond(call, seq)
	s.mu.Unlock()

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
	if got := s.call(0).ParamInt(t, 0); got != 1 {
		t.Fatalf("first request block param = %d, want 1", got)
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
	// answers 500 on every attempt, blocks 1 and 3 answer real blocks.
	s := newScriptedSteemd(t, func(call recordedRPCCall, seq int) rpcScriptResponse {
		if call.Method == "condenser_api.get_block" && call.ParamInt(t, 0) == 2 {
			return rpcScriptResponse{status: http.StatusInternalServerError, body: `boom`}
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
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: rpcOK(`[]`)}))
	c := newTestClient(t, s.URL(), 0)

	_, err := c.GetContentBatch(context.Background(), [][]string{
		{"alice", "p1"}, {"bob", "p2"},
	})
	if err != nil {
		t.Fatalf("GetContentBatch failed against permissive harness: %v", err)
	}
	if got := s.callCount(); got != 1 {
		t.Fatalf("got %d requests, want 1 (single non-batch request until PR#401)", got)
	}
	if got := s.call(0).Method; got != "condenser_api.get_content" {
		t.Fatalf("method = %q, want condenser_api.get_content", got)
	}
}
