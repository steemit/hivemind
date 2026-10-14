package steem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steemit/hivemind/pkg/config"
)

// ---------------------------------------------------------------------------
// Scriptable steemd harness (fake↔real contract tests, 06-quality §3.2)
//
// This harness impersonates a steemd JSON-RPC endpoint behind an
// httptest.Server so the REAL *Client can be tested against scripted
// responses: bad JSON, HTTP 429/5xx, null results, delays. The behaviors
// pinned here are the contract that test doubles (FakeProvider in this
// package and fakeSteemProvider in internal/indexer) must mirror.
//
// PR#401 delivered the get_content quirks: single requests whose params are
// an array of [author, permlink] pairs are rejected (the pre-#401 wire
// shape, which real steemd refuses), while request-object arrays are served
// per item. The harness records batch bodies with IsBatch=true and the
// per-item calls in Batch (method/params/id each), so tests can assert id
// alignment and chunk sizing on the wire.
//
// Any future case: add a TestContract* function; the harness needs no
// changes for sequence-, params-, or delay-driven scenarios.
// ---------------------------------------------------------------------------

// rpcScriptResponse is one scripted HTTP response.
type rpcScriptResponse struct {
	status int           // HTTP status code; 0 means 200
	body   string        // raw response body, written verbatim
	delay  time.Duration // optional sleep before responding (in-flight tests)

	// dribble, when non-zero, writes status/headers immediately and then
	// hands the body out in small flushed chunks with this pause between
	// them: a node whose headers are fast but whose body is slower than
	// the client's per-attempt timeout (body-read timeout contract tests).
	dribble time.Duration
}

