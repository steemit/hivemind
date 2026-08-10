package steem

import (
	"context"
	"sync"
)

// FakeProvider is a test double for the Provider interface. It is intentionally
// hand-written (rather than mockgen-generated) so the test suite has zero
// external toolchain dependency.
//
// Each method field is a function the test sets; calling the method invokes the
// function (or returns the zero value if unset). Call counts are recorded.
type FakeProvider struct {
	mu sync.Mutex

	GetBlockFn              func(ctx context.Context, num int64) (map[string]interface{}, error)
	GetBlocksRangeFn        func(ctx context.Context, from, to int64) ([]map[string]interface{}, error)
	GetAccountsFn           func(ctx context.Context, names []string) ([]map[string]interface{}, error)
	GetContentFn            func(ctx context.Context, author, permlink string) (map[string]interface{}, error)
	GetContentBatchFn       func(ctx context.Context, posts [][]string) ([]map[string]interface{}, error)
	GetDynamicGlobalPropsFn func(ctx context.Context) (map[string]interface{}, error)
	HeadBlockFn             func(ctx context.Context) (int64, error)
	LastIrreversibleFn      func(ctx context.Context) (int64, error)

	// Call counters
	GetBlockCalls                   int
	GetBlocksRangeCalls             int
	GetAccountsCalls                int
	GetContentCalls                 int
	GetContentBatchCalls            int
	GetDynamicGlobalPropertiesCalls int
	HeadBlockCalls                  int
	LastIrreversibleCalls           int
}

func (f *FakeProvider) GetBlock(ctx context.Context, num int64) (map[string]interface{}, error) {
	f.mu.Lock()
	f.GetBlockCalls++
	fn := f.GetBlockFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, num)
}

func (f *FakeProvider) GetBlocksRange(ctx context.Context, from, to int64) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.GetBlocksRangeCalls++
	fn := f.GetBlocksRangeFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, from, to)
}

func (f *FakeProvider) GetAccounts(ctx context.Context, names []string) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.GetAccountsCalls++
	fn := f.GetAccountsFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, names)
}

func (f *FakeProvider) GetContent(ctx context.Context, author, permlink string) (map[string]interface{}, error) {
	f.mu.Lock()
	f.GetContentCalls++
	fn := f.GetContentFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, author, permlink)
}

func (f *FakeProvider) GetContentBatch(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.GetContentBatchCalls++
	fn := f.GetContentBatchFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, posts)
}

func (f *FakeProvider) GetDynamicGlobalProperties(ctx context.Context) (map[string]interface{}, error) {
	f.mu.Lock()
	f.GetDynamicGlobalPropertiesCalls++
	fn := f.GetDynamicGlobalPropsFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx)
}

func (f *FakeProvider) HeadBlock(ctx context.Context) (int64, error) {
	f.mu.Lock()
	f.HeadBlockCalls++
	fn := f.HeadBlockFn
	f.mu.Unlock()
	if fn == nil {
		return 0, nil
	}
	return fn(ctx)
}

func (f *FakeProvider) LastIrreversible(ctx context.Context) (int64, error) {
	f.mu.Lock()
	f.LastIrreversibleCalls++
	fn := f.LastIrreversibleFn
	f.mu.Unlock()
	if fn == nil {
		return 0, nil
	}
	return fn(ctx)
}

// Compile-time check that FakeProvider satisfies Provider.
var _ Provider = (*FakeProvider)(nil)
