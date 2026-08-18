package indexer

import (
	"math"
	"strings"
	"testing"
)

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestMentions pins the legacy regex semantics, including the negative
// lookahead (?![a-z]) that the Go port emulates by scanning.
func TestMentions(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"simple", "hi @alice!", []string{"alice"}},
		{"start of string", "@bob here", []string{"bob"}},
		{"dedupe + lowercase", "@Alice and @alice", []string{"alice"}},
		{"too short", "x @ab y", nil},
		{"preceded by alnum", "email x@y.com", nil},
		{"preceded by @", "@@alice", nil},
		{"preceded by slash", "x/@alice", nil},
		{"underscore ends the name", "@charlie_mc", []string{"charlie"}},
		{"trailing period trimmed from name", "cc @dave.", []string{"dave"}},
		{"digits ok", "ping @user123 now", []string{"user123"}},
		{"16 lowercase chars ok", "@" + strings.Repeat("a", 16), []string{strings.Repeat("a", 16)}},
		{"17 lowercase chars: lookahead kills all", "@" + strings.Repeat("a", 17), nil},
		{"17th char uppercase: 16-char name", "@" + strings.Repeat("a", 16) + "Zx", []string{strings.Repeat("a", 16)}},
		{"multiple", "@one @two and @three", []string{"one", "two", "three"}},
		{"name with dot", "see @a.b here", []string{"a.b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Mentions(c.body)
			if !eqStrings(got, c.want) {
				t.Errorf("Mentions(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

func TestScore(t *testing.T) {
	// Mirror of the formula; asserts the sign/order interplay.
	ts := int64(1458835500)
	cases := []struct {
		rshares   int64
		timescale float64
	}{
		{0, SCTrendTimescale},
		{100000000, SCTrendTimescale}, // mod=10 → order=1
		{-20000000, SCHotTimescale},   // mod=-2 → order=log10(2), negative sign
	}
	for _, c := range cases {
		want := mirrorScore(c.rshares, ts, c.timescale)
		got := Score(c.rshares, ts, c.timescale)
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("Score(%d,%d,%v) = %v, want %v", c.rshares, ts, c.timescale, got, want)
		}
	}
	// Concrete anchor: mod=10 → +1, ts/240000 = 6078.479…
	anchor := Score(100000000, ts, SCTrendTimescale)
	if math.Abs(anchor-(1+float64(ts)/240000)) > 1e-9 {
		t.Errorf("anchor = %v", anchor)
	}
}

func mirrorScore(rshares int64, ts int64, tscale float64) float64 {
	mod := float64(rshares) / 1e7
	order := math.Log10(math.Max(math.Abs(mod), 1))
	sign := 1.0
	if mod <= 0 {
		sign = -1
	}
	return sign*order + float64(ts)/tscale
}

func steemdPost() map[string]interface{} {
	return map[string]interface{}{
		"author":                "alice",
		"permlink":              "p1",
		"category":              "life",
		"depth":                 0,
		"created":               "2026-01-02T03:04:05",
		"last_update":           "2026-01-02T03:04:05",
		"title":                 "T",
		"body":                  "hello @bob",
		"json_metadata":         `{"tags":["life","travel","life"],"image":["ftp://bad","https://ok.example/i.png"]}`,
		"cashout_time":          "2026-01-09T03:04:05",
		"last_payout":           "1969-12-31T23:59:59",
		"max_accepted_payout":   "1000.000 SBD",
		"percent_steem_dollars": 10000,
		"beneficiaries":         []interface{}{},
		"total_payout_value":    "1.000 SBD",
		"curator_payout_value":  "0.500 SBD",
		"pending_payout_value":  "2.500 SBD",
		"net_rshares":           "3000",
		"children":              2,
		"author_reputation":     "1000000000000", // UI 52
		"active_votes": []interface{}{
			map[string]interface{}{"voter": "bob", "rshares": "2000", "percent": "10000", "reputation": "0"},
			map[string]interface{}{"voter": "carol", "rshares": "-1000", "percent": "-5000", "reputation": "500000000"},
			map[string]interface{}{"voter": "dave", "rshares": "0", "percent": "100", "reputation": "0"},
		},
	}
}

func TestComputePostBasic(t *testing.T) {
	post := steemdPost()
	b := ComputePostBasic(post)

	// Tags: category + md tags, deduped, ≤5; nsfw absent.
	if !eqStrings(b.Tags, []string{"life", "travel"}) {
		t.Errorf("Tags = %v", b.Tags)
	}
	if b.IsNSFW {
		t.Error("IsNSFW should be false")
	}

	// Image: invalid dropped, valid kept; md.image mutated to valid-only.
	if b.Image != "https://ok.example/i.png" {
		t.Errorf("Image = %q", b.Image)
	}
	imgs, _ := b.JSONMetadata["image"].([]interface{})
	if len(imgs) != 1 || imgs[0] != "https://ok.example/i.png" {
		t.Errorf("md.image = %v", imgs)
	}

	// Not paid out (cashout 2026): payout_at = cashout_time.
	if b.IsPaidout || b.PayoutAt != "2026-01-09T03:04:05" {
		t.Errorf("PayoutAt = %q paid=%v", b.PayoutAt, b.IsPaidout)
	}
	if b.IsPayoutDeclined || b.IsFullPower {
		t.Error("declined/full_power should be false")
	}
	if b.Preview != "hello @bob" {
		t.Errorf("Preview = %q", b.Preview)
	}
}

func TestComputePostBasic_PaidAndDeclined(t *testing.T) {
	post := steemdPost()
	// Paid out: cashout_time resets to 1969 epoch.
	post["cashout_time"] = "1969-12-31T23:59:59"
	b := ComputePostBasic(post)
	if !b.IsPaidout || b.PayoutAt != "1969-12-31T23:59:59" {
		t.Errorf("paid: %v %q", b.IsPaidout, b.PayoutAt)
	}

	// Declined via max_accepted_payout = 0.
	post["max_accepted_payout"] = "0.000 SBD"
	if b = ComputePostBasic(post); !b.IsPayoutDeclined {
		t.Error("max=0 should decline")
	}

	// Declined via 100% burn beneficiary.
	post["max_accepted_payout"] = "1000.000 SBD"
	post["beneficiaries"] = []interface{}{
		map[string]interface{}{"account": "null", "weight": 10000},
	}
	if b = ComputePostBasic(post); !b.IsPayoutDeclined {
		t.Error("null beneficiary 100% should decline")
	}
	// 50% burn does not decline.
	post["beneficiaries"] = []interface{}{
		map[string]interface{}{"account": "null", "weight": 5000},
	}
	if b = ComputePostBasic(post); b.IsPayoutDeclined {
		t.Error("50% beneficiary should not decline")
	}

	// Full power.
	post["percent_steem_dollars"] = 0
	if b = ComputePostBasic(post); !b.IsFullPower {
		t.Error("percent 0 should be full power")
	}
}

func TestComputePostPayout(t *testing.T) {
	post := steemdPost()
	p, err := ComputePostPayout(post)
	if err != nil {
		t.Fatalf("ComputePostPayout: %v", err)
	}
	if p.Payout != 4.0 { // 1.0 + 0.5 + 2.5
		t.Errorf("Payout = %v, want 4", p.Payout)
	}
	if p.RShares != 1000 { // 2000 - 1000 + 0
		t.Errorf("RShares = %d", p.RShares)
	}
	lines := strings.Split(p.CSVotes, "\n")
	if len(lines) != 3 {
		t.Fatalf("CSVotes lines = %d: %q", len(lines), p.CSVotes)
	}
	// rep 0 → 25 → Python str() keeps ".0".
	if lines[0] != "bob,2000,10000,25.0" {
		t.Errorf("line0 = %q", lines[0])
	}
	if lines[1] != "carol,-1000,-5000,25.0" {
		t.Errorf("line1 = %q", lines[1])
	}
	// Scores: rshares=1000 → mod=1e-4 → order=0 → ±0; anchor trend.
	want := mirrorScore(1000, 1767323045, SCTrendTimescale) // 2026-01-02T03:04:05Z
	if math.Abs(p.SCTrend-want) > 1e-9 {
		t.Errorf("SCTrend = %v, want %v", p.SCTrend, want)
	}
}

func TestComputePostStats(t *testing.T) {
	post := steemdPost()
	s, err := ComputePostStats(post)
	if err != nil {
		t.Fatalf("ComputePostStats: %v", err)
	}
	if s.TotalVotes != 2 || s.UpVotes != 1 { // zero-rshares vote skipped
		t.Errorf("votes = %d/%d", s.TotalVotes, s.UpVotes)
	}
	// neg_rshares=-1000 → /2 = -500 → "-500" len 4 → 4-11 <0 → 0.
	if s.FlagWeight != 0 {
		t.Errorf("FlagWeight = %d", s.FlagWeight)
	}
	if s.AuthorRep != 52 {
		t.Errorf("AuthorRep = %v", s.AuthorRep)
	}
	// rep 52 → not gray, not hidden; pending 2.5 ≥ 0.02 anyway.
	if s.Gray || s.Hide {
		t.Errorf("gray=%v hide=%v", s.Gray, s.Hide)
	}
}

func TestComputePostStats_HideGray(t *testing.T) {
	post := steemdPost()
	// UI rep < 1 (negative raw rep) → gray; pending ≥ 0.02 keeps it visible.
	post["author_reputation"] = "-10000000000000000" // UI ≈ -29
	s, _ := ComputePostStats(post)
	if !s.Gray || s.Hide {
		t.Errorf("gray=%v hide=%v, want gray only (pending payout)", s.Gray, s.Hide)
	}
	// No pending payout → hidden.
	post["pending_payout_value"] = "0.000 SBD"
	s, _ = ComputePostStats(post)
	if !s.Hide {
		t.Error("negative rep + no pending should hide")
	}
	// Flag weight: neg_rshares = -2e13 → /2 = -1e13 → 15 digits → 4.
	post["active_votes"] = []interface{}{
		map[string]interface{}{"voter": "x", "rshares": "-20000000000000", "percent": "-100", "reputation": "0"},
	}
	s, _ = ComputePostStats(post)
	if s.FlagWeight != 4 {
		t.Errorf("FlagWeight = %d, want 4", s.FlagWeight)
	}
}

func TestLegacyPostFields(t *testing.T) {
	post := steemdPost()
	post["url"] = "/life/@alice/p1"
	post["root_title"] = "T"
	post["should_drop"] = "x"
	out := LegacyPostFields(post)
	if _, ok := out["url"]; !ok {
		t.Error("url missing")
	}
	if _, ok := out["root_title"]; !ok {
		t.Error("root_title missing")
	}
	if _, ok := out["should_drop"]; ok {
		t.Error("non-whitelisted key leaked")
	}
	if _, ok := out["author"]; ok {
		t.Error("author should not be in legacy set")
	}
}

func TestPyFloat(t *testing.T) {
	if pyFloat(25) != "25.0" {
		t.Errorf("pyFloat(25) = %q", pyFloat(25))
	}
	if pyFloat(-7.25) != "-7.25" {
		t.Errorf("pyFloat(-7.25) = %q", pyFloat(-7.25))
	}
}
