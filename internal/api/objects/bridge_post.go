package objects

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/steemit/hivemind/internal/models"
)

// RoleIDToString maps the numeric role_id to its display name.
// Mirrors legacy ROLES map (bridge_api/objects.py:39).
var RoleIDToString = map[int16]string{
	-2: "muted",
	0:  "guest",
	2:  "member",
	4:  "mod",
	6:  "admin",
	8:  "owner",
}

// LoadPostsKeyed loads posts by ID and returns a map keyed by post_id,
// producing the bridge_api post object shape (used by bridge.get_discussion).
//
// This mirrors the legacy hive/server/bridge_api/objects.py::load_posts_keyed.
// It differs from the condenser_api LoadPosts in several ways:
//   - json_metadata is a parsed object (not raw string)
//   - active_votes has 2 fields (voter, rshares), not 4
//   - author_reputation is the raw float from hive_posts_cache (not repToRaw)
//   - payout keys are is_paidout/payout_at/payout (not total/pending_payout_value)
//   - includes stats sub-object {hide, gray, total_votes, flag_weight, is_pinned}
//   - includes community/community_title/author_role/author_title (batch queries)
//   - includes raw_json import fields (beneficiaries, max_accepted_payout, etc.)
func (l *PostLoader) LoadPostsKeyed(ctx context.Context, ids []int64, truncateBody int) (map[int64]map[string]interface{}, error) {
	if len(ids) == 0 {
		return map[int64]map[string]interface{}{}, nil
	}

	// 1. Batch fetch from hive_posts_cache (chunk at 1000 like legacy).
	var caches []models.PostCache
	if err := l.db.WithContext(ctx).Where("post_id IN ?", ids).Find(&caches).Error; err != nil {
		return nil, fmt.Errorf("failed to load post caches: %w", err)
	}
	cacheMap := make(map[int64]*models.PostCache, len(caches))
	for i := range caches {
		cacheMap[caches[i].PostID] = &caches[i]
	}

	// 2. Load posts from hive_posts.
	var posts []models.Post
	if err := l.db.WithContext(ctx).Where("id IN ?", ids).Find(&posts).Error; err != nil {
		return nil, fmt.Errorf("failed to load posts: %w", err)
	}
	postMap := make(map[int64]*models.Post, len(posts))
	for i := range posts {
		postMap[posts[i].ID] = &posts[i]
	}

	// 3. Author map: name → {id, name, reputation} (needs id for roles query).
	accountNames := make(map[string]bool)
	for _, p := range posts {
		accountNames[p.Author] = true
	}
	nameList := make([]string, 0, len(accountNames))
	for n := range accountNames {
		nameList = append(nameList, n)
	}
	var accounts []models.Account
	if len(nameList) > 0 {
		if err := l.db.WithContext(ctx).
			Where("name IN ?", nameList).
			Select("id", "name", "reputation").
			Find(&accounts).Error; err != nil {
			return nil, fmt.Errorf("failed to load accounts: %w", err)
		}
	}
	type authorInfo struct {
		ID   int64
		Name string
		Rep  float64
	}
	authorMap := make(map[string]*authorInfo, len(accounts))
	idToName := make(map[int64]string, len(accounts))
	for i := range accounts {
		authorMap[accounts[i].Name] = &authorInfo{ID: accounts[i].ID, Name: accounts[i].Name, Rep: accounts[i].Reputation}
		idToName[accounts[i].ID] = accounts[i].Name
	}

	// 4. Build per-post objects, collect community/account IDs for batch queries.
	postsByID := make(map[int64]map[string]interface{}, len(ids))
	ctxAccounts := make(map[int64][]int64) // community_id → [account_id...]
	postCIDs := make(map[int64]int64)      // post_id → community_id

	for _, id := range ids {
		post := postMap[id]
		if post == nil {
			continue
		}
		cache := cacheMap[id]
		if cache == nil {
			continue
		}
		author := authorMap[post.Author]
		if author == nil {
			continue
		}

		obj := l.buildBridgePost(post, cache, author.Rep, truncateBody)
		postsByID[id] = obj

		// Collect community context for batch queries.
		if cache.CommunityID.Valid {
			cid := cache.CommunityID.Int64
			postCIDs[id] = cid
			ctxAccounts[cid] = append(ctxAccounts[cid], author.ID)
		}
	}

	// 5. Batch query: community titles.
	cidSet := make(map[int64]bool)
	for _, cid := range postCIDs {
		cidSet[cid] = true
	}
	commTitles := make(map[int64]string)
	if len(cidSet) > 0 {
		cids := make([]int64, 0, len(cidSet))
		for c := range cidSet {
			cids = append(cids, c)
		}
		var comms []models.Community
		if err := l.db.WithContext(ctx).
			Where("id IN ?", cids).
			Select("id", "title").
			Find(&comms).Error; err == nil {
			for _, c := range comms {
				commTitles[c.ID] = c.Title
			}
		}
	}

	// 6. Batch query: roles (community_id, account_id) → (role_id, title).
	type roleKey struct{ CID, AID int64 }
	roles := make(map[roleKey]models.Role)
	if len(ctxAccounts) > 0 {
		allCIDs := make([]int64, 0, len(cidSet))
		allAIDs := make([]int64, 0)
		for cid, aids := range ctxAccounts {
			allCIDs = append(allCIDs, cid)
			allAIDs = append(allAIDs, aids...)
		}
		if len(allCIDs) > 0 && len(allAIDs) > 0 {
			var roleRows []models.Role
			if err := l.db.WithContext(ctx).
				Where("community_id IN ? AND account_id IN ?", allCIDs, allAIDs).
				Find(&roleRows).Error; err == nil {
				for _, r := range roleRows {
					roles[roleKey{r.CommunityID, r.AccountID}] = r
				}
			}
		}
	}

	// 7. Decorate posts with community/role info.
	for pid, obj := range postsByID {
		cid, hasCID := postCIDs[pid]
		if !hasCID {
			continue
		}
		obj["community"] = fmt.Sprintf("hive-%d", cid)
		obj["community_title"] = commTitles[cid]

		// Find the author's role in this community.
		authorName, _ := obj["author"].(string)
		author := authorMap[authorName]
		if author != nil {
			if r, ok := roles[roleKey{cid, author.ID}]; ok {
				obj["author_role"] = RoleIDToString[r.RoleID]
				obj["author_title"] = r.Title
			} else {
				obj["author_role"] = "guest"
				obj["author_title"] = ""
			}
		}
	}

	// 8. Batch query: pinned posts.
	pinnedSet := make(map[int64]bool)
	if len(ids) > 0 {
		var pinnedPosts []models.Post
		if err := l.db.WithContext(ctx).
			Where("id IN ? AND is_pinned = ? AND is_deleted = ?", ids, true, false).
			Select("id").
			Find(&pinnedPosts).Error; err == nil {
			for _, p := range pinnedPosts {
				pinnedSet[p.ID] = true
			}
		}
	}

	// 9. Set is_pinned in stats.
	for pid, obj := range postsByID {
		stats, _ := obj["stats"].(map[string]interface{})
		if stats != nil {
			stats["is_pinned"] = pinnedSet[pid]
		}
	}

	return postsByID, nil
}

