package apierrors

import (
	"testing"
)

func TestValidAccount(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		allowEmpty bool
		wantErr    bool
	}{
		{"valid", "alice", false, false},
		{"with dot/dash", "a.b-c", false, false},
		{"empty not allowed", "", false, true},
		{"empty allowed", "", true, false},
		{"too short", "ab", false, true},
		{"too long", "abcdefghijklmnopq", false, true},
		{"leading at", "@alice", false, true},
		{"uppercase", "Alice", false, true},
		{"bad char", "bad:name", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ValidAccount(c.in, c.allowEmpty)
			if c.wantErr && err == nil {
				t.Errorf("ValidAccount(%q) should fail", c.in)
			}
			if !c.wantErr && err != nil {
				t.Errorf("ValidAccount(%q) = %v", c.in, err)
			}
			if err != nil {
				if _, ok := err.(PublicError); !ok {
					t.Errorf("error should be PublicError, got %T", err)
				}
			}
		})
	}
}

func TestValidPermlink(t *testing.T) {
	if _, err := ValidPermlink("ok-permlink", false); err != nil {
		t.Errorf("valid permlink failed: %v", err)
	}
	if _, err := ValidPermlink("", false); err == nil {
		t.Error("blank permlink should fail")
	}
	if _, err := ValidPermlink("", true); err != nil {
		t.Error("blank allowed should pass")
	}
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'x'
	}
	if _, err := ValidPermlink(string(long), false); err == nil {
		t.Error("oversized permlink should fail")
	}
}

func TestValidLimitAndClamp(t *testing.T) {
	if _, err := ValidLimit(20, 100); err != nil {
		t.Errorf("valid limit failed: %v", err)
	}
	if _, err := ValidLimit(-5, 100); err == nil {
		t.Error("negative limit should fail")
	}
	if _, err := ValidLimit(0, 100); err == nil {
		t.Error("zero limit should fail")
	}
	if _, err := ValidLimit(101, 100); err == nil {
		t.Error("over-limit should fail")
	}
	if ClampLimit(-5) != 0 {
		t.Error("ClampLimit(-5) should be 0")
	}
	if ClampLimit(0) != 0 {
		t.Error("ClampLimit(0) should be 0")
	}
	if ClampLimit(50) != 50 {
		t.Error("ClampLimit(50) should be 50")
	}
}

func TestPublicErrorFormatting(t *testing.T) {
	e := Publicf("limit exceeds max (%d > %d)", 101, 100)
	if e.Error() != "limit exceeds max (101 > 100)" {
		t.Errorf("Publicf = %q", e.Error())
	}
}

func TestValidTag(t *testing.T) {
	if _, err := ValidTag("life", false); err != nil {
		t.Errorf("valid tag failed: %v", err)
	}
	if _, err := ValidTag("steem-1_0", false); err != nil {
		t.Errorf("tag with dash/underscore failed: %v", err)
	}
	if _, err := ValidTag("", true); err != nil {
		t.Errorf("empty allowed tag failed: %v", err)
	}
	if _, err := ValidTag("", false); err == nil {
		t.Error("blank tag should fail when not allowed")
	} else if e := err.(PublicError); string(e) != "tag was blank" {
		t.Errorf("blank tag message = %q, want legacy 'tag was blank'", string(e))
	}
	if _, err := ValidTag("BadTag", false); err == nil {
		t.Error("uppercase tag should fail")
	} else if e := err.(PublicError); string(e) != "invalid tag `BadTag`" {
		t.Errorf("invalid tag message = %q, want legacy format", string(e))
	}
	if _, err := ValidTag("nsfw tag", false); err == nil {
		t.Error("tag with space should fail")
	}
}

func TestValidQueryLimit(t *testing.T) {
	// Absent keeps the default (legacy `limit: int = 20` signatures).
	if got, err := ValidQueryLimit(map[string]interface{}{}, 20, 100); err != nil || got != 20 {
		t.Errorf("absent limit = %d, %v; want default 20", got, err)
	}
	// Present and valid.
	p := map[string]interface{}{"limit": float64(50)}
	if got, err := ValidQueryLimit(p, 20, 100); err != nil || got != 50 {
		t.Errorf("valid limit = %d, %v; want 50", got, err)
	}
	// Explicit JSON null is legacy None -> "limit must be provided".
	if _, err := ValidQueryLimit(map[string]interface{}{"limit": nil}, 20, 100); err == nil {
		t.Error("null limit should fail")
	} else if e := err.(PublicError); string(e) != "limit must be provided" {
		t.Errorf("null limit message = %q", string(e))
	}
	// Legacy error messages for invalid values.
	for _, tc := range []struct {
		limit float64
		want  string
	}{
		{-5, "limit must be positive"},
		{0, "limit must be positive"},
		{101, "limit exceeds max (101 > 100)"},
	} {
		_, err := ValidQueryLimit(map[string]interface{}{"limit": tc.limit}, 20, 100)
		if err == nil {
			t.Errorf("limit %v should fail", tc.limit)
			continue
		}
		if e := err.(PublicError); string(e) != tc.want {
			t.Errorf("limit %v message = %q, want %q", tc.limit, string(e), tc.want)
		}
	}
	// Non-numeric values are treated as absent (registered leniency).
	if got, err := ValidQueryLimit(map[string]interface{}{"limit": "ten"}, 20, 100); err != nil || got != 20 {
		t.Errorf("non-numeric limit = %d, %v; want default 20", got, err)
	}
}
