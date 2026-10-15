// Package main implements the hivemind concurrency load generator
// (planned PR#432, 05-deploy.md section 2 note 4: REQUIRED harness for the
// 7-day stability window - "low-pressure all-day + daily 1h high-pressure
// spike" against the comparison case set at 10-50 QPS).
//
// cases.go: the request case set. Cases come either from the api-comparison
// YAML (tests/api-comparison/test_cases.yaml format - `test_cases:` entries
// with `api` + `params`, the same file the comparator runs, so the harness
// and the comparison suite exercise identical traffic) or from the small
// embedded default set of read-only P0 queries.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	yaml "go.yaml.in/yaml/v3"
)

// Case is one JSON-RPC request template. Params is `any` because the wire
// protocol uses positional arrays for condenser_api.* and single objects
// for bridge.* (see tests/api-comparison/test_cases.yaml).
type Case struct {
	API    string `yaml:"api" json:"api"`
	Params any    `yaml:"params" json:"params"`
}

// body renders the JSON-RPC 2.0 request body for the case.
func (c Case) body(id int) ([]byte, error) {
	return json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  c.API,
		"params":  c.Params,
	})
}

// comparisonCaseFile mirrors the subset of the comparator YAML the harness
// consumes. Entries without `api` are ignored (the files carry plenty of
// documentation fields).
type comparisonCaseFile struct {
	TestCases []Case `yaml:"test_cases"`
}

// loadCases reads a case set. An empty path selects the embedded defaults;
// a path ending in .yaml/.yml parses the api-comparison format.
func loadCases(path string) ([]Case, error) {
	if path == "" {
		return defaultCases(), nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read cases file: %w", err)
	}
	var cf comparisonCaseFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		return nil, fmt.Errorf("parse cases file %s: %w", path, err)
	}
	var out []Case
	for _, c := range cf.TestCases {
		if c.API == "" {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cases file %s contains no usable test_cases entries", path)
	}
	return out, nil
}

// defaultCases is the zero-config fallback: cheap read-only queries across
// the three API namespaces, params matching the comparator samples.
func defaultCases() []Case {
	return []Case{
		{API: "hive.db_head_state", Params: []any{}},
		{API: "condenser_api.get_trending_tags", Params: []any{"", 10}},
		{API: "condenser_api.get_account_reputations", Params: []any{"", 10}},
		{API: "condenser_api.get_discussions_by_trending", Params: []any{map[string]any{"limit": 10}}},
		{API: "bridge.get_profile", Params: map[string]any{"account": "steemit"}},
		{API: "bridge.list_communities", Params: map[string]any{"last": ""}},
	}
}
