package steem

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"go.uber.org/zap"

	"github.com/steemit/hivemind/pkg/config"
	"github.com/steemit/hivemind/pkg/logging"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// Client wraps the Steem RPC client
type Client struct {
	rpc       *RPCClient
	maxBatch  int
	maxWorkers int
	logger    *zap.Logger
	mu        sync.Mutex
}

// New creates a new Steem client
func New(cfg *config.SteemConfig) (*Client, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("steemd_url is required")
	}

	logger := logging.GetLogger().With(zap.String("component", "steem-client"))

	rpcClient := NewRPCClient(cfg.URL, logger)

	client := &Client{
		rpc:        rpcClient,
		maxBatch:   cfg.MaxBatch,
		maxWorkers: cfg.MaxWorkers,
		logger:     logger,
	}

	logger.Info("Steem client initialized", zap.String("url", cfg.URL))

	return client, nil
}

// GetBlock fetches a single block by number
func (c *Client) GetBlock(ctx context.Context, num int64) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_block")
	defer span.End()

	result, err := c.rpc.Call(ctx, "block_api", "get_block", []interface{}{
		map[string]interface{}{
			"block_num": num,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get block %d: %w", num, err)
	}

	var response struct {
		Block map[string]interface{} `json:"block"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block response: %w", err)
	}

	return response.Block, nil
}

// GetBlocksRange fetches multiple blocks in a range
func (c *Client) GetBlocksRange(ctx context.Context, from, to int64) ([]map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_blocks_range")
	defer span.End()

	if to < from {
		return nil, fmt.Errorf("invalid range: to (%d) < from (%d)", to, from)
	}

	count := to - from + 1
	if count > int64(c.maxBatch) {
		return nil, fmt.Errorf("range too large: %d blocks (max: %d)", count, c.maxBatch)
	}

	// Build batch requests
	requests := make([]RPCRequest, 0, count)
	for i := from; i <= to; i++ {
		requests = append(requests, RPCRequest{
			JSONRPC: "2.0",
			ID:      int(i - from + 1),
			Method:  "block_api.get_block",
			Params: []interface{}{
				map[string]interface{}{
					"block_num": i,
				},
			},
		})
	}

	responses, err := c.rpc.CallBatch(ctx, requests)
	if err != nil {
		return nil, fmt.Errorf("failed to get blocks range %d-%d: %w", from, to, err)
	}

	// Parse responses
	blocks := make([]map[string]interface{}, 0, len(responses))
	for _, resp := range responses {
		if resp.Error != nil {
			return nil, fmt.Errorf("RPC error in batch: %s", resp.Error.Message)
		}

		var blockResp struct {
			Block map[string]interface{} `json:"block"`
		}
		if err := json.Unmarshal(resp.Result, &blockResp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal block: %w", err)
		}

		blocks = append(blocks, blockResp.Block)
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

	result, err := c.rpc.Call(ctx, "condenser_api", "get_accounts", []interface{}{names})
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts: %w", err)
	}

	var accounts []map[string]interface{}
	if err := json.Unmarshal(result, &accounts); err != nil {
		return nil, fmt.Errorf("failed to unmarshal accounts: %w", err)
	}

	return accounts, nil
}

// GetContent fetches post/comment data
func (c *Client) GetContent(ctx context.Context, author, permlink string) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_content")
	defer span.End()

	result, err := c.rpc.Call(ctx, "condenser_api", "get_content", []interface{}{author, permlink})
	if err != nil {
		return nil, fmt.Errorf("failed to get content: %w", err)
	}

	var content map[string]interface{}
	if err := json.Unmarshal(result, &content); err != nil {
		return nil, fmt.Errorf("failed to unmarshal content: %w", err)
	}

	return content, nil
}

// GetContentBatch fetches multiple posts/comments
func (c *Client) GetContentBatch(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_content_batch")
	defer span.End()

	// TODO: Implement batch fetching
	return nil, fmt.Errorf("steemgosdk integration not yet implemented")
}

// GetDynamicGlobalProperties fetches dynamic global properties
func (c *Client) GetDynamicGlobalProperties(ctx context.Context) (map[string]interface{}, error) {
	ctx, span := telemetry.StartSpan(ctx, "steem.get_dynamic_global_properties")
	defer span.End()

	result, err := c.rpc.Call(ctx, "database_api", "get_dynamic_global_properties", []interface{}{})
	if err != nil {
		return nil, fmt.Errorf("failed to get dynamic global properties: %w", err)
	}

	var props map[string]interface{}
	if err := json.Unmarshal(result, &props); err != nil {
		return nil, fmt.Errorf("failed to unmarshal properties: %w", err)
	}

	return props, nil
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

