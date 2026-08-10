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
