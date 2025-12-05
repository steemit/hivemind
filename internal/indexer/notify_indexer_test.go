package indexer

import (
	"testing"

	"github.com/steemit/hivemind/internal/models"
)

func TestGetNotifyTypeName(t *testing.T) {
	tests := []struct {
		name     string
		typeID   int16
		expected string
	}{
		{"new_community", models.NotifyTypeNewCommunity, "new_community"},
		{"set_role", models.NotifyTypeSetRole, "set_role"},
		{"follow", models.NotifyTypeFollow, "follow"},
		{"vote", models.NotifyTypeVote, "vote"},
		{"unknown", 999, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getNotifyTypeName(tt.typeID)
			if result != tt.expected {
				t.Errorf("getNotifyTypeName(%d) = %v, want %v", tt.typeID, result, tt.expected)
			}
		})
	}
}

func TestGetInt64(t *testing.T) {
	tests := []struct {
		name     string
		ptr      *int64
		expected int64
	}{
		{"nil pointer", nil, 0},
		{"valid pointer", int64Ptr(42), 42},
		{"zero value", int64Ptr(0), 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getInt64(tt.ptr)
			if result != tt.expected {
				t.Errorf("getInt64() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetString(t *testing.T) {
	tests := []struct {
		name     string
		ptr      *string
		expected string
	}{
		{"nil pointer", nil, ""},
		{"valid pointer", stringPtr("test"), "test"},
		{"empty string", stringPtr(""), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getString(tt.ptr)
			if result != tt.expected {
				t.Errorf("getString() = %v, want %v", result, tt.expected)
			}
		})
	}
}

// Helper functions
func int64Ptr(v int64) *int64 {
	return &v
}

func stringPtr(v string) *string {
	return &v
}