// recordedRPCCall captures one JSON-RPC request received by the harness.
// For a batch POST body (a JSON array of request objects), IsBatch is true
// and Batch holds the per-item calls; ID is the raw wire id of the request
// (single) or item (batch), preserved for id-alignment assertions.
type recordedRPCCall struct {
	Method  string
	Params  json.RawMessage
	ID      json.RawMessage
	IsBatch bool
	Batch   []recordedRPCCall
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

// ParamStr returns the i-th positional param as a string, and whether the
// param exists and is a string. Same no-*testing.T rule as ParamInt.
func (c recordedRPCCall) ParamStr(i int) (string, bool) {
	var params []interface{}
	if err := json.Unmarshal(c.Params, &params); err != nil {
		return "", false
	}
	if i >= len(params) {
		return "", false
	}
	s, ok := params[i].(string)
	return s, ok
}

// ParamStrings returns the i-th positional param as a []string, and whether
// it exists and is an array of strings. Same no-*testing.T rule as ParamInt.
func (c recordedRPCCall) ParamStrings(i int) ([]string, bool) {
	var params []interface{}
	if err := json.Unmarshal(c.Params, &params); err != nil {
		return nil, false
	}
	if i >= len(params) {
		return nil, false
	}
	items, ok := params[i].([]interface{})
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// pairArrayParams reports whether params is [[a,p],[a,p],...] — the
// pre-#401 GetContentBatch wire shape (a single request whose params are an
// array of pairs) that real steemd rejects with a JSON-RPC error. Same
// no-*testing.T rule as ParamInt.
func (c recordedRPCCall) pairArrayParams() bool {
	var params []interface{}
	if err := json.Unmarshal(c.Params, &params); err != nil || len(params) == 0 {
		return false
	}
	_, ok := params[0].([]interface{})
	return ok
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

	trimmed := bytes.TrimSpace(body)
	var call recordedRPCCall
	if len(trimmed) > 0 && trimmed[0] == '[' {
		// JSON-RPC batch: a POST body that is an array of request objects.
		var items []struct {
			JsonRpc string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(trimmed, &items); err != nil {
			s.t.Logf("harness: unparsable batch request body: %v", err)
		}
		call = recordedRPCCall{IsBatch: true}
		for _, item := range items {
			call.Batch = append(call.Batch, recordedRPCCall{
				Method: item.Method, Params: item.Params, ID: item.ID,
			})
		}
	} else {
		var req struct {
			JsonRpc string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			s.t.Logf("harness: unparsable request body: %v", err)
		}
		call = recordedRPCCall{Method: req.Method, Params: req.Params, ID: req.ID}
	}

	resp := s.record(call)

	if resp.delay > 0 {
		time.Sleep(resp.delay)
	}
	status := resp.status
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")

	if resp.dribble > 0 {
		// Headers now, body later: flush the status line before the first
		// chunk so the client's Do() returns promptly and the timeout (if
		// any) fires while READING the body. Bail out as soon as a write
		// fails — the client has hung up on a timed-out attempt, and the
		// handler must not keep dribbling into a dead connection.
		w.WriteHeader(status)
		for len(resp.body) > 0 {
			n := min(len(resp.body), 16)
			if _, err := io.WriteString(w, resp.body[:n]); err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			resp.body = resp.body[n:]
			if len(resp.body) > 0 {
				time.Sleep(resp.dribble)
			}
		}
		return
	}

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
	return newTestClientCfg(t, &config.SteemConfig{
		URL:        url,
		MaxWorkers: 4,
		MaxRetries: maxRetries,
		RPCTimeout: 5 * time.Second,
	})
}

// newTestClientCfg builds a real Client from a full SteemConfig for tests
// that need non-default knobs (a small per-attempt RPCTimeout, a custom
// MaxBatch), such as the per-attempt timeout contract tests below.
func newTestClientCfg(t *testing.T, cfg *config.SteemConfig) *Client {
	t.Helper()
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

// batchContentJSON builds a minimal get_content result object for author/
// permlink (steemd's shape for a real post).
func batchContentJSON(author, permlink string) string {
	return fmt.Sprintf(`{"id":7,"author":%q,"permlink":%q,"depth":0}`, author, permlink)
}

// blankContentResult is steemd's blank-post shape for a get_content of a
// non-existent post: a valid object with an empty author (never null).
const blankContentResult = `{"id":0,"author":"","permlink":""}`

// contentResult returns the scripted get_content result for a tuple: the
// fixture body when known, steemd's blank post otherwise.
func contentResult(fixtures map[string]string, author, permlink string) string {
	if body, ok := fixtures[author+"/"+permlink]; ok {
		return body
	}
	return blankContentResult
}

// batchResponseBody renders a JSON-RPC batch response body for the given
// tuples from the fixtures (the same shape contentServerScript serves):
// one response object per tuple, 1-based ids, results in request order.
func batchResponseBody(fixtures map[string]string, tuples ...[]string) string {
	parts := make([]string, 0, len(tuples))
	for i, tuple := range tuples {
		parts = append(parts, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`,
			i+1, contentResult(fixtures, tuple[0], tuple[1])))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// contentServerScript impersonates the get_content surface of a real
// steemd/jussi endpoint:
//
//   - a JSON-RPC batch array (the PR#401 wire shape) is answered per item,
//     echoing each request's id, with results in request order;
//   - a single request whose params are [author, permlink] is answered with
//     one content object (the GetContent shape);
//   - a single request whose params are an ARRAY of pairs — the pre-#401
//     GetContentBatch wire shape — is REJECTED with a JSON-RPC error, as
//     real steemd does. This is the quirk that makes real batching
//     mandatory (02-indexer §1.1 P0-3).
//
// The closure runs on the HTTP handler goroutine: it must not fail the
// test, so marshaling failures answer 500.
func contentServerScript(fixtures map[string]string) func(call recordedRPCCall, seq int) rpcScriptResponse {
	return func(call recordedRPCCall, seq int) rpcScriptResponse {
		// A batch POST body carries its method per item (the outer call has
		// none); every item must be a get_content request.
		if call.IsBatch {
			for _, item := range call.Batch {
				if item.Method != "condenser_api.get_content" {
					return rpcScriptResponse{status: http.StatusInternalServerError, body: "harness: unexpected batch method " + item.Method}
				}
			}
		} else if call.Method != "condenser_api.get_content" {
			return rpcScriptResponse{status: http.StatusInternalServerError, body: "harness: unexpected method " + call.Method}
		}
		if !call.IsBatch {
			if call.pairArrayParams() {
				// Quirk: steemd cannot parse a params array of pairs.
				return rpcScriptResponse{body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"Invalid parameters: expected [author, permlink]"}}`}
			}
			author, _ := call.ParamStr(0)
			permlink, _ := call.ParamStr(1)
			return rpcScriptResponse{body: rpcOK(contentResult(fixtures, author, permlink))}
		}
		type respItem struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
		}
		out := make([]respItem, 0, len(call.Batch))
		for _, item := range call.Batch {
			author, _ := item.ParamStr(0)
			permlink, _ := item.ParamStr(1)
			out = append(out, respItem{
				JSONRPC: "2.0",
				ID:      item.ID,
				Result:  json.RawMessage(contentResult(fixtures, author, permlink)),
			})
		}
		payload, err := json.Marshal(out)
		if err != nil {
			return rpcScriptResponse{status: http.StatusInternalServerError, body: "harness: failed to marshal batch response"}
		}
		return rpcScriptResponse{body: string(payload)}
	}
}

// TestContractContentBatchWireShapeAndIDAlignment pins the PR#401 batch wire
// shape: ONE POST per chunk carrying a JSON-RPC request-object array —
// jsonrpc 2.0 envelope, condenser_api.get_content, per-item [author,
// permlink] params, 1-based per-chunk ids — with results returned in
// request order. This is what real steemd accepts (and the pre-#401 single
// request with pair-array params cannot do; see the quirk test below).
func TestContractContentBatchWireShapeAndIDAlignment(t *testing.T) {
	fixtures := map[string]string{
		"alice/p1": batchContentJSON("alice", "p1"),
		"bob/p2":   batchContentJSON("bob", "p2"),
		"carol/p3": batchContentJSON("carol", "p3"),
	}
	s := newScriptedSteemd(t, contentServerScript(fixtures))
	c := newTestClient(t, s.URL(), 1)

	posts, err := c.GetContentBatch(context.Background(), [][]string{
		{"alice", "p1"}, {"bob", "p2"}, {"carol", "p3"},
	})
	if err != nil {
		t.Fatalf("GetContentBatch failed against a well-behaved batch server: %v", err)
	}

	if got := s.callCount(); got != 1 {
		t.Fatalf("got %d POSTs, want 1 (3 posts fit one max_batch chunk)", got)
	}
	call := s.call(0)
	if !call.IsBatch {
		t.Fatalf("request was not a JSON-RPC batch array (params: %s)", call.Params)
	}
	if len(call.Batch) != 3 {
		t.Fatalf("batch carried %d request objects, want 3", len(call.Batch))
	}
	wantPairs := [][2]string{{"alice", "p1"}, {"bob", "p2"}, {"carol", "p3"}}
	for i, item := range call.Batch {
		if item.Method != "condenser_api.get_content" {
			t.Fatalf("batch item %d method = %q, want condenser_api.get_content", i, item.Method)
		}
		author, aok := item.ParamStr(0)
		permlink, pok := item.ParamStr(1)
		if !aok || !pok || author != wantPairs[i][0] || permlink != wantPairs[i][1] {
			t.Fatalf("batch item %d params = (%q,%q) (ok=%v,%v), want (%q,%q)",
				i, author, permlink, aok, pok, wantPairs[i][0], wantPairs[i][1])
		}
		// Legacy http_client.rpc_body assigns per-chunk 1-based ids.
		if got, want := string(item.ID), fmt.Sprintf("%d", i+1); got != want {
			t.Fatalf("batch item %d wire id = %s, want %s", i, got, want)
		}
	}

	// Results come back in request order, one per request.
	if len(posts) != 3 {
		t.Fatalf("got %d results, want 3", len(posts))
	}
	for i, want := range wantPairs {
		if got := posts[i]["author"]; got != want[0] {
			t.Fatalf("result %d author = %v, want %q (request order not preserved)", i, got, want[0])
		}
	}
}

// TestContractContentBatchPairArrayQuirkRejected pins WHY real batching is
// mandatory (02-indexer §1.1 P0-3): steemd rejects a single
// condenser_api.get_content request whose params are an array of
// [author, permlink] pairs — the exact call the pre-#401 GetContentBatch
// made through the SDK. The script answers that shape with steemd's
// JSON-RPC error, and the replay through the same SDK machinery the old
// code used must surface it as an error; the current client, against the
// same server, succeeds because it sends a request-object array.
func TestContractContentBatchPairArrayQuirkRejected(t *testing.T) {
	fixtures := map[string]string{
		"alice/p1": batchContentJSON("alice", "p1"),
		"bob/p2":   batchContentJSON("bob", "p2"),
	}
	s := newScriptedSteemd(t, contentServerScript(fixtures))
	c := newTestClient(t, s.URL(), 1)

	t.Run("old_shape_single_request_is_rejected", func(t *testing.T) {
		// Replay the pre-#401 call verbatim: one request whose params are
		// the pair array. The scripted server (like real steemd) answers a
		// JSON-RPC error, which the SDK must surface.
		var out []map[string]interface{}
		err := c.api.CallWithResult(context.Background(), "condenser_api", "get_content",
			[]interface{}{[]interface{}{"alice", "p1"}, []interface{}{"bob", "p2"}}, &out)
		if err == nil {
			t.Fatal("pair-array params were accepted by the scripted steemd; want the quirk rejection (JSON-RPC error)")
		}
		if out != nil {
			t.Fatalf("old-shape call returned results alongside the error; want nil, got %v", out)
		}
		if got := s.call(0).Method; got != "condenser_api.get_content" {
			t.Fatalf("method = %q, want condenser_api.get_content", got)
		}
		if !s.call(0).pairArrayParams() {
			t.Fatal("replayed request did not carry the pair-array params; the replay no longer pins the quirk")
		}
	})

	t.Run("batch_array_shape_is_served", func(t *testing.T) {
		// The same server serves the batch array shape without retry: the
		// success itself proves the client sends the accepted shape (the
		// script rejects the old shape on sight).
		posts, err := c.GetContentBatch(context.Background(), [][]string{
			{"alice", "p1"}, {"bob", "p2"},
		})
		if err != nil {
			t.Fatalf("GetContentBatch failed against the same server that rejects the old shape: %v", err)
		}
		if len(posts) != 2 || posts[0]["author"] != "alice" || posts[1]["author"] != "bob" {
			t.Fatalf("results = %v, want [alice, bob] in request order", posts)
		}
		if !s.call(1).IsBatch {
			t.Fatal("successful request was not a batch array; the client regressed to the rejected shape")
		}
	})
}

// TestContractContentBatchChunksAtMaxBatch pins the legacy chunking and
// concurrency model: requests are split into max_batch-sized chunks
// (HIVE_MAX_BATCH, default 50), the chunks are fetched concurrently on the
// max_workers-bounded pool, and results are reassembled in REQUEST order
// regardless of chunk completion order.
func TestContractContentBatchChunksAtMaxBatch(t *testing.T) {
	const total = 101 // 50 + 50 + 1
	fixtures := make(map[string]string, total)
	tuples := make([][]string, total)
	for i := range tuples {
		author := fmt.Sprintf("auth%03d", i)
		tuples[i] = []string{author, "p"}
		fixtures[author+"/p"] = batchContentJSON(author, "p")
	}
	s := newScriptedSteemd(t, contentServerScript(fixtures))
	c := newTestClient(t, s.URL(), 1) // MaxBatch unset -> sanitized default 50

	posts, err := c.GetContentBatch(context.Background(), tuples)
	if err != nil {
		t.Fatalf("GetContentBatch failed across chunk boundaries: %v", err)
	}

	if got := s.callCount(); got != 3 {
		t.Fatalf("got %d batch POSTs, want 3 (101 posts / max_batch 50)", got)
	}
	seen := make(map[string]int)
	for i := 0; i < s.callCount(); i++ {
		call := s.call(i)
		if !call.IsBatch {
			t.Fatalf("POST %d was not a batch array", i)
		}
		if len(call.Batch) > 50 {
			t.Fatalf("POST %d carried %d items, want <= max_batch (50)", i, len(call.Batch))
		}
		for j, item := range call.Batch {
			author, ok := item.ParamStr(0)
			if !ok {
				t.Fatalf("POST %d item %d has no string author param", i, j)
			}
			seen[author]++
			// Per-chunk ids restart at 1 (legacy rpc_body semantics).
			if got, want := string(item.ID), fmt.Sprintf("%d", j+1); got != want {
				t.Fatalf("POST %d item %d wire id = %s, want %s (per-chunk 1-based)", i, j, got, want)
			}
		}
	}
	if len(seen) != total {
		t.Fatalf("distinct authors across chunks = %d, want %d", len(seen), total)
	}
	for author, n := range seen {
		if n != 1 {
			t.Fatalf("author %s requested %d times, want exactly once", author, n)
		}
	}

	// Request-order reassembly: result i is exactly the i-th request's post,
	// even though the chunks were fetched concurrently.
	if len(posts) != total {
		t.Fatalf("got %d results, want %d", len(posts), total)
	}
	for i, post := range posts {
		if got := post["author"]; got != fmt.Sprintf("auth%03d", i) {
			t.Fatalf("result %d author = %v, want auth%03d (request order not preserved)", i, got, i)
		}
	}
}

// TestContractContentBatchTransientFailures pins 429/bad-JSON behavior on
// the batch path: a 429 is retried after a doubled exponential backoff (the
// wrapper-level retry is the ONLY retry layer there, mirroring the SDK
// transport used by single calls), a persistent 429 exhausts into an error
// after exactly maxRetries+1 attempts, and a malformed 200 body is an
// immediate error — never retried, never an empty success.
func TestContractContentBatchTransientFailures(t *testing.T) {
	t.Run("429_backs_off_then_succeeds", func(t *testing.T) {
		alice := map[string]string{"alice/p1": batchContentJSON("alice", "p1")}
		s := newScriptedSteemd(t, func(call recordedRPCCall, seq int) rpcScriptResponse {
			if seq == 0 {
				return rpcScriptResponse{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`}
			}
			return contentServerScript(alice)(call, seq)
		})
		c := newTestClient(t, s.URL(), 3)

		start := time.Now()
		posts, err := c.GetContentBatch(context.Background(), [][]string{{"alice", "p1"}})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("GetContentBatch failed on a transient 429: %v", err)
		}
		if got := s.callCount(); got != 2 {
			t.Fatalf("got %d POSTs, want 2 (one 429, one success)", got)
		}
		// Batch backoff mirrors the SDK: 100ms base << 0, doubled on 429
		// = 200ms. Assert a lower bound below that for timer robustness.
		if elapsed < 150*time.Millisecond {
			t.Fatalf("batch retry came back too fast (%v); 429 backoff not applied", elapsed)
		}
		if len(posts) != 1 || posts[0]["author"] != "alice" {
			t.Fatalf("retried batch returned %v, want alice's post", posts)
		}
	})

	t.Run("429_exhaustion_is_error", func(t *testing.T) {
		s := newScriptedSteemd(t, func(call recordedRPCCall, seq int) rpcScriptResponse {
			return rpcScriptResponse{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`}
		})
		c := newTestClient(t, s.URL(), 1) // initial attempt + 1 retry

		posts, err := c.GetContentBatch(context.Background(), [][]string{{"alice", "p1"}})
		if err == nil {
			t.Fatal("GetContentBatch succeeded after retries were exhausted; want error")
		}
		if posts != nil {
			t.Fatalf("GetContentBatch returned results alongside the error; want nil, got %d", len(posts))
		}
		if got := s.callCount(); got != 2 {
			t.Fatalf("got %d POSTs, want 2 (initial attempt + 1 retry)", got)
		}
	})

	t.Run("bad_json_body_is_error_not_retried", func(t *testing.T) {
		s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
			// Truncated batch array: valid prefix, broken payload.
			body: `[{"jsonrpc":"2.0","id":1,"result":{"author":"ali`,
		}))
		c := newTestClient(t, s.URL(), 3)

		posts, err := c.GetContentBatch(context.Background(), [][]string{{"alice", "p1"}, {"bob", "p2"}})
		if err == nil {
			t.Fatal("GetContentBatch succeeded on a bad JSON batch response; want error")
		}
		if posts != nil {
			t.Fatalf("GetContentBatch returned results alongside the error; want nil, got %d", len(posts))
		}
		if got := s.callCount(); got != 1 {
			t.Fatalf("bad JSON must not be retried (non-transient); got %d POSTs, want 1", got)
		}
	})
}

