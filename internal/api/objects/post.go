package objects

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/internal/steem"
)

// PostLoader loads complete post objects from database
type PostLoader struct {
	db *gorm.DB
}

// NewPostLoader creates a new post loader
func NewPostLoader(database *gorm.DB) *PostLoader {
	return &PostLoader{db: database}
}

// LoadPosts loads complete post objects by IDs
func (l *PostLoader) LoadPosts(ctx context.Context, ids []int64, truncateBody int) ([]map[string]interface{}, error) {
	if len(ids) == 0 {
		return []map[string]interface{}{}, nil
	}

	// Load posts from cache table
	var caches []models.PostCache
	if err := l.db.WithContext(ctx).
		Where("post_id IN ?", ids).
		Find(&caches).Error; err != nil {
		return nil, fmt.Errorf("failed to load post caches: %w", err)
	}

	// Create map for quick lookup
	cacheMap := make(map[int64]*models.PostCache)
	for i := range caches {
		cacheMap[caches[i].PostID] = &caches[i]
	}

	// Load post details
	var posts []models.Post
	if err := l.db.WithContext(ctx).
		Where("id IN ?", ids).
		Find(&posts).Error; err != nil {
		return nil, fmt.Errorf("failed to load posts: %w", err)
	}

	// Load accounts by name
	accountNames := make(map[string]bool)
	for _, post := range posts {
		accountNames[post.Author] = true
	}
	accountNameList := make([]string, 0, len(accountNames))
	for name := range accountNames {
		accountNameList = append(accountNameList, name)
	}

	var accounts []models.Account
	if len(accountNameList) > 0 {
		if err := l.db.WithContext(ctx).
			Where("name IN ?", accountNameList).
			Find(&accounts).Error; err != nil {
			return nil, fmt.Errorf("failed to load accounts: %w", err)
		}
	}

	accountMap := make(map[string]*models.Account)
	for i := range accounts {
		accountMap[accounts[i].Name] = &accounts[i]
	}

	// Build index maps for O(1) lookup (avoids O(n²) scan per ID).
	postMap := make(map[int64]*models.Post, len(posts))
	for i := range posts {
		postMap[posts[i].ID] = &posts[i]
	}

	// Build result in order
	result := make([]map[string]interface{}, 0, len(ids))
	// Snapshot the irredeemables list once per batch and strip muted voters
	// from active_votes (legacy load_posts_keyed, objects.py:60-66).
	muted := mutedAccountSet()
	for _, id := range ids {
		post := postMap[id]
		if post == nil {
			continue // Skip missing posts
		}

		cache := cacheMap[id]
		if cache == nil {
			continue // Skip posts without cache
		}

		account := accountMap[post.Author]
		if account == nil {
			continue // Skip posts without author
		}

		postObj := l.buildPostObject(ctx, post, cache, account, truncateBody)
		if len(muted) > 0 {
			postObj["active_votes"] = muteVotes(postObj["active_votes"].([]interface{}), muted)
		}
		result = append(result, postObj)
	}

	return result, nil
}

