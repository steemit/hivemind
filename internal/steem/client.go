package steem

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.uber.org/zap"

	v2api "github.com/steemit/steemgosdk/api/v2"
	"github.com/steemit/steemutil/protocol"
	protocolapi "github.com/steemit/steemutil/protocol/api"

	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// ErrBlockNotFound is re-exported from steemgosdk api/v2 so callers can
// distinguish "block not produced yet" (e.g. steemd node lagging behind the
// expected head) from transport/infrastructure failures via errors.Is,
// without importing the SDK directly.
var ErrBlockNotFound = v2api.ErrBlockNotFound

// Client wraps the Steem RPC client using steemgosdk's context-aware api/v2.
//
// Hardening contract (PR#400, P1-29):
//   - Every method passes its context through to the SDK, so caller
//     cancellation aborts in-flight RPCs immediately.
//   - Retry with exponential backoff and an upper bound lives in the SDK
//     transport layer (steemgosdk v0.0.31: 100/200/400ms, doubled on 429,
//     capped by WithMaxRetry). The wrapper deliberately adds no outer retry
//     loop — stacking retries would multiply attempts (see the SDK's
//     MIGRATION.md). Persistent failure surfaces as an error and is handled
//     by the caller's sync loop.
//   - Malformed responses are errors, never empty results: a bad JSON body
//     fails json decoding inside the SDK and propagates; a null result for
//     get_block maps to ErrBlockNotFound; null/empty results for calls that
//     steemd never legitimately answers that way are rejected here
//     (get_accounts/get_content null, zero-value DGP, response/request
//     length mismatches). The indexer must never advance head on the back
//     of an empty/garbage response.
//
// Batch contract (PR#401, P0-3): GetContentBatch does NOT go through the SDK
// — steemgosdk v0.0.31 has no JSON-RPC batch capability (its jsonrpc2 layer
// builds and decodes exactly one request/response object), so the batch path
// POSTs a raw request-object array with this client's own http.Client. That
// also means the SDK transport retry does not apply there; the batch path
// carries its own bounded retry with the same backoff shape (base 100ms
// << attempt, doubled on 429), making it the single retry layer for batches —
// no stacking with the SDK.
type Client struct {
	api    *v2api.API
	logger *zap.Logger

	// url and httpClient serve the JSON-RPC batch path (GetContentBatch),
	// which bypasses the SDK; knobs mirror the SDK transport so both paths
	// share the same retry/timeout semantics.
	url        string
	httpClient *http.Client
	maxBatch   int
	maxWorkers int
	maxRetries int
	rpcTimeout time.Duration
}

// Defaults for the batch path when the config leaves a knob unset (tests
// construct configs with zero values). They mirror the SDK transport
// defaults and the legacy hivemind client (max_batch=50, max_workers=4,
// max_retries=3, 15s per attempt).
const (
	batchDefaultMaxBatch   = 50
	batchDefaultMaxWorkers = 4
	batchDefaultMaxRetries = 3
	batchDefaultTimeout    = 15 * time.Second
	batchBaseBackoff       = 100 * time.Millisecond

	// batchResponseItemLimit bounds one get_content result object on the
	// batch path (1 MiB). Real results are post metadata plus the
	// active_votes array; even heavily-voted whale posts stay well below
	// this, so the cap never clips a legitimate answer. Bodies beyond
	// max_batch × this limit (+ slack) are rejected as malformed.
	batchResponseItemLimit int64 = 1 << 20
	// batchResponseEnvelopeSlack covers the JSON-RPC envelopes (jsonrpc/id
	// keys) around the capped results.
	batchResponseEnvelopeSlack int64 = 64 * 1024
	// batchErrorDrainLimit bounds how much of a non-200 body is drained for
	// connection reuse after the first 1 KiB kept for the error message.
	batchErrorDrainLimit int64 = 64 * 1024
)