// TestContractContentBatchPerAttemptTimeoutRetried pins the PR#401 review
// fix: a per-attempt transport timeout (http.Client.Timeout expiring while
// the caller's context is still live) is TRANSIENT on the batch path — one
// slow steemd response must not kill the whole batch. The failure is retried
// after backoff and succeeds once the node answers promptly, matching the
// SDK path's treatment of the same condition (ErrTimeout → retryable).
//
// On Go 1.23+ the raw http.Client.Timeout error also satisfies
// errors.Is(err, context.DeadlineExceeded), which the pre-fix isBatchRetryable
// trusted to mean "caller deadline" — with that misclassification the call
// below failed after ONE request instead of retrying.
func TestContractContentBatchPerAttemptTimeoutRetried(t *testing.T) {
	alice := map[string]string{"alice/p1": batchContentJSON("alice", "p1")}
	body := batchResponseBody(alice, []string{"alice", "p1"})
	// First request stalls past the 200ms per-attempt timeout; the retry is
	// served immediately.
	s := newScriptedSteemd(t, seqScript(
		rpcScriptResponse{delay: 500 * time.Millisecond, body: body},
		rpcScriptResponse{body: body},
	))
	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        s.URL(),
		MaxWorkers: 1,
		MaxRetries: 3,
		RPCTimeout: 200 * time.Millisecond,
	})

	start := time.Now()
	posts, err := c.GetContentBatch(context.Background(), [][]string{{"alice", "p1"}})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetContentBatch failed on a transient per-attempt timeout: %v", err)
	}
	if len(posts) != 1 || posts[0]["author"] != "alice" {
		t.Fatalf("retried batch returned %v, want alice's post", posts)
	}
	if got := s.callCount(); got != 2 {
		t.Fatalf("got %d POSTs, want 2 (one timed-out attempt, one success)", got)
	}
	// ~200ms timeout + 100ms base backoff before the retry; a lower bound
	// under that proves both the in-flight cutoff AND the backoff happened.
	if elapsed < 250*time.Millisecond {
		t.Fatalf("retry came back too fast (%v); timeout cutoff + backoff not applied", elapsed)
	}
}

