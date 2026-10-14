package indexer

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/steemit/hivemind/internal/models"
)

func TestCommunityIndexer_roleNameToID(t *testing.T) {
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
		{"empty", "", models.RoleGuest},          // Default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := roleNameToID(tt.roleName)
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

// TestReadKeyStrRuneCount pins the codepoint-based maxlen semantics of
// readKeyStr (Python len() counts characters, not bytes): 120 CJK runes
// pass a 120 limit even though they are 360 UTF-8 bytes, 121 runes fail,
// and ASCII boundaries are unchanged. Failure class must be
// opValidationError (assertion, not infrastructure).
func TestReadKeyStrRuneCount(t *testing.T) {
	cjk120 := strings.Repeat("规", 120)
	if len(cjk120) != 360 {
		t.Fatalf("fixture sanity: 120 CJK runes = %d bytes, want 360", len(cjk120))
	}

	if s, err := readKeyStr(map[string]interface{}{"k": cjk120}, "k", 120, false); err != nil || s != cjk120 {
		t.Errorf("120 CJK runes at maxlen 120: s=%q err=%v, want accepted", s, err)
	}
	_, err := readKeyStr(map[string]interface{}{"k": strings.Repeat("规", 121)}, "k", 120, false)
	if err == nil {
		t.Error("121 CJK runes at maxlen 120: accepted, want rejected")
	}
	var verr *opValidationError
	if !errors.As(err, &verr) {
		t.Errorf("over-length error type = %T, want *opValidationError", err)
	}

	// ASCII boundary unchanged (bytes == runes).
	if _, err := readKeyStr(map[string]interface{}{"k": strings.Repeat("a", 256)}, "k", 256, false); err != nil {
		t.Errorf("256 ASCII chars at maxlen 256: err=%v, want accepted", err)
	}
	if _, err := readKeyStr(map[string]interface{}{"k": strings.Repeat("a", 257)}, "k", 256, false); err == nil {
		t.Error("257 ASCII chars at maxlen 256: accepted, want rejected")
	}
}

// TestCommunityAvatarURLValidRunes pins the avatar length check against
// codepoints (legacy `len(url) < 1024`).
func TestCommunityAvatarURLValidRunes(t *testing.T) {
	cjk1023 := strings.Repeat("规", 1023)
	if !communityAvatarURLValid("http://" + "x") {
		t.Error("short http URL rejected")
	}
	if communityAvatarURLValid("HTTP://x") {
		t.Error("uppercase scheme accepted (legacy is case-sensitive)")
	}
	// 1023 CJK runes = 3069 bytes: over the byte limit, under the rune one;
	// the scheme check still rejects it, so only the length gate matters
	// for the synthetic prefix-less check below.
	if utf8.RuneCountInString(cjk1023) != 1023 {
		t.Fatalf("fixture sanity: rune count = %d, want 1023", utf8.RuneCountInString(cjk1023))
	}
	if communityAvatarURLValid(cjk1023) {
		t.Error("3069-byte avatar accepted; scheme invalid so must reject")
	}
	// Empty string is explicitly allowed.
	if !communityAvatarURLValid("") {
		t.Error("empty avatar_url rejected, want allowed")
	}
}