// LoadPostsBridge loads posts by ID and returns them as an ordered array of
// bridge_api post objects (same shape as LoadPostsKeyed). Mirrors legacy
// bridge objects.load_posts.
func (l *PostLoader) LoadPostsBridge(ctx context.Context, ids []int64, truncateBody int) ([]map[string]interface{}, error) {
	byID, err := l.LoadPostsKeyed(ctx, ids, truncateBody)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		if obj, ok := byID[id]; ok {
			out = append(out, obj)
		}
	}
	return out, nil
}

// LoadPostsReblogsBridge extends LoadPostsBridge with reblog attribution:
// rebloggers is a post_id → CSV-of-names map (from pids_by_feed_with_reblog's
// string_agg). The post author is excluded from the list, matching legacy
// bridge objects.load_posts_reblogs.
func (l *PostLoader) LoadPostsReblogsBridge(ctx context.Context, ids []int64, rebloggers map[int64]string, truncateBody int) ([]map[string]interface{}, error) {
	posts, err := l.LoadPostsBridge(ctx, ids, truncateBody)
	if err != nil {
		return nil, err
	}
	for _, post := range posts {
		csv, ok := rebloggers[post["post_id"].(int64)]
		if !ok || csv == "" {
			continue
		}
		author, _ := post["author"].(string)
		seen := make(map[string]bool)
		rby := make([]string, 0, 4)
		for _, name := range splitCommas(csv) {
			if name == "" || name == author || seen[name] {
				continue
			}
			seen[name] = true
			rby = append(rby, name)
		}
		if len(rby) > 0 {
			post["reblogged_by"] = rby
		}
	}
	return posts, nil
}