// TestContractContentBatchBodyReadTimeoutRetried pins the PR#401 review-r2
// fix: http.Client.Timeout also covers READING the response body, so a node
// that answers headers promptly but dribbles the body out slower than the
// per-attempt timeout fails the json.Decoder read — not the POST — with a
// timeout-shaped error (net/http timeoutError: a net.Error with Timeout()
// true that on Go 1.23+ ALSO satisfies errors.Is(err,
// context.DeadlineExceeded)) while the caller's context is still live.
// Before the fix both Decode failures in postBatch were wrapped as
// "malformed" — non-retryable, with the trailing-data check mislabeling the
// second one — so a single slow body killed the whole chunk after ONE
// attempt. The variant pinned here is retry-then-succeed: the first response
// dribbles (body unfinished well past the timeout), the retry is served
// whole, and the call succeeds on exactly two POSTs.
func TestContractContentBatchBodyReadTimeoutRetried(t *testing.T) {
	// Pad the item result so the dribbled body cannot finish before the
	// 200ms per-attempt timeout: ~2 KiB / 16 B per 25 ms ≈ 3 s total, so
	// the timeout lands mid-array with certainty (the harness stops
	// dribbling as soon as the timed-out client hangs up).
	alice := map[string]string{"alice/p1": strings.TrimSuffix(batchContentJSON("alice", "p1"), "}") +
		`,"padding":"` + strings.Repeat("x", 2048) + `"}`}
	body := batchResponseBody(alice, []string{"alice", "p1"})

	s := newScriptedSteemd(t, seqScript(
		rpcScriptResponse{dribble: 25 * time.Millisecond, body: body},
		rpcScriptResponse{body: body},
	))
	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        s.URL(),
		MaxWorkers: 1,
		MaxRetries: 3,
		RPCTimeout: 200 * time.Millisecond,
	})

	posts, err := c.GetContentBatch(context.Background(), [][]string{{"alice", "p1"}})
	if err != nil {
		t.Fatalf("GetContentBatch failed on a transient body-read timeout: %v", err)
	}
	if len(posts) != 1 || posts[0]["author"] != "alice" {
		t.Fatalf("retried batch returned %v, want alice's post", posts)
	}
	if got := s.callCount(); got != 2 {
		t.Fatalf("got %d POSTs, want 2 (one body-read timeout, one success)", got)
	}
}

