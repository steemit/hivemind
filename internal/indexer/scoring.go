package indexer

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Port of hive/utils/post.py — post normalization and trending/hot scoring
// used by CachedPost (PR#6c). Behavior mirrors the Python implementation,
// including its quirks (tag slicing before the nsfw check, flag_weight's
// cheap log10, preview truncation, [NUL] replacement).

// Mention name constraints (mentions regex, post.py:18):
//   - '@' at start, or preceded by a char NOT in [a-zA-Z0-9_!#$%&*@/]
//   - name: 3-16 chars, first/last alnum, middle allows [.-]
//   - NOT followed by [a-z] (Python negative lookahead; emulated below)
var mentionRunChars = regexp.MustCompile(`^[a-zA-Z0-9.\-]+$`)

// Mentions returns the @-mentioned account names in a post body
// (lowercased, deduped, first-occurrence order). The Python version returns
// a set (unordered); order here is deterministic.
func Mentions(body string) []string {
	seen := map[string]bool{}
	var out []string
	for i := 0; i < len(body); i++ {
		if body[i] != '@' {
			continue
		}
		if i > 0 && isMentionPreceded(body[i-1]) {
			continue
		}
		name, ok := scanMentionName(body, i+1)
		if !ok {
			continue
		}
		lower := strings.ToLower(name)
		if !seen[lower] {
			seen[lower] = true
			out = append(out, lower)
		}
	}
	return out
}

// isMentionPreceded reports whether prev invalidates an '@' mention
// (the class negated by the legacy regex: alnum plus _!#$%&*@/).
func isMentionPreceded(prev byte) bool {
	switch {
	case prev >= 'a' && prev <= 'z',
		prev >= 'A' && prev <= 'Z',
		prev >= '0' && prev <= '9':
		return true
	}
	return strings.IndexByte("_!#$%&*@/", prev) >= 0
}

// scanMentionName extracts the longest valid name starting at start,
// mirroring the backtracking of the Python regex (including the
// not-followed-by-[a-z] lookahead, which only bites when the char run
// exceeds the 16-char name cap).
func scanMentionName(body string, start int) (string, bool) {
	if start >= len(body) || !isMentionAlnum(body[start]) {
		return "", false
	}
	end := start
	for end < len(body) && mentionRunChars.MatchString(string(body[end])) {
		end++
	}
	run := body[start:end]
	hi := len(run)
	if hi > 16 {
		hi = 16
	}
	for n := hi; n >= 3; n-- {
		if !isMentionAlnum(run[n-1]) {
			continue // last char must be alnum
		}
		if n < len(run) && run[n] >= 'a' && run[n] <= 'z' {
			continue // followed by lowercase → lookahead fails
		}
		return run[:n], true
	}
	return "", false
}

func isMentionAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// PostBasic is the result of basic post normalization (post_basic, py:99).
type PostBasic struct {
	JSONMetadata     map[string]interface{}
	Image            string
	Tags             []string
	IsNSFW           bool
	Body             string
	Preview          string
	PayoutAt         string
	IsPaidout        bool
	IsPayoutDeclined bool
	IsFullPower      bool
}

