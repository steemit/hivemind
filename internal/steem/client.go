package steem

import (
	"context"
	"encoding/json"
	"fmt"

	"go.uber.org/zap"

	sdkapi "github.com/steemit/steemgosdk/api"
	protocolapi "github.com/steemit/steemutil/protocol/api"
	"github.com/steemit/steemutil/protocol"

	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// Client wraps the Steem RPC client using steemgosdk and steemutil
type Client struct {
	api    *sdkapi.API
	logger *zap.Logger
}

// New creates a new Steem client
func New(cfg *config.SteemConfig) (*Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("steemd_url is required")
	}

	logger := logging.GetLogger().With(zap.String("component", "steem-client"))

	api := sdkapi.NewAPI(cfg.URL)
	if cfg.MaxWorkers > 0 {
		// Note: steemgosdk doesn't have a direct maxWorkers setting,
		// but we can set max retry which affects reliability
		api.SetMaxRetry(cfg.MaxWorkers)
	}

	client := &Client{
		api:    api,
		logger: logger,
	}

	logger.Info("Steem client initialized", zap.String("url", cfg.URL))

	return client, nil
}

// GetBlock fetches a single block by number
func (c *Client) GetBlock(ctx context.Context, num int64) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_block")
	defer span.End()

	block, err := c.api.GetBlock(uint(num))
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", num, err)
	}

	return c.blockToMap(block, num), nil
}

// GetBlocksRange fetches multiple blocks in a range
func (c *Client) GetBlocksRange(ctx context.Context, from, to int64) ([]map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_blocks_range")
	defer span.End()

	if to < from {
		return nil, fmt.Errorf("invalid range: to (%d) < from (%d)", to, from)
	}

	// steemgosdk's GetBlocks uses [from, to) (exclusive end)
	// We need [from, to] (inclusive end), so we use to+1
	wrapBlocks, err := c.api.GetBlocks(uint(from), uint(to+1))
	if err != nil {
		return nil, fmt.Errorf("failed to get blocks range %d-%d: %w", from, to, err)
	}

	// Convert WrapBlock to map
	blocks := make([]map[string]interface{}, 0, len(wrapBlocks))
	for _, wrapBlock := range wrapBlocks {
		blockMap := c.blockToMap(wrapBlock.Block, int64(wrapBlock.BlockNum))
		blocks = append(blocks, blockMap)
	}

	return blocks, nil
}

// GetAccounts fetches account data from steemd
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
	err := c.api.CallWithResult("condenser_api", "get_accounts", []interface{}{names}, &accounts)
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts: %w", err)
	}

	return accounts, nil
}

// GetContent fetches post/comment data
func (c *Client) GetContent(ctx context.Context, author, permlink string) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_content")
	defer span.End()

	var content map[string]interface{}
	err := c.api.CallWithResult("condenser_api", "get_content", []interface{}{author, permlink}, &content)
	if err != nil {
		return nil, fmt.Errorf("failed to get content: %w", err)
	}

	return content, nil
}

// GetContentBatch fetches multiple posts/comments
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
	err := c.api.CallWithResult("condenser_api", "get_content", params, &contents)
	if err != nil {
		return nil, fmt.Errorf("failed to get content batch: %w", err)
	}

	return contents, nil
}

// GetDynamicGlobalProperties fetches dynamic global properties
func (c *Client) GetDynamicGlobalProperties(ctx context.Context) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_dynamic_global_properties")
	defer span.End()

	dgp, err := c.api.GetDynamicGlobalProperties()
	if err != nil {
		return nil, fmt.Errorf("failed to get dynamic global properties: %w", err)
	}

	// Convert to map for compatibility
	return c.dgpToMap(dgp), nil
}

// HeadBlock returns the current head block number
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

// LastIrreversible returns the last irreversible block number
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

// blockToMap converts protocolapi.Block to map[string]interface{} for compatibility
func (c *Client) blockToMap(block *protocolapi.Block, blockNum int64) map[string]interface{} {
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
			if err == nil {
				json.Unmarshal(jsonData, &opDataMap)
			} else {
				// Fallback: create empty map
				opDataMap = make(map[string]interface{})
			}
			
			// Operations are stored as [type, data] tuples
			// Convert OpType to format expected by block processor (e.g., "pow" -> "pow_operation")
			opTypeStr := c.opTypeToString(op.Type())
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

	return blockMap
}

// dgpToMap converts DynamicGlobalProperties to map[string]interface{} for compatibility
func (c *Client) dgpToMap(dgp *protocolapi.DynamicGlobalProperties) map[string]interface{} {
	// Marshal to JSON and unmarshal to map for automatic conversion
	jsonData, err := json.Marshal(dgp)
	if err != nil {
		c.logger.Warn("Failed to marshal DGP", zap.Error(err))
		return make(map[string]interface{})
	}

	var result map[string]interface{}
	if err := json.Unmarshal(jsonData, &result); err != nil {
		c.logger.Warn("Failed to unmarshal DGP", zap.Error(err))
		return make(map[string]interface{})
	}

	return result
}

// opTypeToString converts OpType to the format expected by block processor
// e.g., "pow" -> "pow_operation", "comment" -> "comment_operation"
func (c *Client) opTypeToString(opType protocol.OpType) string {
	opTypeStr := string(opType)
	
	// Map of special cases
	specialCases := map[string]string{
		"pow":                         "pow_operation",
		"pow2":                        "pow2_operation",
		"comment":                     "comment_operation",
		"delete_comment":              "delete_comment_operation",
		"vote":                        "vote_operation",
		"transfer":                    "transfer_operation",
		"account_create":              "account_create_operation",
		"account_create_with_delegation": "account_create_with_delegation_operation",
		"create_claimed_account":      "create_claimed_account_operation",
		"account_update":              "account_update_operation",
		"account_update2":             "account_update2_operation",
		"custom_json":                "custom_json_operation",
	}
	
	// Check if there's a special case mapping
	if mapped, ok := specialCases[opTypeStr]; ok {
		return mapped
	}
	
	// Default: append "_operation" suffix
	return opTypeStr + "_operation"
}
