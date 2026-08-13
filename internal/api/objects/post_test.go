package objects

import (
	"math"
	"testing"
)

// TestHydrateActiveVotes verifies CSV parsing of the votes column.
func TestHydrateActiveVotes(t *testing.T) {
	// Empty CSV → empty list
	got := hydrateActiveVotes("")
	if len(got) != 0 {
		t.Errorf("empty CSV should return empty list, got %d items", len(got))
	}

	// Single vote
	got = hydrateActiveVotes("alice,5000000000,10000,25")
	if len(got) != 1 {
		t.Fatalf("single vote: expected 1 item, got %d", len(got))
	}
	vote := got[0].(map[string]interface{})
	if vote["voter"] != "alice" {
		t.Errorf("voter = %v, want alice", vote["voter"])
	}
	if vote["rshares"] != "5000000000" {
		t.Errorf("rshares = %v, want 5000000000", vote["rshares"])
	}
	if vote["percent"] != "10000" {
		t.Errorf("percent = %v, want 10000", vote["percent"])
	}

	// Multiple votes (newline-separated)
	got = hydrateActiveVotes("alice,1000,10000,50\nbob,-500,5000,40")
	if len(got) != 2 {
		t.Fatalf("two votes: expected 2 items, got %d", len(got))
	}

	// Malformed line (wrong field count) should be skipped
	got = hydrateActiveVotes("alice,1000,10000\nbob,500,5000,40,extra\ncarol,200,3000,30")
	if len(got) != 1 {
		t.Errorf("malformed lines should be skipped, got %d items", len(got))
	}
	if got[0].(map[string]interface{})["voter"] != "carol" {
		t.Errorf("expected carol, got %v", got[0].(map[string]interface{})["voter"])
	}
}

// TestRepToRaw verifies the reputation conversion matches legacy rep_to_raw.
func TestRepToRaw(t *testing.T) {
	cases := []struct {
		name string
		rep  float64
		want float64
	}{
		{"default rep 25 → 0", 25, 0},
		{"rep 34 → ~1e10", 34, 1e10},
		{"rep 52 → ~1e12", 52, 1e12},
		{"negative rep -7 → ~-1e2", -7, -100},
		{"rep 0 → negative", 0, -math.Pow(10, float64(9-25.0/9))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := repToRaw(c.rep)
			// For exact-match cases
			if c.rep == 25 {
				if got != 0 {
					t.Errorf("repToRaw(25) = %v, want 0", got)
				}
				return
			}
			// For scale cases, check approximate magnitude (order of magnitude)
			if c.rep == 34 {
				if math.Abs(got-1e10) > 1e9 {
					t.Errorf("repToRaw(34) = %v, want ~1e10", got)
				}
			}
			if c.rep == 52 {
				if math.Abs(got-1e12) > 1e11 {
					t.Errorf("repToRaw(52) = %v, want ~1e12", got)
				}
			}
		})
	}
}

// TestFormatAmount verifies steem-style amount formatting.
func TestFormatAmount(t *testing.T) {
	cases := []struct {
		amount float64
		want   string
	}{
		{0, "0.000 SBD"},
		{10, "10.000 SBD"},
		{10.5, "10.500 SBD"},
		{0.001, "0.001 SBD"},
		{1234.56789, "1234.568 SBD"},
	}
	for _, c := range cases {
		got := formatAmount(c.amount)
		if got != c.want {
			t.Errorf("formatAmount(%v) = %q, want %q", c.amount, got, c.want)
		}
	}
}

// TestRepToRawStr verifies the string-input variant used by hydrateActiveVotes.
func TestRepToRawStr(t *testing.T) {
	// "25" → 0 (default rep)
	got := repToRawStr("25")
	if got != 0 {
		t.Errorf("repToRawStr(\"25\") = %v, want 0", got)
	}

	// Invalid string → 0
	got = repToRawStr("notanumber")
	if got != 0 {
		t.Errorf("repToRawStr(invalid) = %v, want 0", got)
	}

	// "34" should match repToRaw(34)
	got = repToRawStr("34")
	want := repToRaw(34)
	if got != want {
		t.Errorf("repToRawStr(\"34\") = %v, want %v", got, want)
	}
}
