package indexer

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Port of hive/utils/account.py (profile sanitization) plus the account
// flush side of hive/indexer/accounts.py (_cache_accounts/_sql).

// ProfileFields is the sanitized profile subset written to hive_accounts.
type ProfileFields struct {
	Name         string
	About        string
	Location     string
	Website      string
	ProfileImage string
	CoverImage   string
}

// uselessAccountKeys are stripped from raw_json (legacy _sql `useless` list).
var uselessAccountKeys = []string{
	"transfer_history", "market_history", "post_history",
	"vote_history", "other_history", "tags_usage", "guest_bloggers",
}

// SafeProfileMetadata extracts and sanitizes the profile block from a
// steemd account: posting_json_metadata (version 2, number OR string form)
// first, falling back to json_metadata; empty strings on any failure.
// Mirrors legacy safe_profile_metadata (utils/account.py).
func SafeProfileMetadata(account map[string]interface{}) ProfileFields {
	var prof map[string]interface{}

	// Primary: posting_json_metadata with version 2.
	if raw, ok := account["posting_json_metadata"].(string); ok && raw != "" {
		var md map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &md); err == nil {
			if p, ok := md["profile"].(map[string]interface{}); ok {
				if v, present := p["version"]; present && (v == float64(2) || v == "2") {
					prof = p
				}
			}
		}
	}
	// Fallback: json_metadata profile block.
	if prof == nil {
		if raw, ok := account["json_metadata"].(string); ok && raw != "" {
			var md map[string]interface{}
			if err := json.Unmarshal([]byte(raw), &md); err == nil {
				if p, ok := md["profile"].(map[string]interface{}); ok {
					prof = p
				}
			}
		}
	}
	if prof == nil {
		return ProfileFields{}
	}

	str := func(key string) string {
		if v, ok := prof[key].(string); ok {
			return v
		}
		return ""
	}
	pf := ProfileFields{
		Name:         charPolice(Trunc(str("name"), 20)),
		About:        charPolice(Trunc(str("about"), 160)),
		Location:     charPolice(Trunc(str("location"), 30)),
		Website:      charPolice(str("website")),
		ProfileImage: charPolice(str("profile_image")),
		CoverImage:   charPolice(str("cover_image")),
	}

	// Leading '@' names are dropped (legacy).
	if strings.HasPrefix(pf.Name, "@") {
		pf.Name = ""
	}
	if len(pf.Website) > 100 || !validURLProto(pf.Website) {
		if pf.Website != "" {
			if len(pf.Website) > 100 {
				pf.Website = ""
			} else {
				pf.Website = "http://" + pf.Website
			}
		}
	}
	if pf.Website != "" && !validURLProto(pf.Website) {
		pf.Website = "http://" + pf.Website
	}
	if pf.ProfileImage != "" && (!validURLProto(pf.ProfileImage) || len(pf.ProfileImage) > 1024) {
		pf.ProfileImage = ""
	}
	if pf.CoverImage != "" && (!validURLProto(pf.CoverImage) || len(pf.CoverImage) > 1024) {
		pf.CoverImage = ""
	}
	return pf
}

// charPolice drops strings containing NUL (Postgres rejects them).
func charPolice(s string) string {
	if strings.ContainsRune(s, 0) {
		return ""
	}
	return s
}