// PostBasic normalizes a steemd post's json-metadata, tags, and payout flags.
func ComputePostBasic(post map[string]interface{}) PostBasic {
	b := PostBasic{JSONMetadata: map[string]interface{}{}}

	if raw, ok := post["json_metadata"].(string); ok && raw != "" {
		var md map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &md); err == nil && md != nil {
			b.JSONMetadata = md
		}
	}

	// Thumb URL from md.image (wrapped if scalar; invalid entries dropped;
	// key removed entirely when nothing survives — legacy mutates md).
	if imgAny, ok := b.JSONMetadata["image"]; ok {
		var imgs []interface{}
		switch v := imgAny.(type) {
		case []interface{}:
			imgs = v
		case string:
			imgs = []interface{}{v}
		}
		var valid []interface{}
		for _, e := range imgs {
			s, ok := e.(string)
			if !ok {
				continue
			}
			if u := SafeImgURL(s, 1024); u != "" {
				valid = append(valid, u)
			}
		}
		if len(valid) > 0 {
			b.JSONMetadata["image"] = valid
			b.Image, _ = valid[0].(string)
		} else {
			delete(b.JSONMetadata, "image")
		}
	}

	// Tags: category first, then md.tags (list only); normalize, dedupe,
	// cap at 5 — nsfw check happens AFTER the cap (legacy order).
	tags := []string{getStr(post, "category")}
	if rawTags, ok := b.JSONMetadata["tags"].([]interface{}); ok {
		for _, t := range rawTags {
			tags = append(tags, pyTagString(t))
		}
	}
	seen := map[string]bool{}
	for _, t := range tags {
		norm := truncateRunes(strings.Trim(strings.ToLower(t), "# "), 32)
		if norm == "" || seen[norm] {
			continue
		}
		seen[norm] = true
		b.Tags = append(b.Tags, norm)
		if len(b.Tags) == 5 {
			break
		}
	}
	b.IsNSFW = false
	for _, t := range b.Tags {
		if t == "nsfw" {
			b.IsNSFW = true
			break
		}
	}

	b.Body = strings.ReplaceAll(getStr(post, "body"), "\x00", "[NUL]")
	b.Preview = truncateRunes(b.Body, 1024)

	// Payout date: last_payout once paid out (cashout_time = 1969-…), else
	// the future cashout_time.
	cashout := getStr(post, "cashout_time")
	b.IsPaidout = strings.HasPrefix(cashout, "1969")
	if b.IsPaidout {
		b.PayoutAt = getStr(post, "last_payout")
	} else {
		b.PayoutAt = cashout
	}

	// Payout declined: max_accepted_payout = 0, or 100% burned to null.
	if maxPayout, err := SBDAmount(post["max_accepted_payout"]); err == nil && maxPayout == 0 {
		b.IsPayoutDeclined = true
	} else if bennies, ok := post["beneficiaries"].([]interface{}); ok && len(bennies) == 1 {
		if benny, ok := bennies[0].(map[string]interface{}); ok {
			weight, werr := numberInt(benny["weight"])
			if getStr(benny, "account") == "null" && werr == nil && weight == 10000 {
				b.IsPayoutDeclined = true
			}
		}
	}

	// 100% SP payout.
	if psd, err := numberInt(post["percent_steem_dollars"]); err == nil && psd == 0 {
		b.IsFullPower = true
	}

	return b
}

// legacyPostFields is the whitelist preserved in hive_posts_cache.raw_json
// (post_legacy, py:167).
var legacyPostFields = []string{
	"id", "url", "root_comment", "root_author", "root_permlink",
	"root_title", "parent_author", "parent_permlink",
	"max_accepted_payout", "percent_steem_dollars",
	"curator_payout_value", "allow_replies", "allow_votes",
	"allow_curation_rewards", "beneficiaries",
}

// LegacyPostFields extracts the legacy-useful subset of a steemd post.
func LegacyPostFields(post map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{}
	for _, k := range legacyPostFields {
		if v, ok := post[k]; ok {
			out[k] = v
		}
	}
	return out
}

// PostPayout is the computed payout/vote state (post_payout, py:179).
type PostPayout struct {
	Payout  float64
	RShares int64
	CSVotes string
	SCTrend float64
	SCHot   float64
}

// Trending score timescales (py:198-199).
const (
	SCTrendTimescale = 240000.0
	SCHotTimescale   = 10000.0
)