// New creates a new Steem client.
func New(cfg *config.SteemConfig) (*Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("steemd_url is required")
	}

	logger := logging.GetLogger().With(zap.String("component", "steem-client"))

	// MaxWorkers bounds the range-fetch worker pool (GetBlocksRange) and the
	// batch chunk pool (GetContentBatch); MaxRetries bounds transport-level
	// retries; RPCTimeout bounds each HTTP attempt. Note: pre-v0.0.31
	// MaxWorkers was (mis)wired into the SDK's retry knob; with api/v2 it
	// maps to actual fetch concurrency.
	api := v2api.NewAPI(cfg.URL,
		v2api.WithMaxRetry(cfg.MaxRetries),
		v2api.WithTimeout(cfg.RPCTimeout),
		v2api.WithConcurrency(cfg.MaxWorkers),
	)

	// Batch-path knobs: sanitize the same way the SDK does (n <= 0 selects
	// the default; retries cannot be fully disabled) and build a pooled
	// http.Client mirroring the SDK's NewAPI transport.
	maxBatch := cfg.MaxBatch
	if maxBatch <= 0 {
		maxBatch = batchDefaultMaxBatch
	}
	maxWorkers := cfg.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = batchDefaultMaxWorkers
	}
	maxRetries := cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = batchDefaultMaxRetries
	}
	rpcTimeout := cfg.RPCTimeout
	if rpcTimeout <= 0 {
		rpcTimeout = batchDefaultTimeout
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = maxWorkers
	if tr.MaxIdleConns < 100 {
		tr.MaxIdleConns = 100
	}

	client := &Client{
		api:        api,
		logger:     logger,
		url:        cfg.URL,
		httpClient: &http.Client{Timeout: rpcTimeout, Transport: tr},
		maxBatch:   maxBatch,
		maxWorkers: maxWorkers,
		maxRetries: maxRetries,
		rpcTimeout: rpcTimeout,
	}

	logger.Info("Steem client initialized",
		zap.String("url", cfg.URL),
		zap.Int("max_retries", maxRetries),
		zap.Duration("rpc_timeout", rpcTimeout),
		zap.Int("concurrency", maxWorkers),
		zap.Int("max_batch", maxBatch),
	)

	return client, nil
}

// GetBlock fetches a single block by number.
//
// A block number beyond the chain head returns an error wrapping
// ErrBlockNotFound (steemd answers `result: null`); it is never converted
// into an empty block map.
func (c *Client) GetBlock(ctx context.Context, num int64) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_block")
	defer span.End()

	if num < 1 {
		return nil, fmt.Errorf("invalid block number %d: must be >= 1", num)
	}

	block, err := c.api.GetBlock(ctx, uint(num))
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", num, err)
	}

	return c.blockToMap(block, num)
}

// GetBlocksRange fetches blocks [from, to] inclusive.
//
// All-or-nothing: if any block in the range fails after the SDK's bounded
// retries, the whole call returns an error and no blocks — the caller must
// retry the range. Returning partial results would let the indexer skip
// blocks (steem block numbers are contiguous), which is exactly the
// "empty/garbage response advances head" hazard this wrapper prevents.
func (c *Client) GetBlocksRange(ctx context.Context, from, to int64) ([]map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_blocks_range")
	defer span.End()

	if from < 1 || to < from {
		return nil, fmt.Errorf("invalid range: from (%d) must be >= 1 and <= to (%d)", from, to)
	}

	// steemgosdk's GetBlocks uses [from, to) (exclusive end)
	// We need [from, to] (inclusive end), so we use to+1
	wrapBlocks, err := c.api.GetBlocks(ctx, uint(from), uint(to+1))
	if err != nil {
		return nil, fmt.Errorf("failed to get blocks range %d-%d: %w", from, to, err)
	}

	blocks := make([]map[string]interface{}, 0, len(wrapBlocks))
	for _, wrapBlock := range wrapBlocks {
		blockMap, err := c.blockToMap(wrapBlock.Block, int64(wrapBlock.BlockNum))
		if err != nil {
			return nil, fmt.Errorf("failed to convert block %d: %w", wrapBlock.BlockNum, err)
		}
		blocks = append(blocks, blockMap)
	}

	return blocks, nil
}