func validURLProto(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

// accountUselessKeysNote: raw_json keeps the steemd account minus the
// bulky history arrays and the two metadata blobs (already extracted).

// Flush processes the dirty queue: fetches accounts from steemd in batches
// of 1000 and updates ~17 columns. Returns the number processed.
func (ai *AccountIndexer) FlushBatch(ctx context.Context, trx bool) (int, error) {
	ai.mu.Lock()
	names := make([]string, 0, len(ai.dirty))
	for name := range ai.dirty {
		names = append(names, name)
	}
	ai.dirty = map[string]bool{}
	ai.mu.Unlock()

	if len(names) == 0 {
		return 0, nil
	}
	sort.Strings(names)

	gdb := ai.repo.DB()
	processed := 0
	for start := 0; start < len(names); start += 1000 {
		end := start + 1000
		if end > len(names) {
			end = len(names)
		}
		accts, err := ai.steem.GetAccounts(ctx, names[start:end])
		if err != nil {
			return processed, err
		}
		cachedAt := time.Now().UTC().Truncate(time.Second)

		updates := make([]map[string]interface{}, 0, len(accts))
		for _, acct := range accts {
			if getStr(acct, "name") == "" {
				continue
			}
			updates = append(updates, ai.buildAccountUpdate(ctx, acct, cachedAt))
		}

		err = gdb.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for _, vals := range updates {
				name := vals["name"]
				delete(vals, "name")
				if err := tx.Table("hive_accounts").
					Where("name = ?", name).
					Updates(vals).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return processed, err
		}
		processed += len(updates)
	}
	return processed, nil
}

// buildAccountUpdate computes the column values for one account
// (legacy accounts.py::_sql).
func (ai *AccountIndexer) buildAccountUpdate(ctx context.Context, acct map[string]interface{}, cachedAt time.Time) map[string]interface{} {
	gdb := ai.repo.DB()

	vests, _ := VestsAmount(acct["vesting_shares"])
	received, _ := VestsAmount(acct["received_vesting_shares"])
	delegated, _ := VestsAmount(acct["delegated_vesting_shares"])
	voteWeight := vests + received - delegated

	proxyWeight := 0.0
	if proxy, _ := acct["proxy"].(string); proxy == "" {
		proxyWeight = vests
		if proxied, ok := acct["proxied_vsf_votes"].([]interface{}); ok {
			for _, sat := range proxied {
				switch v := sat.(type) {
				case float64:
					proxyWeight += v / 1e6
				case string:
					if f, err := strconv.ParseFloat(v, 64); err == nil {
						proxyWeight += f / 1e6
					}
				}
			}
		}
	}

	profile := SafeProfileMetadata(acct)

	// active_at = max of the five activity timestamps.
	activeAt := maxChainTime(acct,
		"created", "last_account_update", "last_post", "last_root_post", "last_vote_time")

	repRaw, err := RepLog10(acct["reputation"])
	if err != nil {
		repRaw = 25
	}

	values := map[string]interface{}{
		"name":          getStr(acct, "name"),
		"proxy":         getStr(acct, "proxy"),
		"reputation":    repRaw,
		"proxy_weight":  proxyWeight,
		"vote_weight":   voteWeight,
		"active_at":     activeAt,
		"cached_at":     cachedAt,
		"display_name":  nullStr(profile.Name),
		"about":         nullStr(profile.About),
		"location":      nullStr(profile.Location),
		"website":       nullStr(profile.Website),
		"profile_image": profile.ProfileImage,
		"cover_image":   profile.CoverImage,
	}

	if postCount, err := numberInt(acct["post_count"]); err == nil {
		values["post_count"] = postCount
	}
	if created, err := ParseTime(getStr(acct, "created")); err == nil {
		values["created_at"] = created
	}

	// raw_json: the steemd account minus bulky/useless keys and metadata blobs.
	raw := map[string]interface{}{}
	for k, v := range acct {
		raw[k] = v
	}
	for _, k := range uselessAccountKeys {
		delete(raw, k)
	}
	delete(raw, "json_metadata")
	delete(raw, "posting_json_metadata")
	if rawJSON, err := json.Marshal(raw); err == nil {
		values["raw_json"] = string(rawJSON)
	}

	// Rank (when the shared cache knows this account).
	name := getStr(acct, "name")
	var id int64
	gdb.WithContext(ctx).
		Table("hive_accounts").
		Select("id").
		Where("name = ?", name).
		Scan(&id)
	if id != 0 {
		if rank, ok := sharedRankCache(gdb).Rank(ctx, id); ok {
			values["rank"] = rank
		}
	}

	return values
}

func maxChainTime(acct map[string]interface{}, keys ...string) time.Time {
	var best time.Time
	for _, k := range keys {
		t, err := ParseTime(getStr(acct, k))
		if err != nil {
			continue
		}
		if t.After(best) {
			best = t
		}
	}
	return best
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
