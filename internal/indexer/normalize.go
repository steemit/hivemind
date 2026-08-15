package indexer

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Port of hive/utils/normalize.py (legacy reference for CachedPost scoring
// and account flush). Function-level behavior is kept faithful, including
// quirks (e.g. repLog10's string-based log10 and trunc's '...' penalty).
// int_log_level is intentionally not ported (Python logging specific).

// NAIMap maps steemd NAI asset identifiers to asset symbols.
// Mirrors legacy NAI_MAP (normalize.py:10).
var NAIMap = map[string]string{
	"@@000000013": "SBD",
	"@@000000021": "STEEM",
	"@@000000037": "VESTS",
}

// assetPrecision is the display precision per asset (legacy_amount, py:62).
var assetPrecision = map[string]int{"SBD": 3, "STEEM": 3, "VESTS": 6}

// VestsAmount returns the numeric value, asserting units are VESTS.
func VestsAmount(value interface{}) (float64, error) {
	return parseAmountUnit(value, "VESTS")
}

// SteemAmount returns the numeric value, asserting units are STEEM.
func SteemAmount(value interface{}) (float64, error) {
	return parseAmountUnit(value, "STEEM")
}

// SBDAmount returns the numeric value, asserting units are SBD.
func SBDAmount(value interface{}) (float64, error) {
	return parseAmountUnit(value, "SBD")
}

// ParseAmount parses a steemd-style amount/asset value and returns
// (decimal, unit). Accepts three input shapes, mirroring legacy
// parse_amount (normalize.py:28):
//   - string:  "123.500 SBD" (legacy format)
//   - list:    [satoshis, precision, nai] (appbase format)
//   - object:  {amount, precision, nai}
func ParseAmount(value interface{}) (float64, string, error) {
	switch v := value.(type) {
	case string:
		raw, unit, found := strings.Cut(v, " ")
		if !found {
			return 0, "", fmt.Errorf("invalid amount string %q", v)
		}
		amt, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, "", fmt.Errorf("invalid amount %q: %w", raw, err)
		}
		return amt, unit, nil
	case []interface{}:
		if len(v) != 3 {
			return 0, "", fmt.Errorf("invalid amount array %v", v)
		}
		return naiAmount(v[0], v[1], v[2])
	case map[string]interface{}:
		return naiAmount(v["amount"], v["precision"], v["nai"])
	case json.Number:
		return 0, "", fmt.Errorf("bare number is not a steemd amount: %v", v)
	default:
		return 0, "", fmt.Errorf("invalid input amount %#v", value)
	}
}

func parseAmountUnit(value interface{}, expected string) (float64, error) {
	amt, unit, err := ParseAmount(value)
	if err != nil {
		return 0, err
	}
	if unit != expected {
		return 0, fmt.Errorf("expected %s, got %s", expected, unit)
	}
	return amt, nil
}

// naiAmount converts [satoshis, precision, nai] via exact decimal string
// arithmetic (legacy uses decimal.Decimal; we shift the decimal point on the
// digit string before a single float conversion to avoid double rounding).
func naiAmount(satoshisV, precisionV, naiV interface{}) (float64, string, error) {
	nai, ok := naiV.(string)
	if !ok {
		return 0, "", fmt.Errorf("invalid NAI %#v", naiV)
	}
	unit, ok := NAIMap[nai]
	if !ok {
		return 0, "", fmt.Errorf("unknown NAI %s", nai)
	}
	satStr, err := numberString(satoshisV)
	if err != nil {
		return 0, "", err
	}
	prec, err := numberInt(precisionV)
	if err != nil || prec < 0 {
		return 0, "", fmt.Errorf("invalid precision %#v", precisionV)
	}
	amt, err := strconv.ParseFloat(shiftDecimal(satStr, prec), 64)
	if err != nil {
		return 0, "", err
	}
	return amt, unit, nil
}

// Amount parses a steemd asset-amount string, discarding the asset type.
func Amount(s string) (float64, error) {
	amt, _, err := ParseAmount(s)
	return amt, err
}

