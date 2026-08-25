package condenser

import (
	"encoding/json"

	"github.com/steemit/hivemind/internal/apierrors"
)

// parseQueryParams accepts all parameter shapes steemd/condenser clients send
// for discussion-style queries and returns a unified params map:
//
//  1. object:        {"tag": "steem", "limit": 1}
//  2. nested query:  [{"tag": "steem", "limit": 1}]   (nested_query_compat)
//  3. positional:    ["steem", "", "", 1]            (jussi/call style)
//
// For the positional form, posKeys maps array indices to query keys — e.g.
// trending-style calls pass ("tag", "start_author", "start_permlink",
// "limit") while replies-style calls pass ("start_author", "start_permlink",
// "limit"), matching each method's legacy positional signature. Extra array
// elements beyond posKeys are ignored, like steemd does.
//
// Mirrors the combination of legacy _strict_query and nested_query_compat.
func parseQueryParams(params json.RawMessage, posKeys ...string) (map[string]interface{}, error) {
	// Shape 1: a plain object.
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err == nil {
		return pMap, nil
	}

	// Shape 2/3: an array.
	var arr []interface{}
	if err := json.Unmarshal(params, &arr); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}
	if len(arr) == 0 {
		return map[string]interface{}{}, nil
	}

	// Nested query object inside the list.
	if m, ok := arr[0].(map[string]interface{}); ok {
		return m, nil
	}

	// Positional args mapped through posKeys.
	out := map[string]interface{}{}
	for i, key := range posKeys {
		if i >= len(arr) || arr[i] == nil {
			continue
		}
		switch v := arr[i].(type) {
		case string:
			if v != "" {
				out[key] = v
			}
		case float64:
			out[key] = v
		}
	}
	return out, nil
}

// parseListParams validates a positional parameter list with a length window
// (mirrors legacy _strict_list): exactly expectedLen params, or between
// minLen and expectedLen when minLen is set.
func parseListParams(params json.RawMessage, expectedLen int, minLen int) ([]interface{}, error) {
	var arr []interface{}
	if err := json.Unmarshal(params, &arr); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}
	if minLen < 0 {
		minLen = expectedLen
	}
	if len(arr) > expectedLen || len(arr) < minLen {
		return nil, apierrors.Publicf("expected %d params", expectedLen)
	}
	return arr, nil
}

// paramString extracts a string element from a positional list.
func paramString(arr []interface{}, idx int) string {
	if idx < len(arr) {
		if s, ok := arr[idx].(string); ok {
			return s
		}
	}
	return ""
}

// paramInt extracts an int element from a positional list.
func paramInt(arr []interface{}, idx int) int {
	if idx < len(arr) {
		if f, ok := arr[idx].(float64); ok {
			return int(f)
		}
	}
	return 0
}