// TestContractContentBatchCallerDeadlineNotRetried pins the other half of the
// PR#401 fix: a CALLER-side context deadline stays non-retryable on the batch
// path. With the per-attempt timeout generous and the caller's deadline
// short, exactly one request goes out and the error wraps
// context.DeadlineExceeded — the caller gave up, so no retry storm.
func TestContractContentBatchCallerDeadlineNotRetried(t *testing.T) {
	alice := map[string]string{"alice/p1": batchContentJSON("alice", "p1")}
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
		delay: 800 * time.Millisecond,
		body:  batchResponseBody(alice, []string{"alice", "p1"}),
	}))
	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        s.URL(),
		MaxWorkers: 1,
		MaxRetries: 3,
		RPCTimeout: 5 * time.Second, // per-attempt timeout never fires here
	})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	posts, err := c.GetContentBatch(ctx, [][]string{{"alice", "p1"}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("GetContentBatch succeeded despite caller deadline; want context error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetContentBatch error = %v; want context.DeadlineExceeded in the chain", err)
	}
	if posts != nil {
		t.Fatalf("GetContentBatch returned results alongside the error; want nil, got %d", len(posts))
	}
	if got := s.callCount(); got != 1 {
		t.Fatalf("caller cancellation must not be retried; got %d POSTs, want 1", got)
	}
	if elapsed >= time.Second {
		t.Fatalf("cancellation did not abort the in-flight batch (%v); ctx is not passed through", elapsed)
	}
}

// TestContractContentBatchTimeoutExhaustionIsError pins the failure twin of
// the recovery case: when the node stalls past the per-attempt timeout on
// every attempt, the batch errors after exactly maxRetries+1 attempts, the
// final error carrying the errBatchTimeout sentinel.
func TestContractContentBatchTimeoutExhaustionIsError(t *testing.T) {
	alice := map[string]string{"alice/p1": batchContentJSON("alice", "p1")}
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
		delay: 500 * time.Millisecond,
		body:  batchResponseBody(alice, []string{"alice", "p1"}),
	}))
	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        s.URL(),
		MaxWorkers: 1,
		MaxRetries: 1, // initial attempt + 1 retry
		RPCTimeout: 200 * time.Millisecond,
	})

	posts, err := c.GetContentBatch(context.Background(), [][]string{{"alice", "p1"}})
	if err == nil {
		t.Fatal("GetContentBatch succeeded while every attempt timed out; want error")
	}
	if !errors.Is(err, errBatchTimeout) {
		t.Fatalf("GetContentBatch error = %v; want errBatchTimeout in the chain", err)
	}
	if posts != nil {
		t.Fatalf("GetContentBatch returned results alongside the error; want nil, got %d", len(posts))
	}
	if got := s.callCount(); got != 2 {
		t.Fatalf("got %d POSTs, want 2 (initial attempt + 1 retry)", got)
	}
}

// TestContractContentBatchResponseBodyCapped pins the memory guard on the
// batch path: a 200 response larger than max_batch × per-item bound (+slack)
// is rejected as malformed after ONE attempt, never buffered whole — a
// compromised node or MITM cannot balloon process memory through the batch
// decoder. MaxBatch=1 caps the response at ~4.06 MiB (4 MiB per item + 64 KiB
// envelope slack); the body below is ~6 MiB.
func TestContractContentBatchResponseBodyCapped(t *testing.T) {
	huge := strings.Repeat("x", 6<<20)
	s := newScriptedSteemd(t, seqScript(rpcScriptResponse{
		body: `[{"jsonrpc":"2.0","id":1,"result":{"author":"alice","permlink":"p1","junk":"` + huge + `"}}]`,
	}))
	c := newTestClientCfg(t, &config.SteemConfig{
		URL:        s.URL(),
		MaxWorkers: 1,
		MaxRetries: 1,
		MaxBatch:   1, // cap = 1*4MiB + 64KiB envelope slack
		RPCTimeout: 5 * time.Second,
	})

	posts, err := c.GetContentBatch(context.Background(), [][]string{{"alice", "p1"}})
	if err == nil {
		t.Fatal("GetContentBatch succeeded on an over-cap response body; want malformed error")
	}
	if posts != nil {
		t.Fatalf("GetContentBatch returned results alongside the error; want nil, got %d", len(posts))
	}
	if got := s.callCount(); got != 1 {
		t.Fatalf("an over-cap body must not be retried; got %d POSTs, want 1", got)
	}
}

