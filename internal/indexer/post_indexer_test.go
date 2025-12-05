package indexer

import (
	"testing"
)

func TestExtractCategory(t *testing.T) {
	tests := []struct {
		name     string
		jsonMeta string
		expected string
	}{
		{
			name:     "empty metadata",
			jsonMeta: "",
			expected: "",
		},
		{
			name:     "valid tags array",
			jsonMeta: `{"tags":["steem","blockchain","crypto"]}`,
			expected: "steem",
		},
		{
			name:     "tags with hash prefix",
			jsonMeta: `{"tags":["#steem","blockchain"]}`,
			expected: "steem",
		},
		{
			name:     "tags as string (legacy)",
			jsonMeta: `{"tags":"steem blockchain crypto"}`,
			expected: "steem",
		},
		{
			name:     "tags with spaces and hash",
			jsonMeta: `{"tags":" #steem  blockchain"}`,
			expected: "steem",
		},
		{
			name:     "long tag truncated",
			jsonMeta: `{"tags":["this-is-a-very-long-tag-name-that-should-be-truncated"]}`,
			expected: "this-is-a-very-long-tag-name-tha", // 32 chars
		},
		{
			name:     "invalid JSON",
			jsonMeta: `{"tags":["steem"`,
			expected: "",
		},
		{
			name:     "no tags field",
			jsonMeta: `{"app":"steemit/0.1"}`,
			expected: "",
		},
		{
			name:     "empty tags array",
			jsonMeta: `{"tags":[]}`,
			expected: "",
		},
		{
			name:     "tags with mixed case",
			jsonMeta: `{"tags":["Steem","BLOCKCHAIN"]}`,
			expected: "steem",
		},
		{
			name:     "tags with non-string first element",
			jsonMeta: `{"tags":[123,"steem"]}`,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractCategory(tt.jsonMeta)
			if result != tt.expected {
				t.Errorf("extractCategory(%q) = %q, want %q", tt.jsonMeta, result, tt.expected)
			}
		})
	}
}