// GetAccounts fetches account data from steemd.
func (c *Client) GetAccounts(ctx context.Context, names []string) ([]map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_accounts")
	defer span.End()

	if len(names) == 0 {
		return nil, fmt.Errorf("no account names provided")
	}
	if len(names) > 1000 {
		return nil, fmt.Errorf("too many accounts: %d (max: 1000)", len(names))
	}

	var accounts []map[string]interface{}
	err := c.api.CallWithResult(ctx, "condenser_api", "get_accounts", []interface{}{names}, &accounts)
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts: %w", err)
	}
	// steemd answers get_accounts with an array (never null); a null result
	// means a malformed response and must not become an empty success.
	if accounts == nil {
		return nil, fmt.Errorf("failed to get accounts: unexpected null result for %d names", len(names))
	}
	// Mirror legacy client.py: steemd answers get_accounts with exactly one
	// entry per requested name, in order; a length mismatch is malformed.
	if len(accounts) != len(names) {
		return nil, fmt.Errorf("malformed accounts response: requested %d names, got %d results", len(names), len(accounts))
	}

	return accounts, nil
}

// GetContent fetches post/comment data.
func (c *Client) GetContent(ctx context.Context, author, permlink string) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_content")
	defer span.End()

	var content map[string]interface{}
	err := c.api.CallWithResult(ctx, "condenser_api", "get_content", []interface{}{author, permlink}, &content)
	if err != nil {
		return nil, fmt.Errorf("failed to get content: %w", err)
	}
	// steemd answers get_content with an object (never null).
	if content == nil {
		return nil, fmt.Errorf("failed to get content @%s/%s: unexpected null result", author, permlink)
	}

	return content, nil
}

