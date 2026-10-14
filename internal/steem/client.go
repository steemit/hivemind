package steem

import (
	"context"
	"encoding/json"
	"fmt"

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
//     get_block maps to ErrBlockNotFound; null results for calls that steemd
//     never legitimately answers with null are rejected here. The indexer
//     must never advance head on the back of an empty/garbage response.
type Client struct {
	api    *v2api.API
	logger *zap.Logger
}

// New creates a new Steem client.
func New(cfg *config.SteemConfig) (*Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("steemd_url is required")
	}

	logger := logging.GetLogger().With(zap.String("component", "steem-client"))

	// MaxWorkers bounds the range-fetch worker pool (GetBlocksRange);
	// MaxRetries bounds transport-level retries; RPCTimeout bounds each
	// HTTP attempt. Note: pre-v0.0.31 MaxWorkers was (mis)wired into the
	// SDK's retry knob; with api/v2 it maps to actual fetch concurrency.
	api := v2api.NewAPI(cfg.URL,
		v2api.WithMaxRetry(cfg.MaxRetries),
		v2api.WithTimeout(cfg.RPCTimeout),
		v2api.WithConcurrency(cfg.MaxWorkers),
	)

	client := &Client{
		api:    api,
		logger: logger,
	}

	logger.Info("Steem client initialized",
		zap.String("url", cfg.URL),
		zap.Int("max_retries", cfg.MaxRetries),
		zap.Duration("rpc_timeout", cfg.RPCTimeout),
		zap.Int("concurrency", cfg.MaxWorkers),
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
// NOTE (P0-3, to be fixed in PR#401): this still issues a single
// condenser_api.get_content call whose params are an array of
// [author, permlink] pairs, which real steemd rejects. Real batch semantics
// (batched request objects + per-item validation) ship with PR#401; this PR
// only carries the minimal adaptation to the v0.0.31 context-aware SDK.
func (c *Client) GetContentBatch(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_content_batch")
	defer span.End()

	// Build batch request
	params := make([]interface{}, 0, len(posts))
	for _, post := range posts {
		if len(post) >= 2 {
			params = append(params, []interface{}{post[0], post[1]})
		}
	}

	if len(params) == 0 {
		return []map[string]interface{}{}, nil
	}

	var contents []map[string]interface{}
	err := c.api.CallWithResult(ctx, "condenser_api", "get_content", params, &contents)
	if err != nil {
		return nil, fmt.Errorf("failed to get content batch: %w", err)
	}
	if contents == nil {
		return nil, fmt.Errorf("failed to get content batch: unexpected null result for %d posts", len(params))
	}

	return contents, nil
}

// GetDynamicGlobalProperties fetches dynamic global properties.
func (c *Client) GetDynamicGlobalProperties(ctx context.Context) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_dynamic_global_properties")
	defer span.End()

	dgp, err := c.api.GetDynamicGlobalProperties(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get dynamic global properties: %w", err)
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