// PostPayout computes payout sum, rshares, the votes CSV blob, and the
// trend/hot scores for a steemd post.
func ComputePostPayout(post map[string]interface{}) (PostPayout, error) {
	var p PostPayout

	for _, key := range []string{"total_payout_value", "curator_payout_value", "pending_payout_value"} {
		amt, err := SBDAmount(post[key])
		if err != nil {
			return p, fmt.Errorf("%s: %w", key, err)
		}
		p.Payout += amt
	}

	votes, _ := post["active_votes"].([]interface{})
	rows := make([]string, 0, len(votes))
	for _, v := range votes {
		vote, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		rsStr, err := numberString(vote["rshares"])
		if err != nil {
			return p, err
		}
		rs, err := strconv.ParseInt(rsStr, 10, 64)
		if err != nil {
			return p, fmt.Errorf("vote rshares %q: %w", rsStr, err)
		}
		p.RShares += rs

		pct, err := numberString(vote["percent"])
		if err != nil {
			return p, err
		}
		rep, err := RepLog10(vote["reputation"])
		if err != nil {
			// Legacy rep_log10 never fails; degrade like Python's crash-free
			// paths by treating bad reps as the default.
			rep = 25
		}
		rows = append(rows, fmt.Sprintf("%s,%s,%s,%s",
			getStr(vote, "voter"), rsStr, pct, pyFloat(rep)))
	}
	p.CSVotes = strings.Join(rows, "\n")

	created := getStr(post, "created")
	ts, err := ParseTime(created)
	if err != nil {
		return p, fmt.Errorf("created %q: %w", created, err)
	}
	unix := UTCTimestamp(ts)
	p.SCTrend = Score(p.RShares, unix, SCTrendTimescale)
	p.SCHot = Score(p.RShares, unix, SCHotTimescale)
	return p, nil
}

// PostStats is the derived display state (post_stats, py:224).
type PostStats struct {
	Hide       bool
	Gray       bool
	AuthorRep  float64
	FlagWeight int
	TotalVotes int64
	UpVotes    int64
}

// PostStats computes vote statistics and the hide/gray flags.
func ComputePostStats(post map[string]interface{}) (PostStats, error) {
	var s PostStats
	var negRShares int64
	votes, _ := post["active_votes"].([]interface{})
	for _, v := range votes {
		vote, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		rsStr, err := numberString(vote["rshares"])
		if err != nil {
			return s, err
		}
		rs, err := strconv.ParseInt(rsStr, 10, 64)
		if err != nil {
			return s, fmt.Errorf("vote rshares %q: %w", rsStr, err)
		}
		if rs == 0 {
			continue
		}
		s.TotalVotes++
		if rs > 0 {
			s.UpVotes++
		} else {
			negRShares += rs
		}
	}

	// Cheap stake-based log10: digits of neg_rshares/2 minus 11
	// (1 ≈ $400 of downvote stake, 2 ≈ $4,000, …). Python's str(int) of a
	// negative value includes the sign, so count it via %d formatting.
	s.FlagWeight = len(fmt.Sprintf("%d", negRShares/2)) - 11
	if s.FlagWeight < 0 {
		s.FlagWeight = 0
	}

	rep, err := RepLog10(post["author_reputation"])
	if err != nil {
		return s, fmt.Errorf("author_reputation: %w", err)
	}
	s.AuthorRep = rep

	pending, _ := SBDAmount(post["pending_payout_value"])
	hasPendingPayout := pending >= 0.02

	s.Hide = rep < 0 && !hasPendingPayout
	s.Gray = rep < 1
	return s, nil
}

// Score calculates the trending/hot score. Source: steem tags_plugin
// calculate_score (referenced at py:214).
func Score(rshares int64, createdTimestamp int64, timescale float64) float64 {
	modScore := float64(rshares) / 10000000.0
	order := math.Log10(math.Max(math.Abs(modScore), 1))
	sign := 1.0
	if modScore <= 0 {
		sign = -1
	}
	return sign*order + float64(createdTimestamp)/timescale
}

// --- small helpers ---

func getStr(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// pyTagString renders a possibly-non-string metadata tag the way Python
// str() would.
func pyTagString(t interface{}) string {
	switch v := t.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		return v.String()
	case bool:
		return strconv.FormatBool(v)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", v)
	}
}

// pyFloat formats a float like Python's str(): integral values keep ".0".
func pyFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// truncateRunes caps a string at n characters (Python slices by char).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