// LegacyAmount renders a value as a pre-appbase-style amount string
// ("452574.069424 VESTS"). String input passes through unchanged.
// Mirrors legacy_amount (normalize.py:57).
func LegacyAmount(value interface{}) (string, error) {
	if s, ok := value.(string); ok {
		return s, nil
	}
	amt, unit, err := ParseAmount(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%.*f %s", assetPrecision[unit], amt, unit), nil
}

// BlockNumFromID extracts the block number from a block_id hex prefix.
func BlockNumFromID(blockID string) (int64, error) {
	if len(blockID) < 8 {
		return 0, fmt.Errorf("invalid block_id %q", blockID)
	}
	return strconv.ParseInt(blockID[:8], 16, 64)
}

// BlockDate parses a block's timestamp field.
func BlockDate(block map[string]interface{}) (time.Time, error) {
	ts, _ := block["timestamp"].(string)
	return ParseTime(ts)
}

// ParseTime converts a chain date string ("2016-03-24T16:05:00") to a
// (zoneless) time.Time. Mirrors parse_time (normalize.py:74).
func ParseTime(blockTime string) (time.Time, error) {
	return time.Parse("2006-01-02T15:04:05", blockTime)
}

// UTCTimestamp converts a chain time to UTC unix seconds.
func UTCTimestamp(date time.Time) int64 {
	return date.UTC().Unix()
}

// LoadJSONKey parses the JSON string stored under obj[key]; empty map on
// missing/blank/invalid. Mirrors load_json_key (normalize.py:82).
func LoadJSONKey(obj map[string]string, key string) map[string]interface{} {
	out := map[string]interface{}{}
	raw, ok := obj[key]
	if !ok || raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// Trunc truncates a string to maxlen with a 3-char '...' penalty when
// exceeded. Operates on runes (Python slices by character).
// Mirrors trunc (normalize.py:93).
func Trunc(s string, maxlen int) string {
	if s == "" {
		return s
	}
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxlen {
		return s
	}
	return string(r[:maxlen-3]) + "..."
}

// SecsToStr renders seconds as e.g. "02h 30m 00s" / "01w 03d 04h 05m 06s".
// Mirrors secs_to_str (normalize.py:101).
func SecsToStr(secs int64) string {
	type unit struct {
		n    int64
		name string
	}
	var out []unit
	cycles := []struct {
		cycle int64
		name  string
	}{{60, "s"}, {60, "m"}, {24, "h"}, {7, "d"}}
	rem := secs
	for _, c := range cycles {
		out = append(out, unit{rem % c.cycle, c.name})
		rem = rem / c.cycle
		if rem == 0 {
			break
		}
	}
	if rem > 0 { // leftover = weeks
		out = append(out, unit{rem, "w"})
	}
	parts := make([]string, len(out))
	for i, u := range out {
		parts[len(out)-1-i] = fmt.Sprintf("%02d%s", u.n, u.name)
	}
	return strings.Join(parts, " ")
}

// RepLog10 converts a raw steemd reputation into a UI-ready value centered
// at 25. Uses legacy's string-based log10 (leading 4 digits + digit count)
// so behavior matches Python bit-for-bit for the rounding it applies.
// Mirrors rep_log10 (normalize.py:115).
func RepLog10(rep interface{}) (float64, error) {
	var s string
	switch v := rep.(type) {
	case string:
		s = v
	case int64:
		s = strconv.FormatInt(v, 10)
	case int:
		s = strconv.Itoa(v)
	case float64:
		s = strconv.FormatFloat(v, 'f', -1, 64)
	case json.Number:
		s = v.String()
	default:
		return 0, fmt.Errorf("invalid reputation %#v", rep)
	}
	if s == "0" {
		return 25, nil
	}
	sign := 1.0
	if strings.HasPrefix(s, "-") {
		sign = -1
		s = s[1:]
	}
	// Python slices string[0:4] — for short strings this takes what exists
	// (int("500") = 500), so take up to 4 chars rather than zero-padding.
	if s == "" || strings.TrimLeft(s, "0") == "" {
		return 25, nil
	}
	head := s
	if len(head) > 4 {
		head = head[:4]
	}
	leading, err := strconv.Atoi(head)
	if err != nil {
		return 0, fmt.Errorf("invalid reputation %q", s)
	}
	lg := math.Log10(float64(leading)) + 0.00000001
	out := float64(len(s)-1) + (lg - math.Floor(lg))
	out = math.Max(out-9, 0) * sign // at -9, $1 earned is approx magnitude 1
	out = out*9 + 25                // 9 points per magnitude, centered at 25
	return math.Round(out*100) / 100, nil
}

// SafeImgURL validates an image URL (size + http prefix) and returns it
// trimmed; empty string when invalid. Mirrors safe_img_url (normalize.py:148).
func SafeImgURL(url string, maxSize int) string {
	if url != "" && len(url) < maxSize && strings.HasPrefix(url, "http") {
		return strings.TrimSpace(url)
	}
	return ""
}

// StrToBool converts a booleany string to a bool; errors on anything else.
// Mirrors strtobool (normalize.py:156).
func StrToBool(val string) (bool, error) {
	switch strings.ToLower(val) {
	case "y", "yes", "t", "true", "on", "1":
		return true, nil
	case "n", "no", "f", "false", "off", "0":
		return false, nil
	default:
		return false, fmt.Errorf("not booleany: %q", val)
	}
}

// shiftDecimal divides an integer digit string by 10^prec exactly, returning
// a plain decimal string ("452574069424", 6 -> "452574.069424").
func shiftDecimal(satoshis string, prec int) string {
	neg := strings.HasPrefix(satoshis, "-")
	if neg {
		satoshis = satoshis[1:]
	}
	satoshis = strings.TrimLeft(satoshis, "0")
	if satoshis == "" {
		return "0"
	}
	if prec == 0 {
		if neg {
			return "-" + satoshis
		}
		return satoshis
	}
	for len(satoshis) <= prec {
		satoshis = "0" + satoshis
	}
	intPart := satoshis[:len(satoshis)-prec]
	frac := strings.TrimRight(satoshis[len(satoshis)-prec:], "0")
	out := intPart
	if frac != "" {
		out += "." + frac
	}
	if neg && out != "0" {
		out = "-" + out
	}
	return out
}

// numberString renders a JSON-decoded number (float64/json.Number/string)
// as an exact integer digit string.
func numberString(v interface{}) (string, error) {
	switch n := v.(type) {
	case string:
		return n, nil
	case json.Number:
		return n.String(), nil
	case float64:
		if n != math.Trunc(n) {
			return "", fmt.Errorf("expected integer satoshis, got %v", n)
		}
		return strconv.FormatFloat(n, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(n), nil
	case int64:
		return strconv.FormatInt(n, 10), nil
	default:
		return "", fmt.Errorf("invalid amount value %#v", v)
	}
}

// numberInt renders a JSON-decoded number as int.
func numberInt(v interface{}) (int, error) {
	switch n := v.(type) {
	case float64:
		return int(n), nil
	case json.Number:
		i, err := strconv.Atoi(n.String())
		if err != nil {
			return 0, err
		}
		return i, nil
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case string:
		return strconv.Atoi(n)
	default:
		return 0, fmt.Errorf("invalid numeric value %#v", v)
	}
}
