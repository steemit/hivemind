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