// GetContentBatch fetches multiple posts/comments.
//
// Real JSON-RPC batch semantics (PR#401, P0-3), mirroring legacy
// hive/steem/http_client.py (__exec_batch + _validated_batch_result):
//
//   - The wire shape is a POST body of a JSON-RPC request-object array —
//     [{"jsonrpc":"2.0","id":N,"method":"condenser_api.get_content",
//     "params":["author","permlink"]}, ...] — with per-chunk 1-based ids.
//     steemd rejects the pre-#401 shape (a single request whose params are
//     an array of [author, permlink] pairs) with a JSON-RPC error; that
//     quirk is pinned in client_contract_test.go.
//   - Requests are chunked by max_batch (HIVE_MAX_BATCH, default 50) and
//     the chunks are fetched concurrently on a max_workers-bounded pool
//     (HIVE_MAX_WORKERS) — the same concurrency model as GetBlocksRange.
//     Results are reassembled in request order regardless of chunk
//     completion order, so callers may keep indexing by request position.
//   - Response validation is fail-loud and all-or-nothing: the response
//     array length must match the request count, each item's id must equal
//     the paired request's id (order-based, as steemd/legacy return in
//     order), any single item carrying a JSON-RPC error fails the WHOLE
//     call, and a null/missing per-item result or a result without an
//     "author" field (legacy `assert 'author' in post`) is rejected as
//     malformed. Legacy nuance: exec() retries an item RPCError in place up
//     to 100 tries (rotating nodes) before aborting the whole batch; this
//     implementation does not retry protocol-level failures at all — a
//     deterministic error/malformed response from the same node will not
//     heal — so the terminal state is identical: the whole batch errors and
//     the caller (sync/jobs layer) decides what to retry.
//
// Length guard (PR#400 review, kept): callers index the response by the
// request's position (cached_post.go updateBatchTuples, jobs.go
// audit_cache_undelete), so the returned slice always has exactly one entry
// per requested post or the call errors. An explicitly empty request is not
// a failure: it returns an empty slice and a nil error without any wire
// traffic.
func (c *Client) GetContentBatch(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_content_batch")
	defer span.End()

	if len(posts) == 0 {
		return []map[string]interface{}{}, nil
	}

	// Validate tuples before any wire traffic. Malformed tuples are rejected
	// up front instead of silently dropped: dropping would desynchronize the
	// response from the caller's request slice.
	for i, post := range posts {
		if len(post) < 2 {
			return nil, fmt.Errorf("invalid post tuple at index %d: want [author, permlink], got %d element(s)", i, len(post))
		}
	}

	// Chunk by max_batch (legacy chunkify).
	chunks := make([][][]string, 0, (len(posts)+c.maxBatch-1)/c.maxBatch)
	for start := 0; start < len(posts); start += c.maxBatch {
		end := start + c.maxBatch
		if end > len(posts) {
			end = len(posts)
		}
		chunks = append(chunks, posts[start:end])
	}

	// Fan the chunks out to a bounded worker pool (legacy exec_multi uses a
	// ThreadPoolExecutor with map(), which preserves order; the order here is
	// restored by assembling per-chunk results by chunk index).
	results := make([][]map[string]interface{}, len(chunks))
	errs := make([]error, len(chunks))

	workers := c.maxWorkers
	if workers > len(chunks) {
		workers = len(chunks)
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if err := ctx.Err(); err != nil {
					// Canceled while queued: fail fast without a request.
					errs[idx] = err
					continue
				}
				contents, err := c.fetchContentChunk(ctx, chunks[idx])
				if err != nil {
					errs[idx] = err
					continue
				}
				results[idx] = contents
			}
		}()
	}
	// Dispatch strictly in order on an unbuffered channel; on ctx
	// cancellation stop dispatching and (after the workers drained) mark the
	// never-sent tail with the context error, so every chunk is accounted
	// for. (Do not buffer jobs: in-flight workers must not race the sender
	// over tail ownership — same reasoning as the SDK's runRange.)
	sending := true
	unsentFrom := len(chunks)
	for idx := 0; idx < len(chunks) && sending; idx++ {
		select {
		case jobs <- idx:
		case <-ctx.Done():
			unsentFrom = idx
			sending = false
		}
	}
	close(jobs)
	wg.Wait()
	for i := unsentFrom; i < len(chunks); i++ {
		errs[i] = ctx.Err()
	}

	// All-or-nothing: report the lowest-index chunk failure deterministically.
	for i, err := range errs {
		if err != nil {
			start := i*c.maxBatch + 1
			return nil, fmt.Errorf("failed to get content batch (chunk %d/%d, posts %d-%d): %w",
				i+1, len(chunks), start, i*c.maxBatch+len(chunks[i]), err)
		}
	}

	out := make([]map[string]interface{}, 0, len(posts))
	for _, res := range results {
		out = append(out, res...)
	}
	return out, nil
}

// batchRequest is one JSON-RPC request object inside a batch POST body.
type batchRequest struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      int64     `json:"id"`
	Method  string    `json:"method"`
	Params  [2]string `json:"params"`
}

// batchResponse is one JSON-RPC response object inside a batch response body.
// ID decodes strictly as a number: steemd/legacy echo the integer ids we
// send; anything else is a malformed response and fails loud at decode time.
type batchResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *batchRPCError  `json:"error"`
}

// batchRPCError is a JSON-RPC error object carried by one batch item.
type batchRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *batchRPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// batchHTTPError is a non-200 transport response on the batch path.
type batchHTTPError struct {
	Status int
	Body   string
}

func (e *batchHTTPError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Status, e.Body)
}