// sbdAmount parses a steemd-style SBD amount ("1.234 SBD" string, bare
// number, or {amount, precision, nai} object) into its numeric value.
// Mirrors legacy utils/normalize.py sbd_amount/parse_amount.
func sbdAmount(value interface{}) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case string:
		fields := strings.Fields(v)
		if len(fields) == 0 {
			return 0
		}
		amt, err := strconv.ParseFloat(fields[0], 64)
		if err != nil {
			return 0
		}
		return amt
	case map[string]interface{}:
		if amt, ok := v["amount"].(float64); ok {
			return amt / 1000 // asset amount is fixed-point with precision 3
		}
	}
	return 0
}

// buildBridgePost builds a single bridge_api-shape post object.
// Mirrors legacy _condenser_post_object (bridge_api/objects.py:231).
func (l *PostLoader) buildBridgePost(post *models.Post, cache *models.PostCache, authorRep float64, truncateBody int) map[string]interface{} {
	body := cache.Body
	if truncateBody > 0 && len(body) > truncateBody {
		body = body[:truncateBody]
	}

	// Parse json_metadata into a dict (bridge_api returns parsed, not raw string).
	var jsonMeta interface{}
	if cache.JSON != "" {
		if err := json.Unmarshal([]byte(cache.JSON), &jsonMeta); err != nil {
			jsonMeta = map[string]interface{}{}
		}
	} else {
		jsonMeta = map[string]interface{}{}
	}

	// active_votes: bridge_api uses 2 fields (voter, rshares) only.
	votes := hydrateBridgeVotes(cache.Votes)

	// Parse raw_json for import fields.
	rawFields := parseRawJSON(cache.RawJSON)

	// Payout split: pending before payout, author+curator after.
	var pendingPayout, authorPayout float64
	if cache.IsPaidout {
		authorPayout = cache.Payout
	} else {
		pendingPayout = cache.Payout
	}

	obj := map[string]interface{}{
		"post_id":       post.ID,
		"author":        post.Author,
		"permlink":      post.Permlink,
		"category":      post.Category,
		"title":         cache.Title,
		"body":          body,
		"json_metadata": jsonMeta,
		"created":       post.CreatedAt.Format(time.RFC3339),
		"updated":       cache.UpdatedAt.Format(time.RFC3339),
		"depth":         post.Depth,
		"children":      cache.Children,
		"net_rshares":   cache.RShares,
		"is_paidout":    cache.IsPaidout,
		"payout_at":     cache.PayoutAt.Format(time.RFC3339),
		"payout":        cache.Payout,
		// Payout split mirrors legacy bridge _condenser_post_object: before
		// payout the whole amount is pending; after payout the author value is
		// recomputed from raw_json.curator_payout_value below.
		"pending_payout_value": formatAmount(pendingPayout),
		"author_payout_value":  formatAmount(authorPayout),
		"curator_payout_value": formatAmount(0),
		"promoted":             formatAmount(cache.Promoted),
		"replies":              []interface{}{},
		"active_votes":         votes,
		"author_reputation":    authorRep, // raw float, no repToRaw for bridge_api
		"stats": map[string]interface{}{
			"hide":        false, // display flag from Mutes/irredeemables list (TODO); DB-level hiding is enforced by get_discussion via hive_posts_status
			"gray":        cache.IsGrayed,
			"total_votes": cache.TotalVotes,
			"flag_weight": cache.FlagWeight,
		},
	}

	// Merge raw_json import fields if available.
	for k, v := range rawFields {
		obj[k] = v
	}

	// For paid posts, split the total payout between author and curators
	// using the curator payout recorded at payout time (raw_json).
	if cache.IsPaidout {
		if raw, ok := rawFields["curator_payout_value"]; ok {
			curator := sbdAmount(raw)
			obj["author_payout_value"] = formatAmount(cache.Payout - curator)
			obj["curator_payout_value"] = formatAmount(curator)
		}
	}

	// URL: prefer raw_json url, fall back to constructed.
	if _, ok := obj["url"]; !ok {
		obj["url"] = fmt.Sprintf("/%s/@%s/%s", post.Category, post.Author, post.Permlink)
	}

	// For comments (depth > 0), rewrite title to "RE: root_title".
	if post.Depth > 0 {
		if rootTitle, ok := rawFields["root_title"].(string); ok && rootTitle != "" {
			obj["title"] = "RE: " + rootTitle
		}
	}

	return obj
}

