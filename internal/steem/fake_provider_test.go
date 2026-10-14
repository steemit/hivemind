package steem

import (
	"context"
	"fmt"
	"sync"
)

// FakeProvider is a test double for the Provider interface. It is intentionally
// hand-written (rather than mockgen-generated) so the test suite has zero
// external toolchain dependency.
//
// Each method field is a function the test sets; calling the method invokes the
// function. Call counts are recorded.
//
// Default behavior (Fn unset) deliberately FAILS LOUD: every method returns an
// explicit error instead of (zero value, nil). The Provider contract
// (provider.go) forbids nil-result-with-nil-error on failure paths — a fake
// that silently returns (nil, nil) lets bugs pass that the real client would
// surface, so test code that forgets to script an Fn gets an unmistakable
// error at the call site. The single sanctioned exception is the empty-request
// fast path of GetContentBatch, which mirrors the real client: zero posts
// return an empty slice and a nil error without consulting the Fn.
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

// errFnNotSet is the fail-loud default for unset Fns: a failure path must
// carry an error, never a silent (zero value, nil) — see provider.go.
func errFnNotSet(method string) error {
	return fmt.Errorf("fake provider: %s called without its Fn set (a failed call must never return a nil result and nil error; see the Provider contract)", method)
}

func (f *FakeProvider) GetBlock(ctx context.Context, num int64) (map[string]interface{}, error) {
	f.mu.Lock()
	f.GetBlockCalls++
	fn := f.GetBlockFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errFnNotSet("GetBlock")
	}
	return fn(ctx, num)
}

func (f *FakeProvider) GetBlocksRange(ctx context.Context, from, to int64) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.GetBlocksRangeCalls++
	fn := f.GetBlocksRangeFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errFnNotSet("GetBlocksRange")
	}
	return fn(ctx, from, to)
}

func (f *FakeProvider) GetAccounts(ctx context.Context, names []string) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.GetAccountsCalls++
	fn := f.GetAccountsFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errFnNotSet("GetAccounts")
	}
	return fn(ctx, names)
}

func (f *FakeProvider) GetContent(ctx context.Context, author, permlink string) (map[string]interface{}, error) {
	f.mu.Lock()
	f.GetContentCalls++
	fn := f.GetContentFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errFnNotSet("GetContent")
	}
	return fn(ctx, author, permlink)
}

func (f *FakeProvider) GetContentBatch(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.GetContentBatchCalls++
	fn := f.GetContentBatchFn
	f.mu.Unlock()
	// Contract fast path, identical to the real client: an explicitly empty
	// request is not a failure and must not consult (or require) the Fn.
	if len(posts) == 0 {
		return []map[string]interface{}{}, nil
	}
	if fn == nil {
		return nil, errFnNotSet("GetContentBatch")
	}
	return fn(ctx, posts)
}

func (f *FakeProvider) GetDynamicGlobalProperties(ctx context.Context) (map[string]interface{}, error) {
	f.mu.Lock()
	f.GetDynamicGlobalPropertiesCalls++
	fn := f.GetDynamicGlobalPropsFn
	f.mu.Unlock()
	if fn == nil {
		return nil, errFnNotSet("GetDynamicGlobalProperties")
	}
	return fn(ctx)
}

func (f *FakeProvider) HeadBlock(ctx context.Context) (int64, error) {
	f.mu.Lock()
	f.HeadBlockCalls++
	fn := f.HeadBlockFn
	f.mu.Unlock()
	if fn == nil {
		return 0, errFnNotSet("HeadBlock")
	}
	return fn(ctx)
}

func (f *FakeProvider) LastIrreversible(ctx context.Context) (int64, error) {
	f.mu.Lock()
	f.LastIrreversibleCalls++
	fn := f.LastIrreversibleFn
	f.mu.Unlock()
	if fn == nil {
		return 0, errFnNotSet("LastIrreversible")
	}
	return fn(ctx)
}

// Compile-time check that FakeProvider satisfies Provider.
var _ Provider = (*FakeProvider)(nil)