// errBatchTimeout is the batch path's sentinel for a per-attempt transport
// timeout (http.Client.Timeout expiry) as opposed to a CALLER-side context
// deadline. Since Go 1.23, http.Client.Timeout errors satisfy
// errors.Is(err, context.DeadlineExceeded), so the two can no longer be told
// apart from the error chain alone: postBatch classifies the Do failure
// while it still holds the request context (a live ctx means the expiry was
// the client's own per-attempt timeout) and wraps it in this sentinel with
// the underlying chain cut — exactly how steemgosdk api/v2's callOnce wraps
// its ErrTimeout before its retryable check. isBatchRetryable treats this
// sentinel as transient, so a slow steemd node gets the same bounded
// retry-and-backoff the SDK path gives single calls.
var errBatchTimeout = errors.New("steem batch: per-attempt timeout")

// isHTTPTimeout reports whether err is a per-attempt transport timeout
// (net.Error / url.Error with Timeout() true, e.g. http.Client.Timeout);
// mirrors the SDK's asTimeoutError.
func isHTTPTimeout(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return true
	}
	return false
}

// fetchContentChunk POSTs one JSON-RPC batch array for a chunk of posts and
// returns its contents in request order. Transport failures — network
// errors, per-attempt timeouts, HTTP 429/5xx — are retried with bounded
// exponential backoff (doubled on 429) — the ONLY retry layer for the batch
// path, since it bypasses the SDK transport (whose retry it mirrors; see the
// Client doc comment). Caller-side context cancellation and protocol-level
// failures (bad JSON, trailing data, id/length mismatch, per-item JSON-RPC
// errors, null results) are not retried: a deterministic malformed response
// will not heal, and a caller who gave up must not be kept waiting.
func (c *Client) fetchContentChunk(ctx context.Context, chunk [][]string) ([]map[string]interface{}, error) {
	reqs := make([]batchRequest, len(chunk))
	for i, tuple := range chunk {
		reqs[i] = batchRequest{
			JSONRPC: "2.0",
			ID:      int64(i + 1), // legacy: per-chunk 1-based request ids
			Method:  "condenser_api.get_content",
			Params:  [2]string{tuple[0], tuple[1]},
		}
	}
	payload, err := json.Marshal(reqs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal get_content batch: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resps, err := c.postBatch(ctx, payload)
		if err == nil {
			return validateBatchResponses(reqs, resps)
		}
		if !isBatchRetryable(err) {
			return nil, err
		}
		lastErr = err
		if attempt == c.maxRetries {
			break
		}
		backoff := batchBackoffFor(attempt, err)
		// Ops visibility for retry storms (e.g. sustained 429 from steemd):
		// without this, batches silently grind through backoffs.
		c.logger.Warn("get_content batch attempt failed; retrying",
			zap.Int("attempt", attempt+1),
			zap.Int("max_attempts", c.maxRetries+1),
			zap.Int("chunk_posts", len(chunk)),
			zap.Duration("backoff", backoff),
			zap.Error(err),
		)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("get_content batch of %d posts failed after %d attempts: %w", len(chunk), c.maxRetries+1, lastErr)
}

// postBatch performs one transport attempt: POST the raw request-object
// array and decode the response-object array.
func (c *Client) postBatch(ctx context.Context, payload []byte) ([]batchResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to build get_content batch request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		// Classify the failure while the request context is still at hand.
		// A live ctx plus a timeout-shaped error means http.Client.Timeout
		// expired on THIS attempt (slow node, transient): wrap it in the
		// errBatchTimeout sentinel with the underlying chain deliberately
		// cut (%v) — on Go 1.23+ the raw chain also satisfies errors.Is(err,
		// context.DeadlineExceeded), which would make isBatchRetryable
		// misclassify the per-attempt timeout as a caller deadline and kill
		// the whole batch after one slow response. Caller-side cancellation
		// (ctx already done) falls through unchanged and stays
		// non-retryable, as do all other *url.Error failures, which
		// isBatchRetryable classifies.
		if ctx.Err() == nil && isHTTPTimeout(err) {
			return nil, fmt.Errorf("%w: %v", errBatchTimeout, err)
		}
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
		// Drain the remainder (bounded) so the keep-alive connection returns
		// to the pool for the next attempt instead of being torn down.
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, batchErrorDrainLimit))
		return nil, &batchHTTPError{Status: res.StatusCode, Body: string(body)}
	}

	// Response size cap: max_batch items × a generous per-item bound plus
	// envelope slack. A real get_content result (post metadata plus
	// active_votes) stays far below 1 MiB per item, so the cap never clips a
	// legitimate answer, while a compromised node or MITM cannot use the
	// batch path to balloon process memory. Reading cap+1 bytes makes any
	// body beyond the cap surface below as trailing data (or above, as a
	// decode error on the truncated array).
	cap := int64(c.maxBatch)*batchResponseItemLimit + batchResponseEnvelopeSlack
	dec := json.NewDecoder(io.LimitReader(res.Body, cap+1))
	var resps []batchResponse
	if err := dec.Decode(&resps); err != nil {
		return nil, fmt.Errorf("malformed get_content batch response: %w", err)
	}
	// Legacy parses the whole body (json.loads), rejecting trailing data.
	// json.Decoder stops after the first value, so demand EOF on a second
	// read: any byte beyond the array — including a stray ']' that
	// Decoder.More would miss — fails loud here as malformed.
	if err := dec.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, fmt.Errorf("malformed get_content batch response: trailing data after response array")
	}
	return resps, nil
}