// TestContractContentBatchItemValidationFailsWholeBatch pins the
// all-or-nothing per-item validation (legacy _validated_batch_result): any
// single malformed or failing item — a JSON-RPC error object, a null or
// missing result, a result without an author field, a response/request
// length mismatch, or an id misalignment — fails the WHOLE call with no
// results. The error semantics decision (a per-item error fails the batch
// rather than erroring just that item) mirrors legacy and lets callers
// retry or abort the whole batch, exactly like GetBlocksRange.
func TestContractContentBatchItemValidationFailsWholeBatch(t *testing.T) {
	cases := []struct {
		name   string
		script func(call recordedRPCCall, seq int) rpcScriptResponse
	}{
		{
			name: "item_jsonrpc_error",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				return rpcScriptResponse{body: `[{"jsonrpc":"2.0","id":1,"result":{"author":"alice","permlink":"p1"}},{"jsonrpc":"2.0","id":2,"error":{"code":-32000,"message":"boom"}}]`}
			},
		},
		{
			name: "item_null_result",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				return rpcScriptResponse{body: `[{"jsonrpc":"2.0","id":1,"result":null},{"jsonrpc":"2.0","id":2,"result":{"author":"bob","permlink":"p2"}}]`}
			},
		},
		{
			name: "item_result_without_author",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				return rpcScriptResponse{body: `[{"jsonrpc":"2.0","id":1,"result":{"permlink":"p1"}}]`}
			},
		},
		{
			name: "length_mismatch",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				return rpcScriptResponse{body: `[{"jsonrpc":"2.0","id":1,"result":{"author":"alice","permlink":"p1"}}]`}
			},
		},
		{
			name: "id_misalignment",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				return rpcScriptResponse{body: `[{"jsonrpc":"2.0","id":2,"result":{"author":"alice","permlink":"p1"}},{"jsonrpc":"2.0","id":1,"result":{"author":"bob","permlink":"p2"}}]`}
			},
		},
		{
			name: "single_object_body_instead_of_array",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				return rpcScriptResponse{body: rpcOK(`[{"author":"alice"},{"author":"bob"}]`)}
			},
		},
		{
			name: "null_body",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				return rpcScriptResponse{body: `null`}
			},
		},
		// Trailing data after the response array is rejected, aligning the
		// batch path with legacy's json.loads whole-body parse (json.Decoder
		// alone would happily ignore everything after the first value).
		{
			name: "trailing_garbage",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				return rpcScriptResponse{body: `[{"jsonrpc":"2.0","id":1,"result":{"author":"alice","permlink":"p1"}},{"jsonrpc":"2.0","id":2,"result":{"author":"bob","permlink":"p2"}}]xx`}
			},
		},
		{
			name: "trailing_second_value",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				return rpcScriptResponse{body: `[{"jsonrpc":"2.0","id":1,"result":{"author":"alice","permlink":"p1"}}] {"extra":true}`}
			},
		},
		{
			name: "trailing_close_bracket",
			script: func(call recordedRPCCall, seq int) rpcScriptResponse {
				// A stray ']' is invisible to Decoder.More but still
				// "Extra data" to a whole-body parse (json.loads).
				return rpcScriptResponse{body: `[{"jsonrpc":"2.0","id":1,"result":{"author":"alice","permlink":"p1"}}]]`}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScriptedSteemd(t, tc.script)
			c := newTestClient(t, s.URL(), 1)

			posts, err := c.GetContentBatch(context.Background(), [][]string{
				{"alice", "p1"}, {"bob", "p2"},
			})
			if err == nil {
				t.Fatal("GetContentBatch succeeded on a malformed batch response; want error")
			}
			if posts != nil {
				t.Fatalf("GetContentBatch returned results alongside the error; want nil, got %d", len(posts))
			}
			// Malformed responses are deterministic: never retried.
			if got := s.callCount(); got != 1 {
				t.Fatalf("malformed response must not be retried; got %d POSTs, want 1", got)
			}
		})
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

// TestContractContentBatchEmptyAndInputGuards pins the defensive input
// paths carried over from the PR#400 review plus the #402-registered items:
// the empty-request fast path (no wire traffic, empty non-nil slice, nil
// error), malformed tuples rejected before any request, and the GetBlock /
// GetBlocksRange argument guards (block numbers are 1-based and ranges are
// ordered — steemd block numbers are contiguous, so an invalid fetch window
// must never reach the wire).
func TestContractContentBatchEmptyAndInputGuards(t *testing.T) {
	s := newScriptedSteemd(t, contentServerScript(nil))
	c := newTestClient(t, s.URL(), 1)

	t.Run("empty_request_fast_path", func(t *testing.T) {
		posts, err := c.GetContentBatch(context.Background(), nil)
		if err != nil {
			t.Fatalf("GetContentBatch(nil) errored: %v; want the empty fast path", err)
		}
		if posts == nil {
			t.Fatal("GetContentBatch(nil) returned a nil slice; want an empty non-nil slice")
		}
		if len(posts) != 0 {
			t.Fatalf("GetContentBatch(nil) returned %d posts, want 0", len(posts))
		}
		posts, err = c.GetContentBatch(context.Background(), [][]string{})
		if err != nil || len(posts) != 0 || posts == nil {
			t.Fatalf("GetContentBatch(empty) = (%v, %v); want (empty, nil)", posts, err)
		}
		if got := s.callCount(); got != 0 {
			t.Fatalf("empty request must not hit the wire; got %d POSTs", got)
		}
	})

	t.Run("malformed_tuple_rejected_before_request", func(t *testing.T) {
		if _, err := c.GetContentBatch(context.Background(), [][]string{{"authorless"}}); err == nil {
			t.Fatal("GetContentBatch accepted a malformed [author] tuple; want input validation error")
		}
		if got := s.callCount(); got != 0 {
			t.Fatalf("malformed tuples must fail before any request is sent; got %d POSTs", got)
		}
	})

	t.Run("get_block_num_guard", func(t *testing.T) {
		for _, num := range []int64{0, -1, -12345} {
			if _, err := c.GetBlock(context.Background(), num); err == nil {
				t.Fatalf("GetBlock(%d) succeeded; want invalid-block-number error", num)
			}
		}
		if got := s.callCount(); got != 0 {
			t.Fatalf("invalid block numbers must fail before any request is sent; got %d POSTs", got)
		}
	})

	t.Run("get_blocks_range_bounds_guard", func(t *testing.T) {
		// from must be >= 1 and <= to ([from,to] is inclusive, so {5,5}
		// is a valid single-block range and deliberately absent here).
		for _, tc := range [][2]int64{{0, 5}, {-3, 5}, {5, 4}} {
			from, to := tc[0], tc[1]
			blocks, err := c.GetBlocksRange(context.Background(), from, to)
			if err == nil {
				t.Fatalf("GetBlocksRange(%d, %d) succeeded; want range validation error", from, to)
			}
			if blocks != nil {
				t.Fatalf("GetBlocksRange(%d, %d) returned blocks alongside the error; want nil", from, to)
			}
		}
		if got := s.callCount(); got != 0 {
			t.Fatalf("invalid ranges must fail before any request is sent; got %d POSTs", got)
		}
	})
}

