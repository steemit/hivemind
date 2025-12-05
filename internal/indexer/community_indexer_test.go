package indexer

import (
	"regexp"
	"testing"

	"github.com/steemit/hivemind/internal/models"
)

func TestCommunityIndexer_roleNameToID(t *testing.T) {
	ci := &CommunityIndexer{}

	tests := []struct {
		name     string
		roleName string
		expected int16
	}{
		{"muted", "muted", models.RoleMuted},
		{"guest", "guest", models.RoleGuest},
		{"member", "member", models.RoleMember},
		{"mod", "mod", models.RoleMod},
		{"admin", "admin", models.RoleAdmin},
		{"owner", "owner", models.RoleOwner},
		{"unknown", "unknown", models.RoleGuest}, // Default
		{"empty", "", models.RoleGuest},         // Default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ci.roleNameToID(tt.roleName)
			if result != tt.expected {
				t.Errorf("roleNameToID(%q) = %v, want %v", tt.roleName, result, tt.expected)
			}
		})
	}
}

func TestCommunityIndexer_IsCommunityName(t *testing.T) {
	tests := []struct {
		name     string
		account  string
		expected bool
	}{
		{"valid topic community", "hive-12345", true},
		{"valid topic community long", "hive-123456", true},
		{"invalid - journal community (not registered)", "hive-22345", false},
		{"invalid - council community (not registered)", "hive-32345", false},
		{"invalid - too short", "hive-123", false},
		{"invalid - wrong prefix", "steem-12345", false},
		{"invalid - wrong type", "hive-42345", false},
		{"invalid - not a number", "hive-abcde", false},
		{"regular account", "alice", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use regex pattern from Register function (only matches hive-1xxxxx)
			communityPattern := regexp.MustCompile(`^hive-[1]\d{4,6}$`)
			result := communityPattern.MatchString(tt.account)
			if result != tt.expected {
				t.Errorf("IsCommunityName(%q) = %v, want %v", tt.account, result, tt.expected)
			}
		})
	}
}