// validateBatchResponses enforces the legacy batch contract on one decoded
// chunk response: length alignment, pairwise (order-based) id alignment, no
// per-item JSON-RPC error (one failing item fails the whole call), no
// null/missing result, and an "author" field on every post (legacy
// `assert 'author' in post`).
func validateBatchResponses(reqs []batchRequest, resps []batchResponse) ([]map[string]interface{}, error) {
	// A `null` body decodes to a nil slice without error; treat it as the
	// length mismatch it is.
	if len(resps) != len(reqs) {
		return nil, fmt.Errorf("malformed content batch response: got %d results for %d requested posts", len(resps), len(reqs))
	}

	contents := make([]map[string]interface{}, len(reqs))
	for i, resp := range resps {
		author, permlink := reqs[i].Params[0], reqs[i].Params[1]
		if resp.ID != reqs[i].ID {
			return nil, fmt.Errorf("malformed content batch response: item %d id mismatch (request %d, response %d)", i, reqs[i].ID, resp.ID)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("get_content batch item %d (@%s/%s) failed: %w", i, author, permlink, resp.Error)
		}
		var content map[string]interface{}
		if len(resp.Result) == 0 {
			return nil, fmt.Errorf("get_content batch item %d (@%s/%s): missing result", i, author, permlink)
		}
		if err := json.Unmarshal(resp.Result, &content); err != nil {
			return nil, fmt.Errorf("malformed get_content batch response item %d (@%s/%s): %w", i, author, permlink, err)
		}
		// `"result":null` unmarshals into a nil map WITHOUT an error; steemd
		// never legitimately answers get_content with null.
		if content == nil {
			return nil, fmt.Errorf("get_content batch item %d (@%s/%s): null result", i, author, permlink)
		}
		if _, ok := content["author"]; !ok {
			return nil, fmt.Errorf("get_content batch item %d (@%s/%s): invalid post: no author field", i, author, permlink)
		}
		contents[i] = content
	}
	return contents, nil
}

// isBatchRetryable mirrors the SDK transport's error classification for the
// batch path: transient failures (network errors, per-attempt timeouts,
// HTTP 429/5xx) are retried; caller-side context errors and protocol-level
// failures (malformed bodies, per-item JSON-RPC errors) are not.
func isBatchRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Per-attempt timeouts first: postBatch wraps them in errBatchTimeout
	// with the underlying chain cut, because on Go 1.23+ that chain also
	// satisfies errors.Is(err, context.DeadlineExceeded) and the caller-
	// context check right below would otherwise misclassify them as
	// non-retryable (the SDK reaches the same outcome by checking its
	// ErrTimeout sentinel before its context check).
	if errors.Is(err, errBatchTimeout) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr *batchHTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status == http.StatusTooManyRequests || httpErr.Status >= 500
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// client.Do failures (DNS, connection refused, TLS, resets) surface
		// as *url.Error; treat as transient network trouble.
		return true
	}
	return false
}