// TestContractGetContentAndGetAccountsNullAreErrors pins the null-result
// branches for the single-object calls (#402-registered item): steemd never
// legitimately answers get_content or get_accounts with null, so a null is
// a malformed response and must surface as an error — never a nil/empty
// result with a nil error that callers would treat as "post/account gone".
func TestContractGetContentAndGetAccountsNullAreErrors(t *testing.T) {
	t.Run("get_content_null", func(t *testing.T) {
		s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: rpcOK(`null`)}))
		c := newTestClient(t, s.URL(), 1)

		content, err := c.GetContent(context.Background(), "alice", "p1")
		if err == nil {
			t.Fatal("GetContent succeeded on a null result; want malformed-response error")
		}
		if content != nil {
			t.Fatalf("GetContent returned content alongside the error; want nil, got %v", content)
		}
	})

	t.Run("get_accounts_null", func(t *testing.T) {
		s := newScriptedSteemd(t, seqScript(rpcScriptResponse{body: rpcOK(`null`)}))
		c := newTestClient(t, s.URL(), 1)

		accounts, err := c.GetAccounts(context.Background(), []string{"alice"})
		if err == nil {
			t.Fatal("GetAccounts succeeded on a null result; want malformed-response error")
		}
		if accounts != nil {
			t.Fatalf("GetAccounts returned accounts alongside the error; want nil, got %d", len(accounts))
		}
	})
}

// ---------------------------------------------------------------------------
// fake ↔ real equivalence (06-quality §三-3.2)
// ---------------------------------------------------------------------------

// contentFixturesPR401 is the shared wire dataset: fixture bodies served by
// the scripted server AND consumed by the model FakeProvider, so both sides
// of the equivalence test decode byte-identical JSON.
var contentFixturesPR401 = map[string]string{
	"alice/p1": `{"id":11,"author":"alice","permlink":"p1","depth":0,"category":"life"}`,
	"bob/p2":   `{"id":22,"author":"bob","permlink":"p2","depth":1,"category":"test"}`,
}

// accountFixture builds the scripted get_accounts entry for a name.
func accountFixture(name string) string {
	return fmt.Sprintf(`{"name":%q,"post_count":7}`, name)
}

// newModelFakeProvider builds a FakeProvider whose Fns implement the
// Provider contract exactly as documented in provider.go: input validation
// and the empty fast path behave like the real client, results come from
// the same fixture dataset the scripted server serves, and failures carry
// errors. This is the model every test double should imitate.
func newModelFakeProvider(fixtures map[string]string, batchErr error) *FakeProvider {
	fake := &FakeProvider{}
	fake.GetContentBatchFn = func(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
		if batchErr != nil {
			return nil, batchErr
		}
		if len(posts) == 0 {
			return []map[string]interface{}{}, nil
		}
		out := make([]map[string]interface{}, 0, len(posts))
		for i, p := range posts {
			if len(p) < 2 {
				return nil, fmt.Errorf("invalid post tuple at index %d: want [author, permlink], got %d element(s)", i, len(p))
			}
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(contentResult(fixtures, p[0], p[1])), &m); err != nil {
				return nil, err
			}
			if _, ok := m["author"]; !ok {
				return nil, fmt.Errorf("invalid post: no author field")
			}
			out = append(out, m)
		}
		return out, nil
	}
	fake.GetContentFn = func(ctx context.Context, author, permlink string) (map[string]interface{}, error) {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(contentResult(fixtures, author, permlink)), &m); err != nil {
			return nil, err
		}
		if m == nil {
			return nil, fmt.Errorf("unexpected null result")
		}
		return m, nil
	}
	fake.GetAccountsFn = func(ctx context.Context, names []string) ([]map[string]interface{}, error) {
		if len(names) == 0 {
			return nil, fmt.Errorf("no account names provided")
		}
		out := make([]map[string]interface{}, 0, len(names))
		for _, n := range names {
			var m map[string]interface{}
			if err := json.Unmarshal([]byte(accountFixture(n)), &m); err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, nil
	}
	return fake
}

