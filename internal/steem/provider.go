package steem

import "context"

// Provider is the abstraction over the Steem RPC surface that the indexer and
// API layers depend on. The concrete *Client implements it; tests inject a
// mock to avoid hitting a live steemd node.
//
// Defining this interface (rather than depending on the concrete *Client) is
// the KR5 prerequisite: it lets CachedPost (KR2), AccountIndexer.Flush (KR3),
// and the condenser get_transaction handler (KR6) be unit-tested, and keeps
// the dependency direction one-way (indexer/api -> interface <- steem impl).
//
// # Error contract (fake ↔ real alignment)
//
// The real *Client (steemgosdk api/v2 + wrapper-level JSON-RPC batch, PR#400/
// PR#401) obeys the following contract; test doubles used by indexer/API
// tests must mirror it so that behavior verified against fakes matches
// production:
//
//   - On failure paths, a malformed/bad JSON response, a null result where
//     steemd never legitimately returns null (get_accounts, get_content),
//     a zero-value dynamic-global-properties payload, a response whose
//     length does not match the request (get_accounts, get_content batch),
//     or an exhausted bounded retry are ALL errors — a failed call never
//     returns a nil/empty result alongside a nil error. Explicitly empty
//     requests are not failures: GetContentBatch with zero posts returns
//     an empty slice and a nil error.
//   - GetContentBatch uses real JSON-RPC batch semantics (PR#401): a POST
//     body of request objects (chunked by max_batch, fetched on a
//     max_workers-bounded pool), results always in request order with
//     exactly one entry per request, per-item response id alignment, and
//     ALL-OR-NOTHING failure — a single batch item carrying a JSON-RPC
//     error, a null result, or a missing author field fails the whole call
//     (mirroring legacy http_client._validated_batch_result). Callers must
//     treat any GetContentBatch error as "no results", never index into a
//     partial slice.
//   - GetBlock for a block beyond the chain head fails with an error wrapping
//     ErrBlockNotFound (see client.go), not an empty block.
//   - GetBlocksRange is all-or-nothing: any per-block failure after retries
//     fails the whole range so the caller can never skip a block.
//   - ctx cancellation aborts in-flight RPCs and surfaces as an error
//     (context.Canceled / context.DeadlineExceeded).
//
// Only the methods actually consumed by callers are listed. Add methods here
// when a new consumer needs them, so the interface stays intentionally small.
type Provider interface {
	// GetBlock fetches a single block by number.
	GetBlock(ctx context.Context, num int64) (map[string]interface{}, error)

	// GetBlocksRange fetches blocks [from, to] inclusive.
	GetBlocksRange(ctx context.Context, from, to int64) ([]map[string]interface{}, error)

	// GetAccounts fetches up to 1000 accounts by name (steemd get_accounts).
	GetAccounts(ctx context.Context, names []string) ([]map[string]interface{}, error)

	// GetContent fetches a single post by author/permlink (steemd get_content).
	GetContent(ctx context.Context, author, permlink string) (map[string]interface{}, error)

	// GetContentBatch fetches multiple posts (steemd get_content per item).
	GetContentBatch(ctx context.Context, posts [][]string) ([]map[string]interface{}, error)

	// GetDynamicGlobalProperties returns the chain's dynamic global properties.
	GetDynamicGlobalProperties(ctx context.Context) (map[string]interface{}, error)

	// HeadBlock returns the current head block number.
	HeadBlock(ctx context.Context) (int64, error)

	// LastIrreversible returns the last irreversible block number.
	LastIrreversible(ctx context.Context) (int64, error)
}

// Compile-time assertion that *Client satisfies Provider. If a method is added
// to the interface without a matching implementation, this fails the build.
var _ Provider = (*Client)(nil)