// buildPostObject builds a complete post object from post, cache, and account data
func (l *PostLoader) buildPostObject(ctx context.Context, post *models.Post, cache *models.PostCache, account *models.Account, truncateBody int) map[string]interface{} {
	body := cache.Body
	if truncateBody > 0 && len(body) > truncateBody {
		body = body[:truncateBody]
	}

	jsonMetadata := cache.JSON
	if jsonMetadata == "" {
		jsonMetadata = "{}"
	}

	// condenser#3424 mitigation: an empty category is surfaced as 'undefined'
	// (legacy condenser_api/objects.py:139-141). The mitigated value also
	// flows into parent_permlink for top-level posts, as in legacy.
	category := cache.Category
	if category == "" {
		category = "undefined"
	}

	// Import fields from the legacy steemd object stored in raw_json
	// (legacy objects.py:171-187). parseRawJSON tolerates a missing/short
	// raw_json where legacy asserts; absent keys surface as nil.
	rawFields := parseRawJSON(cache.RawJSON)

	// Payout split mirrors legacy _condenser_post_object (objects.py:157-163,
	// 189-192): before payout the whole amount is pending and the curator
	// payout is zero; after payout the total is net of the curator payout
	// recorded in raw_json at payout time.
	var totalPayout, pendingPayout, curatorPayout float64
	if cache.IsPaidout {
		totalPayout = cache.Payout
		if raw, ok := rawFields["curator_payout_value"]; ok {
			curatorPayout = sbdAmount(raw)
			totalPayout = cache.Payout - curatorPayout
		}
	} else {
		pendingPayout = cache.Payout
	}

	// Parent fields: comments read them from raw_json; top-level posts use
	// ''/category (legacy objects.py:176-181).
	parentAuthor := ""
	parentPermlink := category
	if post.Depth > 0 {
		if v, ok := rawFields["parent_author"].(string); ok {
			parentAuthor = v
		}
		if v, ok := rawFields["parent_permlink"].(string); ok {
			parentPermlink = v
		}
	}

	postObj := map[string]interface{}{
		"id":                post.ID,
		"author":            account.Name,
		"permlink":          post.Permlink,
		"category":          category,
		"title":             cache.Title,
		"body":              body,
		"json_metadata":     jsonMetadata,
		"created":           jsonDate(post.CreatedAt),
		"last_update":       jsonDate(cache.UpdatedAt),
		"depth":             post.Depth,
		"children":          cache.Children,
		"net_rshares":       cache.RShares,
		"url":               postURL(rawFields, post, account),
		"active_votes":      hydrateActiveVotes(cache.Votes),
		"replies":           []interface{}{},
		"reblogged_by":      l.getRebloggedBy(ctx, post.ID),
		"body_length":       len(cache.Body),
		"author_reputation": RepToRaw(cache.AuthorRep),
		"promoted":          formatAmount(cache.Promoted),
		"payout":            cache.Payout,
		// last_payout/cashout_time always carry a string: json_date renders
		// the legacy sentinel for the empty side (objects.py:159-160).
		"last_payout":           jsonDate(when(cache.IsPaidout, cache.PayoutAt)),
		"cashout_time":          jsonDate(when(!cache.IsPaidout, cache.PayoutAt)),
		"total_payout_value":    formatAmount(totalPayout),
		"curator_payout_value":  formatAmount(curatorPayout),
		"pending_payout_value":  formatAmount(pendingPayout),
		"total_votes":           cache.TotalVotes,
		"parent_author":         parentAuthor,
		"parent_permlink":       parentPermlink,
		"root_title":            rawFields["root_title"],
		"beneficiaries":         rawFields["beneficiaries"],
		"max_accepted_payout":   rawFields["max_accepted_payout"],
		"percent_steem_dollars": rawFields["percent_steem_dollars"],
	}

	return postObj
}

// legacyNullDate is the sentinel legacy json_date renders for missing
// timestamps (hive/server/common/helpers.py:52-55).
const legacyNullDate = "1969-12-31T23:59:59"

// jsonDate renders a timestamp the way legacy json_date does: 'T'-joined
// and timezone-less, with missing (zero) timestamps becoming the sentinel
// string instead of null. Mirrors hive/server/common/helpers.py:52.
func jsonDate(ts time.Time) string {
	if ts.IsZero() {
		return legacyNullDate
	}
	return ts.Format("2006-01-02T15:04:05")
}

// postURL mirrors legacy objects.py:183: the URL comes from the steemd
// raw_json (comment URLs point at the root post with a #/@author/permlink
// fragment); the constructed form is only a fallback for rows whose
// raw_json lacks the key.
func postURL(rawFields map[string]interface{}, post *models.Post, account *models.Account) string {
	if v, ok := rawFields["url"].(string); ok && v != "" {
		return v
	}
	return fmt.Sprintf("/%s/@%s/%s", post.Category, account.Name, post.Permlink)
}