// TestContractFakeProviderMatchesRealClient is the 06-quality §三-3.2
// equivalence assertion: against the same scripted steemd, the real *Client
// and a contract-faithful FakeProvider must return equivalent results (or
// equivalent errors) for the same inputs. If the real client's semantics
// drift from what test doubles imitate, this test fails — that is the
// moment to update the fakes, not after indexer tests quietly pass with a
// stale double.
func TestContractFakeProviderMatchesRealClient(t *testing.T) {
	// The server speaks the full get_content/get_accounts surface.
	s := newScriptedSteemd(t, func(call recordedRPCCall, seq int) rpcScriptResponse {
		if call.Method == "condenser_api.get_accounts" {
			names, ok := call.ParamStrings(0)
			if !ok {
				return rpcScriptResponse{status: http.StatusInternalServerError, body: "harness: missing names param"}
			}
			out := make([]string, 0, len(names))
			for _, n := range names {
				out = append(out, accountFixture(n))
			}
			return rpcScriptResponse{body: rpcOK("[" + strings.Join(out, ",") + "]")}
		}
		return contentServerScript(contentFixturesPR401)(call, seq)
	})
	real := newTestClient(t, s.URL(), 1)

	t.Run("get_content_batch", func(t *testing.T) {
		cases := []struct {
			name  string
			input [][]string
		}{
			{name: "nil_request", input: nil},
			{name: "empty_request", input: [][]string{}},
			{name: "single", input: [][]string{{"alice", "p1"}}},
			{name: "mixed_known_and_blank", input: [][]string{{"alice", "p1"}, {"ghost", "nope"}, {"bob", "p2"}}},
			{name: "malformed_tuple", input: [][]string{{"alice", "p1"}, {"authorless"}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fake := newModelFakeProvider(contentFixturesPR401, nil)

				realRes, realErr := real.GetContentBatch(context.Background(), tc.input)
				fakeRes, fakeErr := fake.GetContentBatch(context.Background(), tc.input)

				if (realErr == nil) != (fakeErr == nil) {
					t.Fatalf("error presence differs: real err=%v, fake err=%v", realErr, fakeErr)
				}
				if realErr != nil {
					if realRes != nil || fakeRes != nil {
						t.Fatalf("failed calls must return nil results: real=%v fake=%v", realRes, fakeRes)
					}
					return
				}
				if !reflect.DeepEqual(realRes, fakeRes) {
					t.Fatalf("results differ:\nreal: %#v\nfake: %#v", realRes, fakeRes)
				}
			})
		}
	})

	t.Run("get_content_batch_failure_equivalence", func(t *testing.T) {
		// A persistently failing server vs a fake scripted to fail: both
		// must surface (nil, error) — the contract shape indexer callers
		// rely on to retry or abort.
		failing := newScriptedSteemd(t, func(call recordedRPCCall, seq int) rpcScriptResponse {
			return rpcScriptResponse{status: http.StatusTooManyRequests, body: `{"error":"rate limited"}`}
		})
		realFailing := newTestClient(t, failing.URL(), 1)
		fakeFailing := newModelFakeProvider(contentFixturesPR401, fmt.Errorf("scripted batch failure"))

		input := [][]string{{"alice", "p1"}, {"bob", "p2"}}
		realRes, realErr := realFailing.GetContentBatch(context.Background(), input)
		fakeRes, fakeErr := fakeFailing.GetContentBatch(context.Background(), input)

		if realErr == nil || fakeErr == nil {
			t.Fatalf("both must fail: real err=%v, fake err=%v", realErr, fakeErr)
		}
		if realRes != nil || fakeRes != nil {
			t.Fatalf("failed calls must return nil results: real=%v fake=%v", realRes, fakeRes)
		}
	})

	t.Run("get_content_single", func(t *testing.T) {
		fake := newModelFakeProvider(contentFixturesPR401, nil)
		for _, tuple := range [][]string{{"alice", "p1"}, {"ghost", "nope"}} {
			realRes, realErr := real.GetContent(context.Background(), tuple[0], tuple[1])
			fakeRes, fakeErr := fake.GetContent(context.Background(), tuple[0], tuple[1])
			if (realErr == nil) != (fakeErr == nil) {
				t.Fatalf("@%s/%s: error presence differs: real=%v fake=%v", tuple[0], tuple[1], realErr, fakeErr)
			}
			if realErr == nil && !reflect.DeepEqual(realRes, fakeRes) {
				t.Fatalf("@%s/%s results differ:\nreal: %#v\nfake: %#v", tuple[0], tuple[1], realRes, fakeRes)
			}
		}
	})

	t.Run("get_accounts", func(t *testing.T) {
		fake := newModelFakeProvider(contentFixturesPR401, nil)

		realRes, realErr := real.GetAccounts(context.Background(), []string{"alice", "bob"})
		fakeRes, fakeErr := fake.GetAccounts(context.Background(), []string{"alice", "bob"})
		if realErr != nil || fakeErr != nil {
			t.Fatalf("unexpected errors: real=%v fake=%v", realErr, fakeErr)
		}
		if !reflect.DeepEqual(realRes, fakeRes) {
			t.Fatalf("accounts differ:\nreal: %#v\nfake: %#v", realRes, fakeRes)
		}

		// Empty-name list: both reject (never an empty success that would
		// mask a caller bug).
		if _, realErr := real.GetAccounts(context.Background(), nil); realErr == nil {
			t.Fatal("real GetAccounts(nil) succeeded; want error")
		}
		if _, fakeErr := fake.GetAccounts(context.Background(), nil); fakeErr == nil {
			t.Fatal("fake GetAccounts(nil) succeeded; want error")
		}
	})
}

// TestFakeProviderUnsetFnFailsLoud pins the #402-registered fix: a
// FakeProvider method invoked without its Fn set must return an explicit
// error — never (nil, nil), which the Provider contract forbids on failure
// paths and which would let production bugs pass in tests that forgot to
// script a response. The sole sanctioned exception is GetContentBatch's
// empty-request fast path, which mirrors the real client and succeeds
// without consulting the Fn.
func TestFakeProviderUnsetFnFailsLoud(t *testing.T) {
	fake := &FakeProvider{}
	ctx := context.Background()

	if res, err := fake.GetBlock(ctx, 1); err == nil || res != nil {
		t.Fatalf("GetBlock (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetBlocksRange(ctx, 1, 2); err == nil || res != nil {
		t.Fatalf("GetBlocksRange (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetAccounts(ctx, []string{"a"}); err == nil || res != nil {
		t.Fatalf("GetAccounts (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetContent(ctx, "a", "p"); err == nil || res != nil {
		t.Fatalf("GetContent (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetContentBatch(ctx, [][]string{{"a", "p"}}); err == nil || res != nil {
		t.Fatalf("GetContentBatch (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetDynamicGlobalProperties(ctx); err == nil || res != nil {
		t.Fatalf("GetDynamicGlobalProperties (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.HeadBlock(ctx); err == nil || res != 0 {
		t.Fatalf("HeadBlock (Fn unset) = (%v, %v); want (0, error)", res, err)
	}
	if res, err := fake.LastIrreversible(ctx); err == nil || res != 0 {
		t.Fatalf("LastIrreversible (Fn unset) = (%v, %v); want (0, error)", res, err)
	}

	// Empty-request fast path: mirrors the real client even without an Fn.
	res, err := fake.GetContentBatch(ctx, nil)
	if err != nil || res == nil || len(res) != 0 {
		t.Fatalf("GetContentBatch(nil) (Fn unset) = (%v, %v); want (empty non-nil slice, nil)", res, err)
	}
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
