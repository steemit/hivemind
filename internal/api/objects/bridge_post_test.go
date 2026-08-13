package objects

import (
	"encoding/json"
	"testing"
)

// TestHydrateBridgeVotes verifies the 2-field vote CSV parsing.
func TestHydrateBridgeVotes(t *testing.T) {
	// Empty
	got := hydrateBridgeVotes("")
	if len(got) != 0 {
		t.Errorf("empty CSV should return empty, got %d", len(got))
	}

	// Single vote (2 fields only, unlike condenser's 4)
	got = hydrateBridgeVotes("alice,5000000000,10000,25")
	if len(got) != 1 {
		t.Fatalf("expected 1 vote, got %d", len(got))
	}
	v := got[0].(map[string]interface{})
	if v["voter"] != "alice" {
		t.Errorf("voter = %v, want alice", v["voter"])
	}
	if v["rshares"] != "5000000000" {
		t.Errorf("rshares = %v, want 5000000000", v["rshares"])
	}
	// Should NOT have percent or reputation (bridge_api uses 2 fields)
	if _, ok := v["percent"]; ok {
		t.Error("bridge votes should not have percent field")
	}
	if _, ok := v["reputation"]; ok {
		t.Error("bridge votes should not have reputation field")
	}

	// Multiple votes
	got = hydrateBridgeVotes("alice,1000\nbob,-500")
	if len(got) != 2 {
		t.Fatalf("expected 2 votes, got %d", len(got))
	}
}

// TestParseRawJSON verifies extraction of import fields.
func TestParseRawJSON(t *testing.T) {
	// Empty or too short
	r := parseRawJSON("")
	if len(r) != 0 {
		t.Errorf("empty raw_json should return empty map, got %d fields", len(r))
	}
	r = parseRawJSON("{}")
	if len(r) != 0 {
		t.Errorf("short raw_json (<33 chars) should return empty, got %d fields", len(r))
	}

	// Valid raw_json with fields
	raw := `{"beneficiaries":[{"account":"alice","weight":100}],"max_accepted_payout":"1000.000 SBD","percent_steem_dollars":10000,"parent_author":"bob","parent_permlink":"post1","root_title":"Original Title","url":"/tag/@bob/post1","curator_payout_value":"5.000 SBD"}`
	r = parseRawJSON(raw)

	if _, ok := r["beneficiaries"]; !ok {
		t.Error("missing beneficiaries")
	}
	if _, ok := r["max_accepted_payout"]; !ok {
		t.Error("missing max_accepted_payout")
	}
	if _, ok := r["percent_steem_dollars"]; !ok {
		t.Error("missing percent_steem_dollars")
	}
	if _, ok := r["parent_author"]; !ok {
		t.Error("missing parent_author")
	}
	if _, ok := r["root_title"]; !ok {
		t.Error("missing root_title")
	}
	if _, ok := r["url"]; !ok {
		t.Error("missing url")
	}

	// Verify url value
	if r["url"] != "/tag/@bob/post1" {
		t.Errorf("url = %v, want /tag/@bob/post1", r["url"])
	}
}

// TestRoleIDToString verifies the role mapping.
func TestRoleIDToString(t *testing.T) {
	cases := []struct {
		id   int16
		want string
	}{
		{-2, "muted"},
		{0, "guest"},
		{2, "member"},
		{4, "mod"},
		{6, "admin"},
		{8, "owner"},
	}
	for _, c := range cases {
		got := RoleIDToString[c.id]
		if got != c.want {
			t.Errorf("RoleIDToString[%d] = %q, want %q", c.id, got, c.want)
		}
	}
}

// TestSplitNewlines verifies the newline splitter.
func TestSplitNewlines(t *testing.T) {
	got := splitNewlines("a\nb\nc")
	if len(got) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(got))
	}
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("got %v, want [a b c]", got)
	}

	// Trailing newline → strings.Split includes trailing empty string
	got = splitNewlines("a\nb\n")
	if len(got) != 3 {
		t.Fatalf("expected 3 (incl empty trailing), got %d", len(got))
	}
	if got[2] != "" {
		t.Errorf("third element should be empty string, got %q", got[2])
	}
}

// TestSplitCommas verifies the comma splitter.
func TestSplitCommas(t *testing.T) {
	got := splitCommas("a,b,c,d")
	if len(got) != 4 {
		t.Fatalf("expected 4 parts, got %d", len(got))
	}
}

// TestParseRawJSON_InvalidJSON verifies graceful handling of bad JSON.
func TestParseRawJSON_InvalidJSON(t *testing.T) {
	r := parseRawJSON("not valid json but long enough to pass length check xxxxxxxxxx")
	if len(r) != 0 {
		t.Errorf("invalid JSON should return empty map, got %d fields", len(r))
	}
}

// TestBuildBridgePost_BasicShape is a smoke test of buildBridgePost field shape.
func TestBuildBridgePost_BasicShape(t *testing.T) {
	t.Skip("requires models — covered by integration test in PR#5c")
	_ = json.Marshal
}