// when mirrors the `payout_at if paid else None` expressions in legacy
// _condenser_post_object (objects.py:159-160): yields the timestamp, or the
// zero time jsonDate turns into the sentinel.
func when(cond bool, ts time.Time) time.Time {
	if cond {
		return ts
	}
	return time.Time{}
}

// mutedAccountSet snapshots the process-wide irredeemables list. Mirrors
// legacy load_posts_keyed's single Mutes.all() per batch (objects.py:60);
// returns nil when nothing is muted so vote filtering can be skipped.
func mutedAccountSet() map[string]bool {
	all := steem.SharedMutes().All()
	if len(all) == 0 {
		return nil
	}
	set := make(map[string]bool, len(all))
	for _, name := range all {
		set[name] = true
	}
	return set
}

// muteVotes removes votes cast by muted accounts. Mirrors legacy
// _mute_votes (condenser_api/objects.py:71-74): with an empty mute list
// the original votes are returned untouched.
func muteVotes(votes []interface{}, muted map[string]bool) []interface{} {
	if len(muted) == 0 {
		return votes
	}
	out := make([]interface{}, 0, len(votes))
	for _, v := range votes {
		vote, ok := v.(map[string]interface{})
		if !ok {
			out = append(out, v)
			continue
		}
		if voter, _ := vote["voter"].(string); muted[voter] {
			continue
		}
		out = append(out, v)
	}
	return out
}

// hydrateActiveVotes converts the minimal CSV representation in
// hive_posts_cache.votes into steemd-style vote objects.
// Format: one vote per line, fields: voter,rshares,percent,reputation
// Mirrors legacy _hydrate_active_votes (condenser_api/objects.py:207).
func hydrateActiveVotes(voteCSV string) []interface{} {
	if voteCSV == "" {
		return []interface{}{}
	}
	votes := []interface{}{}
	for _, line := range strings.Split(voteCSV, "\n") {
		parts := strings.Split(line, ",")
		if len(parts) != 4 {
			continue
		}
		votes = append(votes, map[string]interface{}{
			"voter":      parts[0],
			"rshares":    parts[1],
			"percent":    parts[2],
			"reputation": repToRawStr(parts[3]),
		})
	}
	return votes
}

// RepToRaw converts a UI-ready reputation score back into its approximate
// raw steemd value. Mirrors legacy rep_to_raw (utils/normalize.py:136).
// Callers that need legacy's integer form (e.g. get_account_reputations)
// truncate toward zero, like Python's int().
func RepToRaw(rep float64) float64 {
	if rep == 25 {
		return 0
	}
	rep = rep - 25
	rep = rep / 9
	sign := 1.0
	if rep < 0 {
		sign = -1
	}
	return sign * math.Pow(10, math.Abs(rep)+9)
}

// repToRawStr is the string-input variant used by hydrateActiveVotes.
// The reputation in the CSV is a UI-ready float stored as a string.
func repToRawStr(repStr string) float64 {
	rep, err := strconv.ParseFloat(repStr, 64)
	if err != nil {
		return 0
	}
	return RepToRaw(rep)
}

// formatAmount returns a steem-style amount string ("X.XXX SBD").
// Mirrors legacy _amount (condenser_api/objects.py:202).
func formatAmount(amount float64) string {
	return fmt.Sprintf("%.3f SBD", amount)
}

// getRebloggedBy gets account names that reblogged a post
func (l *PostLoader) getRebloggedBy(ctx context.Context, postID int64) []interface{} {
	// Create a temporary repository to query reblogs
	// Note: This is a workaround since we don't have direct access to repository
	// In a real implementation, PostLoader should have access to repository
	var reblogs []models.Reblog
	if err := l.db.WithContext(ctx).
		Where("post_id = ?", postID).
		Order("created_at DESC").
		Find(&reblogs).Error; err != nil {
		return []interface{}{}
	}

	result := make([]interface{}, 0, len(reblogs))
	for _, reblog := range reblogs {
		result = append(result, reblog.Account)
	}

	return result
}