// batchBackoffFor returns the sleep before the next batch attempt: base <<
// attempt, doubled when the failure was rate limiting — the same shape as
// the SDK transport's backoffFor.
func batchBackoffFor(attempt int, err error) time.Duration {
	d := batchBaseBackoff << attempt
	var httpErr *batchHTTPError
	if errors.As(err, &httpErr) && httpErr.Status == http.StatusTooManyRequests {
		d *= 2
	}
	return d
}

// GetDynamicGlobalProperties fetches dynamic global properties.
//
// The SDK decodes `result:null` and `result:{}` into a zero-value struct
// WITHOUT an error (json.Unmarshal of "null" is a no-op). A zero-value DGP
// would make HeadBlock()/LastIrreversible() report 0 with err==nil, and the
// initial-sync check (sync.go) would then conclude the chain is empty and
// skip syncing. Mirroring legacy client.py's `assert 'time' in ret`, such
// responses are rejected as malformed here.
func (c *Client) GetDynamicGlobalProperties(ctx context.Context) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_dynamic_global_properties")
	defer span.End()

	dgp, err := c.api.GetDynamicGlobalProperties(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dynamic global properties: %w", err)
	}
	// Zero-value hazard: an empty time, or head and LIB both 0, only occurs
	// for null/empty/garbage responses — real chain state always has them.
	if dgp.Time == "" || (dgp.HeadBlockNumber == 0 && dgp.LastIrreversibleBlockNum == 0) {
		return nil, fmt.Errorf(
			"malformed dynamic global properties response: time=%q head_block_number=%d last_irreversible_block_num=%d",
			dgp.Time, dgp.HeadBlockNumber, dgp.LastIrreversibleBlockNum)
	}

	// Convert to map for compatibility
	return dgpToMap(dgp)
}

// HeadBlock returns the current head block number.
func (c *Client) HeadBlock(ctx context.Context) (int64, error) {
	props, err := c.GetDynamicGlobalProperties(ctx)
	if err != nil {
		return 0, err
	}

	headBlockNum, ok := props["head_block_number"]
	if !ok {
		return 0, fmt.Errorf("head_block_number not found in properties")
	}

	// Handle different number types
	switch v := headBlockNum.(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("unexpected type for head_block_number: %T", v)
	}
}

// LastIrreversible returns the last irreversible block number.
func (c *Client) LastIrreversible(ctx context.Context) (int64, error) {
	props, err := c.GetDynamicGlobalProperties(ctx)
	if err != nil {
		return 0, err
	}

	lastIrreversible, ok := props["last_irreversible_block_num"]
	if !ok {
		return 0, fmt.Errorf("last_irreversible_block_num not found in properties")
	}

	// Handle different number types
	switch v := lastIrreversible.(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case uint32:
		return int64(v), nil
	default:
		return 0, fmt.Errorf("unexpected type for last_irreversible_block_num: %T", v)
	}
}

// StreamBlocks streams blocks starting from a given block number
// This will be implemented based on fork strategy evaluation
func (c *Client) StreamBlocks(ctx context.Context, startFrom int64, trailBlocks int, maxGap int) (<-chan map[string]interface{}, error) {
	// TODO: Implement block streaming
	// This depends on fork strategy decision
	return nil, fmt.Errorf("block streaming not yet implemented")
}

