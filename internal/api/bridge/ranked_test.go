package bridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestGetAccountPosts_FeedSortNoRecursion is a regression test for the
// 2026-08-16 security audit finding: `case "feed"` used to delegate to
// GetAccountPosts with unchanged params — infinite recursion, stack overflow,
// uncatchable by gin.Recovery(), crashing the whole process on a single
// unauthenticated request.
//
// The feed branch must return an explicit error BEFORE any DB access, so this
// test uses a zero-value RankedAPI (nil cursor/repo) and asserts we get an
// error, not a crash.
func TestGetAccountPosts_FeedSortNoRecursion(t *testing.T) {
	r := &RankedAPI{}
	gin.SetMode(gin.TestMode)
	ctx := &gin.Context{}

	params, _ := json.Marshal(map[string]interface{}{
		"sort":    "feed",
		"account": "alice",
	})

	result, err := r.GetAccountPosts(ctx, params)

	if err == nil {
		t.Fatalf("feed sort must return an explicit not-implemented error, got result=%v", result)
	}
	if !strings.Contains(err.Error(), "not implemented") {
		t.Errorf("error should mention 'not implemented', got: %v", err)
	}
}

// TestGetAccountPosts_UnimplementedSorts verifies the other stub sorts return
// explicit empty results (their documented placeholder shape) rather than
// delegating anywhere.
func TestGetAccountPosts_UnimplementedSorts(t *testing.T) {
	r := &RankedAPI{}
	gin.SetMode(gin.TestMode)
	ctx := &gin.Context{}

	for _, sort := range []string{"posts", "comments", "replies"} {
		params, _ := json.Marshal(map[string]interface{}{
			"sort":    sort,
			"account": "alice",
		})
		result, err := r.GetAccountPosts(ctx, params)
		if err != nil {
			t.Errorf("sort %q returned error %v (expected empty placeholder)", sort, err)
		}
		if result == nil {
			t.Errorf("sort %q returned nil (expected empty slice)", sort)
		}
	}
}
