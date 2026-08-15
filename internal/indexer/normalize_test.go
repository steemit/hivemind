package indexer

import (
	"encoding/json"
	"testing"
)

func TestParseAmount(t *testing.T) {
	cases := []struct {
		name    string
		in      interface{}
		wantAmt float64
		wantU   string
		wantErr bool
	}{
		{"legacy string", "10.500 SBD", 10.5, "SBD", false},
		{"NAI list SBD", []interface{}{"1050000", 3, "@@000000013"}, 1050.0, "SBD", false},
		{"NAI list STEEM numeric", []interface{}{json.Number("1234"), 3, "@@000000021"}, 1.234, "STEEM", false},
		{"NAI list VESTS", []interface{}{"452574069424", 6, "@@000000037"}, 452574.069424, "VESTS", false},
		{"NAI object", map[string]interface{}{"amount": json.Number("5000"), "precision": 3, "nai": "@@000000013"}, 5.0, "SBD", false},
		{"unknown NAI", []interface{}{"1", 3, "@@000000099"}, 0, "", true},
		{"garbage string", "10.5SBD", 0, "", true},
		{"garbage type", 42, 0, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			amt, unit, err := ParseAmount(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got (%v,%v)", amt, unit)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if unit != c.wantU {
				t.Errorf("unit = %q, want %q", unit, c.wantU)
			}
			diff := amt - c.wantAmt
			if diff < -1e-9 || diff > 1e-9 {
				t.Errorf("amount = %v, want %v", amt, c.wantAmt)
			}
		})
	}
}

func TestAmountUnitHelpers(t *testing.T) {
	if v, err := VestsAmount("1.000000 VESTS"); err != nil || v != 1 {
		t.Errorf("VestsAmount = %v err %v", v, err)
	}
	if _, err := VestsAmount("1.000 SBD"); err == nil {
		t.Error("VestsAmount should reject SBD")
	}
	if v, err := SBDAmount("12.250 SBD"); err != nil || v != 12.25 {
		t.Errorf("SBDAmount = %v err %v", v, err)
	}
	if v, err := SteemAmount([]interface{}{"2000", 3, "@@000000021"}); err != nil || v != 2 {
		t.Errorf("SteemAmount = %v err %v", v, err)
	}
	if v, err := Amount("99.999 STEEM"); err != nil || v != 99.999 {
		t.Errorf("Amount = %v err %v", v, err)
	}
}

func TestLegacyAmount(t *testing.T) {
	// String passthrough.
	if s, err := LegacyAmount("1.500 SBD"); err != nil || s != "1.500 SBD" {
		t.Errorf("string passthrough = %q err %v", s, err)
	}
	// NAI → legacy with per-asset precision.
	if s, err := LegacyAmount([]interface{}{"452574069424", 6, "@@000000037"}); err != nil || s != "452574.069424 VESTS" {
		t.Errorf("VESTS = %q err %v", s, err)
	}
	if s, err := LegacyAmount([]interface{}{"2500", 3, "@@000000013"}); err != nil || s != "2.500 SBD" {
		t.Errorf("SBD = %q err %v", s, err)
	}
}

