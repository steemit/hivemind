package cache

import (
	"testing"
)

func TestHashKey(t *testing.T) {
	tests := []struct {
		name     string
		parts    []string
		expected string // We'll check consistency, not exact value
	}{
		{
			name:  "single part",
			parts: []string{"test"},
		},
		{
			name:  "multiple parts",
			parts: []string{"test", "key", "with", "many", "parts"},
		},
		{
			name:  "empty parts",
			parts: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hashed1 := HashKey(tt.parts...)
			hashed2 := HashKey(tt.parts...)

			// Hash should be consistent
			if hashed1 != hashed2 {
				t.Errorf("HashKey() should be consistent, got %s and %s", hashed1, hashed2)
			}

			// Hash should be 32 characters (MD5 hex)
			if len(hashed1) != 32 {
				t.Errorf("HashKey() should return 32 character hex string, got length %d", len(hashed1))
			}
		})
	}
}

func TestCache_NamespaceKey(t *testing.T) {
	cache := &Cache{}

	tests := []struct {
		name     string
		key      string
		expected string
	}{
		{
			name:     "simple key",
			key:      "test",
			expected: "hivemind:test",
		},
		{
			name:     "key with colon",
			key:      "test:key",
			expected: "hivemind:test:key",
		},
		{
			name:     "empty key",
			key:      "",
			expected: "hivemind:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cache.namespaceKey(tt.key)
			if result != tt.expected {
				t.Errorf("namespaceKey() = %v, want %v", result, tt.expected)
			}
		})
	}
}