// blockToMap converts protocolapi.Block to map[string]interface{} for compatibility.
// Conversion failures are returned, never swallowed: a partially-converted
// block must not be written as if it were valid.
func (c *Client) blockToMap(block *protocolapi.Block, blockNum int64) (map[string]interface{}, error) {
	// Convert transactions to []interface{}
	transactions := make([]interface{}, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		// Convert transaction to map
		txMap := make(map[string]interface{})

		// Convert operations to []interface{} format: [[type, data], ...]
		ops := make([]interface{}, 0, len(tx.Operations))
		for _, op := range tx.Operations {
			// Convert operation data to map[string]interface{}
			opData := op.Data()
			var opDataMap map[string]interface{}

			// Marshal and unmarshal to convert struct to map
			jsonData, err := json.Marshal(opData)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal operation %s data in block %d: %w", op.Type(), blockNum, err)
			}
			if err := json.Unmarshal(jsonData, &opDataMap); err != nil {
				return nil, fmt.Errorf("failed to unmarshal operation %s data in block %d: %w", op.Type(), blockNum, err)
			}

			// Operations are stored as [type, data] tuples
			// Convert OpType to format expected by block processor (e.g., "pow" -> "pow_operation")
			opTypeStr := opTypeToString(op.Type())
			opTuple := []interface{}{
				opTypeStr,
				opDataMap,
			}
			ops = append(ops, opTuple)
		}

		txMap["operations"] = ops
		txMap["ref_block_num"] = uint16(tx.RefBlockNum)
		txMap["ref_block_prefix"] = uint32(tx.RefBlockPrefix)
		if tx.Expiration != nil && tx.Expiration.Time != nil {
			txMap["expiration"] = tx.Expiration.Time.Format("2006-01-02T15:04:05")
		}
		txMap["signatures"] = tx.Signatures
		txMap["extensions"] = tx.Extensions

		transactions = append(transactions, txMap)
	}

	// Build block map
	blockMap := make(map[string]interface{})
	blockMap["block_num"] = float64(blockNum)
	blockMap["block_id"] = block.BlockId
	blockMap["previous"] = block.Previous
	blockMap["witness"] = block.Witness
	blockMap["witness_signature"] = block.WitnessSignature
	blockMap["transaction_merkle_root"] = block.TransactionMerkleRoot
	blockMap["transactions"] = transactions
	blockMap["transaction_ids"] = block.TransactionIds

	if block.Timestamp != nil && block.Timestamp.Time != nil {
		blockMap["timestamp"] = block.Timestamp.Time.Format("2006-01-02T15:04:05")
	} else {
		blockMap["timestamp"] = ""
	}

	blockMap["extensions"] = block.Extensions
	blockMap["signing_key"] = block.SigningKey

	return blockMap, nil
}

// dgpToMap converts DynamicGlobalProperties to map[string]interface{} for
// compatibility. Unlike the pre-PR#400 version, conversion errors are
// returned rather than logged-and-swallowed: a zero-value properties map
// must not be mistaken for real chain state (e.g. head_block_number 0).
func dgpToMap(dgp *protocolapi.DynamicGlobalProperties) (map[string]interface{}, error) {
	// Marshal to JSON and unmarshal to map for automatic conversion
	jsonData, err := json.Marshal(dgp)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal dynamic global properties: %w", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(jsonData, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal dynamic global properties: %w", err)
	}

	return result, nil
}

// opTypeToString converts OpType to the format expected by block processor
// e.g., "pow" -> "pow_operation", "comment" -> "comment_operation"
func opTypeToString(opType protocol.OpType) string {
	opTypeStr := string(opType)

	// Map of special cases
	specialCases := map[string]string{
		"pow":                            "pow_operation",
		"pow2":                           "pow2_operation",
		"comment":                        "comment_operation",
		"delete_comment":                 "delete_comment_operation",
		"vote":                           "vote_operation",
		"transfer":                       "transfer_operation",
		"account_create":                 "account_create_operation",
		"account_create_with_delegation": "account_create_with_delegation_operation",
		"create_claimed_account":         "create_claimed_account_operation",
		"account_update":                 "account_update_operation",
		"account_update2":                "account_update2_operation",
		"custom_json":                    "custom_json_operation",
	}

	// Check if there's a special case mapping
	if mapped, ok := specialCases[opTypeStr]; ok {
		return mapped
	}

	// Default: append "_operation" suffix
	return opTypeStr + "_operation"
}
