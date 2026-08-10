package indexer

import (
	"context"
	"sync"

	"github.com/steemit/hivemind/internal/steem"
)

// fakeSteemProvider is a test double for steem.Provider used by indexer tests.
// Hand-written (no mockgen) to keep the test suite free of external toolchain
// dependency. Identical in shape to internal/steem/fake_provider_test.go.
type fakeSteemProvider struct {
	mu                         sync.Mutex
	getBlock                   func(ctx context.Context, num int64) (map[string]interface{}, error)
	getBlocksRange             func(ctx context.Context, from, to int64) ([]map[string]interface{}, error)
	getAccounts                func(ctx context.Context, names []string) ([]map[string]interface{}, error)
	getContent                 func(ctx context.Context, author, permlink string) (map[string]interface{}, error)
	getContentBatch            func(ctx context.Context, posts [][]string) ([]map[string]interface{}, error)
	getDynamicGlobalProperties func(ctx context.Context) (map[string]interface{}, error)
	headBlock                  func(ctx context.Context) (int64, error)
	lastIrreversible           func(ctx context.Context) (int64, error)

	nBlock       int
	nBlocksRange int
	nAccounts    int
	nContent     int
	nBatch       int
	nDGP         int
	nHead        int
	nLastIrr     int
}

var _ steem.Provider = (*fakeSteemProvider)(nil)

func (f *fakeSteemProvider) GetBlock(ctx context.Context, num int64) (map[string]interface{}, error) {
	f.mu.Lock()
	f.nBlock++
	fn := f.getBlock
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, num)
}

func (f *fakeSteemProvider) GetBlocksRange(ctx context.Context, from, to int64) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.nBlocksRange++
	fn := f.getBlocksRange
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, from, to)
}

func (f *fakeSteemProvider) GetAccounts(ctx context.Context, names []string) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.nAccounts++
	fn := f.getAccounts
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, names)
}

func (f *fakeSteemProvider) GetContent(ctx context.Context, author, permlink string) (map[string]interface{}, error) {
	f.mu.Lock()
	f.nContent++
	fn := f.getContent
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, author, permlink)
}

func (f *fakeSteemProvider) GetContentBatch(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.nBatch++
	fn := f.getContentBatch
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, posts)
}

func (f *fakeSteemProvider) GetDynamicGlobalProperties(ctx context.Context) (map[string]interface{}, error) {
	f.mu.Lock()
	f.nDGP++
	fn := f.getDynamicGlobalProperties
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx)
}

func (f *fakeSteemProvider) HeadBlock(ctx context.Context) (int64, error) {
	f.mu.Lock()
	f.nHead++
	fn := f.headBlock
	f.mu.Unlock()
	if fn == nil {
		return 0, nil
	}
	return fn(ctx)
}

func (f *fakeSteemProvider) LastIrreversible(ctx context.Context) (int64, error) {
	f.mu.Lock()
	f.nLastIrr++
	fn := f.lastIrreversible
	f.mu.Unlock()
	if fn == nil {
		return 0, nil
	}
	return fn(ctx)
}
