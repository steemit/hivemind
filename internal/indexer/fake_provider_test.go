package indexer

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/steemit/hivemind/internal/steem"
)

// fakeSteemProvider is a test double for steem.Provider used by indexer tests.
// Hand-written (no mockgen) to keep the test suite free of external toolchain
// dependency. It is behaviorally aligned with internal/steem's FakeProvider
// (different field naming, same contract — see TestFakeSteemProviderUnsetFnFailsLoud
// at the bottom of this file):
//
//   - A method invoked without its Fn set FAILS LOUD: it returns an explicit
//     error instead of (nil, nil). The Provider contract (steem/provider.go)
//     forbids nil-result-with-nil-error on failure paths — a fake that
//     silently returns (nil, nil) lets production bugs pass that the real
//     client would surface, so test code that forgets to script an Fn gets
//     an unmistakable error at the call site.
//   - The single sanctioned exception is GetContentBatch's empty-request
//     fast path, which mirrors the real client: zero posts return an empty
//     slice and a nil error without consulting (or requiring) the Fn.
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

// errFakeFnNotSet is the fail-loud default for unset Fns: a failure path
// must carry an error, never a silent (zero value, nil) — see steem/provider.go.
func errFakeFnNotSet(method string) error {
	return fmt.Errorf("fake provider: %s called without its Fn set (a failed call must never return a nil result and nil error; see the Provider contract)", method)
}

func (f *fakeSteemProvider) GetBlock(ctx context.Context, num int64) (map[string]interface{}, error) {
	f.mu.Lock()
	f.nBlock++
	fn := f.getBlock
	f.mu.Unlock()
	if fn == nil {
		return nil, errFakeFnNotSet("GetBlock")
	}
	return fn(ctx, num)
}

func (f *fakeSteemProvider) GetBlocksRange(ctx context.Context, from, to int64) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.nBlocksRange++
	fn := f.getBlocksRange
	f.mu.Unlock()
	if fn == nil {
		return nil, errFakeFnNotSet("GetBlocksRange")
	}
	return fn(ctx, from, to)
}

func (f *fakeSteemProvider) GetAccounts(ctx context.Context, names []string) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.nAccounts++
	fn := f.getAccounts
	f.mu.Unlock()
	if fn == nil {
		return nil, errFakeFnNotSet("GetAccounts")
	}
	return fn(ctx, names)
}

func (f *fakeSteemProvider) GetContent(ctx context.Context, author, permlink string) (map[string]interface{}, error) {
	f.mu.Lock()
	f.nContent++
	fn := f.getContent
	f.mu.Unlock()
	if fn == nil {
		return nil, errFakeFnNotSet("GetContent")
	}
	return fn(ctx, author, permlink)
}

func (f *fakeSteemProvider) GetContentBatch(ctx context.Context, posts [][]string) ([]map[string]interface{}, error) {
	f.mu.Lock()
	f.nBatch++
	fn := f.getContentBatch
	f.mu.Unlock()
	// Contract fast path, identical to the real client: an explicitly empty
	// request is not a failure and must not consult (or require) the Fn.
	if len(posts) == 0 {
		return []map[string]interface{}{}, nil
	}
	if fn == nil {
		return nil, errFakeFnNotSet("GetContentBatch")
	}
	return fn(ctx, posts)
}

func (f *fakeSteemProvider) GetDynamicGlobalProperties(ctx context.Context) (map[string]interface{}, error) {
	f.mu.Lock()
	f.nDGP++
	fn := f.getDynamicGlobalProperties
	f.mu.Unlock()
	if fn == nil {
		return nil, errFakeFnNotSet("GetDynamicGlobalProperties")
	}
	return fn(ctx)
}

func (f *fakeSteemProvider) HeadBlock(ctx context.Context) (int64, error) {
	f.mu.Lock()
	f.nHead++
	fn := f.headBlock
	f.mu.Unlock()
	if fn == nil {
		return 0, errFakeFnNotSet("HeadBlock")
	}
	return fn(ctx)
}

func (f *fakeSteemProvider) LastIrreversible(ctx context.Context) (int64, error) {
	f.mu.Lock()
	f.nLastIrr++
	fn := f.lastIrreversible
	f.mu.Unlock()
	if fn == nil {
		return 0, errFakeFnNotSet("LastIrreversible")
	}
	return fn(ctx)
}

// TestFakeSteemProviderUnsetFnFailsLoud pins this double's contract sync with
// internal/steem's FakeProvider: every method fails loud when its Fn is unset
// (never a silent nil-result/nil-error), and GetContentBatch's empty-request
// fast path succeeds without an Fn, mirroring the real client.
func TestFakeSteemProviderUnsetFnFailsLoud(t *testing.T) {
	fake := &fakeSteemProvider{}
	ctx := context.Background()

	if res, err := fake.GetBlock(ctx, 1); err == nil || res != nil {
		t.Fatalf("GetBlock (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetBlocksRange(ctx, 1, 2); err == nil || res != nil {
		t.Fatalf("GetBlocksRange (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetAccounts(ctx, []string{"a"}); err == nil || res != nil {
		t.Fatalf("GetAccounts (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetContent(ctx, "a", "p"); err == nil || res != nil {
		t.Fatalf("GetContent (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetContentBatch(ctx, [][]string{{"a", "p"}}); err == nil || res != nil {
		t.Fatalf("GetContentBatch (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.GetDynamicGlobalProperties(ctx); err == nil || res != nil {
		t.Fatalf("GetDynamicGlobalProperties (Fn unset) = (%v, %v); want (nil, error)", res, err)
	}
	if res, err := fake.HeadBlock(ctx); err == nil || res != 0 {
		t.Fatalf("HeadBlock (Fn unset) = (%v, %v); want (0, error)", res, err)
	}
	if res, err := fake.LastIrreversible(ctx); err == nil || res != 0 {
		t.Fatalf("LastIrreversible (Fn unset) = (%v, %v); want (0, error)", res, err)
	}

	// Empty-request fast path: mirrors the real client even without an Fn.
	res, err := fake.GetContentBatch(ctx, nil)
	if err != nil || res == nil || len(res) != 0 {
		t.Fatalf("GetContentBatch(nil) (Fn unset) = (%v, %v); want (empty non-nil slice, nil)", res, err)
	}
}