// TestRepLog10 pins the legacy rep_log10 formula. Expected values computed
// with the Python implementation.
func TestRepLog10(t *testing.T) {
	cases := []struct {
		in   interface{}
		want float64
	}{
		{"0", 25},             // literal zero
		{int64(0), 25},        // numeric zero formats to "0"
		{"1000000000", 25},    // 1e9  -> magnitude 0 -> centered 25
		{"1000000000000", 52}, // 1e12 -> 3 magnitudes -> 25+27
		{"-1000000000", 25},   // negative but max(out-9,0)=0 kills the sign
		// 12 nines: log10(9999)≈3.99996 → 11.99996 → 51.9996 → rounds to 52.
		{"999999999999", 52},
	}
	for _, c := range cases {
		got, err := RepLog10(c.in)
		if err != nil {
			t.Fatalf("RepLog10(%v) error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("RepLog10(%v) = %v, want %v", c.in, got, c.want)
		}
	}

	// Round-trip sanity with the API-side inverse (objects.repToRaw).
	if _, err := RepLog10("not-a-number!"); err == nil {
		t.Error("expected error for non-numeric reputation")
	}
}

func TestTrunc(t *testing.T) {
	if s := Trunc("hello world", 8); s != "hello..." {
		t.Errorf("Trunc = %q, want hello...", s)
	}
	if s := Trunc("short", 10); s != "short" {
		t.Errorf("Trunc under limit = %q", s)
	}
	if s := Trunc("  padded  ", 10); s != "padded" {
		t.Errorf("Trunc should trim, got %q", s)
	}
	// Rune-aware truncation (Python slices by character).
	if s := Trunc("你好世界测试文本", 5); s != "你好..." {
		t.Errorf("rune trunc = %q, want 你好...", s)
	}
}

func TestSecsToStr(t *testing.T) {
	cases := []struct {
		secs int64
		want string
	}{
		{0, "00s"},
		{59, "59s"},
		{61, "01m 01s"},
		{9000, "02h 30m 00s"},
		{86400, "01d 00h 00m 00s"},
		{691200, "01w 01d 00h 00m 00s"}, // 8 days
	}
	for _, c := range cases {
		if got := SecsToStr(c.secs); got != c.want {
			t.Errorf("SecsToStr(%d) = %q, want %q", c.secs, got, c.want)
		}
	}
}

func TestSafeImgURL(t *testing.T) {
	if u := SafeImgURL("https://x.com/a.png", 1024); u != "https://x.com/a.png" {
		t.Errorf("valid url = %q", u)
	}
	// Legacy checks the http prefix BEFORE stripping, so leading spaces
	// invalidate the URL; trailing spaces get trimmed.
	if u := SafeImgURL("https://x.com/a.png  ", 1024); u != "https://x.com/a.png" {
		t.Errorf("should trim trailing, got %q", u)
	}
	if u := SafeImgURL("  https://x.com/a.png", 1024); u != "" {
		t.Errorf("leading space must invalidate (legacy checks prefix first), got %q", u)
	}
	if u := SafeImgURL("ftp://x.com/a.png", 1024); u != "" {
		t.Errorf("non-http should be empty, got %q", u)
	}
	if u := SafeImgURL("", 1024); u != "" {
		t.Errorf("empty should be empty")
	}
	// Oversized (>=1024).
	long := "http://" + string(make([]byte, 1100))
	if u := SafeImgURL(long, 1024); u != "" {
		t.Errorf("oversized should be empty")
	}
}

func TestStrToBool(t *testing.T) {
	for _, s := range []string{"y", "YES", "true", "on", "1"} {
		if v, err := StrToBool(s); err != nil || !v {
			t.Errorf("StrToBool(%q) = %v err %v", s, v, err)
		}
	}
	for _, s := range []string{"n", "No", "false", "off", "0"} {
		if v, err := StrToBool(s); err != nil || v {
			t.Errorf("StrToBool(%q) = %v err %v", s, v, err)
		}
	}
	if _, err := StrToBool("maybe"); err == nil {
		t.Error("expected error for non-booleany")
	}
}

func TestBlockNumFromIDAndTime(t *testing.T) {
	// "00000064" hex = 100.
	if n, err := BlockNumFromID("0000006400aa00bb00cc00dd00ee00ff00112233"); err != nil || n != 100 {
		t.Errorf("BlockNumFromID = %v err %v", n, err)
	}
	if _, err := BlockNumFromID("short"); err == nil {
		t.Error("expected error for short id")
	}

	ts, err := ParseTime("2016-03-24T16:05:00")
	if err != nil {
		t.Fatalf("ParseTime: %v", err)
	}
	if ts.Year() != 2016 || ts.Month().String() != "March" || ts.Day() != 24 {
		t.Errorf("ParseTime = %v", ts)
	}
	// Chain epoch sanity: 2016-03-24T16:05:00Z = 1458835500.
	if UTCTimestamp(ts) != 1458835500 {
		t.Errorf("UTCTimestamp = %d", UTCTimestamp(ts))
	}

	bd, err := BlockDate(map[string]interface{}{"timestamp": "2016-03-24T16:05:00"})
	if err != nil || !bd.Equal(ts) {
		t.Errorf("BlockDate = %v err %v", bd, err)
	}
}

func TestLoadJSONKey(t *testing.T) {
	obj := map[string]string{
		"good":   `{"tags":["a"]}`,
		"blank":  "",
		"bad":    "{invalid",
		"absent": "",
	}
	if m := LoadJSONKey(obj, "good"); len(m) != 1 || m["tags"] == nil {
		t.Errorf("good = %v", m)
	}
	if m := LoadJSONKey(obj, "blank"); len(m) != 0 {
		t.Errorf("blank = %v", m)
	}
	if m := LoadJSONKey(obj, "bad"); len(m) != 0 {
		t.Errorf("bad should be empty map, got %v", m)
	}
	if m := LoadJSONKey(obj, "missing"); len(m) != 0 {
		t.Errorf("missing = %v", m)
	}
}

func TestShiftDecimal(t *testing.T) {
	cases := []struct {
		sat  string
		prec int
		want string
	}{
		{"452574069424", 6, "452574.069424"},
		{"1050000", 3, "1050"},
		{"5000", 3, "5"},
		{"1", 3, "0.001"},
		{"0", 3, "0"},
		{"-5000", 3, "-5"},
		{"123", 0, "123"},
	}
	for _, c := range cases {
		if got := shiftDecimal(c.sat, c.prec); got != c.want {
			t.Errorf("shiftDecimal(%q,%d) = %q, want %q", c.sat, c.prec, got, c.want)
		}
	}
}