// hydrateBridgeVotes parses the CSV into 2-field vote objects (voter, rshares).
// Mirrors the bridge_api active_votes shape.
func hydrateBridgeVotes(voteCSV string) []interface{} {
	if voteCSV == "" {
		return []interface{}{}
	}
	votes := []interface{}{}
	for _, line := range splitNewlines(voteCSV) {
		parts := splitCommas(line)
		if len(parts) < 2 {
			continue
		}
		votes = append(votes, map[string]interface{}{
			"voter":   parts[0],
			"rshares": parts[1],
		})
	}
	return votes
}

// parseRawJSON extracts import fields from the raw_json column.
// Mirrors legacy raw_json usage in _condenser_post_object (bridge_api).
func parseRawJSON(rawJSON string) map[string]interface{} {
	result := map[string]interface{}{}
	if rawJSON == "" || len(rawJSON) < 33 {
		return result
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(rawJSON), &raw); err != nil {
		return result
	}

	// Beneficiaries.
	if v, ok := raw["beneficiaries"]; ok {
		result["beneficiaries"] = v
	}
	// Max accepted payout.
	if v, ok := raw["max_accepted_payout"]; ok {
		result["max_accepted_payout"] = v
	}
	// Percent steem dollars.
	if v, ok := raw["percent_steem_dollars"]; ok {
		result["percent_steem_dollars"] = v
	}
	// Parent info (for comments).
	if v, ok := raw["parent_author"]; ok {
		result["parent_author"] = v
	}
	if v, ok := raw["parent_permlink"]; ok {
		result["parent_permlink"] = v
	}
	// Root title.
	if v, ok := raw["root_title"]; ok {
		result["root_title"] = v
	}
	// URL.
	if v, ok := raw["url"]; ok {
		result["url"] = v
	}
	// Curator payout value (for paid posts).
	if v, ok := raw["curator_payout_value"]; ok {
		result["curator_payout_value"] = v
	}

	return result
}

// splitNewlines and splitCommas are thin wrappers over strings.Split to keep
// vote-parsing self-contained and testable.
func splitNewlines(s string) []string {
	return strings.Split(s, "\n")
}

func splitCommas(s string) []string {
	return strings.Split(s, ",")
}
