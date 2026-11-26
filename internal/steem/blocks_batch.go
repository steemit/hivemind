package steem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	protocolapi "github.com/steemit/steemutil/protocol/api"
)

// Block-fetch half of the batch path (GetBlocksRange). The transport, the
// retry loop and the chunk pool are shared with GetContentBatch; this file
// only adds the get_block wire shape and its per-item validation.
//
// Wire shape (mirrors legacy hive/steem/http_client.py __exec_batch, which
// sends the same request-object array for get_block):
//
//	[{"jsonrpc":"2.0","id":1,"method":"condenser_api.get_block","params":[100]},
//	 {"jsonrpc":"2.0","id":2,"method":"condenser_api.get_block","params":[101]}, ...]
//
// with per-chunk 1-based ids, exactly the method and params the single-block
// path sends (steemgosdk GetBlock). block_api.get_block_range was not used:
// it serializes operations in a different shape (objects with type/value
// instead of [type, value] tuples), which would put every operation type's
// decoding in scope instead of only the transport.

// blockBatchRequest is one condenser_api.get_block request object inside a
// block-batch POST body.
type blockBatchRequest struct {
	JSONRPC string   `json:"jsonrpc"`
	ID      int64    `json:"id"`
	Method  string   `json:"method"`
	Params  [1]int64 `json:"params"`
}

// fetchBlocksChunk POSTs one JSON-RPC batch array for the inclusive block
// range [from, to] (at most max_batch blocks) and returns the converted
// blocks in request order. Transport failures are retried exactly like
// GetContentBatch (postBatchWithRetry); protocol-level failures are not.
func (c *Client) fetchBlocksChunk(ctx context.Context, from, to int64) ([]map[string]interface{}, error) {
	reqs := make([]blockBatchRequest, to-from+1)
	for i := range reqs {
		reqs[i] = blockBatchRequest{
			JSONRPC: "2.0",
			ID:      int64(i + 1), // legacy: per-chunk 1-based request ids
			Method:  "condenser_api.get_block",
			Params:  [1]int64{from + int64(i)},
		}
	}
	payload, err := json.Marshal(reqs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal get_block batch: %w", err)
	}

	resps, err := c.postBatchWithRetry(ctx, payload, batchChunk{
		what:      "get_block",
		noun:      "blocks",
		count:     len(reqs),
		sizeField: "chunk_blocks",
	})
	if err != nil {
		return nil, err
	}
	return c.decodeBlockBatch(reqs, resps)
}

// decodeBlockBatch enforces the batch contract on one decoded chunk
// response: length alignment, pairwise (order-based) id alignment, no
// per-item JSON-RPC error, and no null or undecodable result. One bad item
// fails the whole chunk, so the caller never gets a partial range.
func (c *Client) decodeBlockBatch(reqs []blockBatchRequest, resps []batchResponse) ([]map[string]interface{}, error) {
	// A `null` body decodes to a nil slice without error; treat it as the
	// length mismatch it is.
	if len(resps) != len(reqs) {
		return nil, fmt.Errorf("malformed get_block batch response: got %d results for %d requested blocks",
			len(resps), len(reqs))
	}

	blocks := make([]map[string]interface{}, len(reqs))
	for i, resp := range resps {
		num := reqs[i].Params[0]
		if resp.ID != reqs[i].ID {
			return nil, fmt.Errorf("malformed get_block batch response: item %d (block %d) id mismatch (request %d, response %d)",
				i, num, reqs[i].ID, resp.ID)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("get_block batch item %d (block %d) failed: %w", i, num, resp.Error)
		}
		if isJSONNull(resp.Result) {
			// steemd answers `result: null` for a block that does not exist
			// (yet) -- the same verdict the single-block path returns.
			return nil, fmt.Errorf("%w: condenser_api.get_block returned null for block %d", ErrBlockNotFound, num)
		}
		block := &protocolapi.Block{}
		if err := json.Unmarshal(resp.Result, block); err != nil {
			return nil, fmt.Errorf("malformed get_block batch response item %d (block %d): %w", i, num, err)
		}
		blockMap, err := c.blockToMap(block, num)
		if err != nil {
			return nil, fmt.Errorf("failed to convert block %d: %w", num, err)
		}
		blocks[i] = blockMap
	}
	return blocks, nil
}

// isJSONNull reports whether a raw JSON value is a missing or null result.
func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
