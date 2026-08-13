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

	// Payout logic mirrors legacy condenser_api/objects.py:
	// - If paid out: total_payout_value = payout, pending = 0
	// - If not paid: total_payout_value = 0, pending = payout
	var totalPayout, pendingPayout float64
	if cache.IsPaidout {
		totalPayout = cache.Payout
		pendingPayout = 0
	} else {
		totalPayout = 0
		pendingPayout = cache.Payout
	}

	// cashout_time: nil if paid out, otherwise payout_at
	var cashoutTime interface{}
	if cache.IsPaidout {
		cashoutTime = nil
	} else {
		cashoutTime = cache.PayoutAt.Format(time.RFC3339)
	}

	postObj := map[string]interface{}{
		"id":                   post.ID,
		"author":               account.Name,
		"permlink":             post.Permlink,
		"category":             post.Category,
		"title":                cache.Title,
		"body":                 body,
		"json_metadata":        jsonMetadata,
		"created":              post.CreatedAt.Format(time.RFC3339),
		"last_update":          cache.UpdatedAt.Format(time.RFC3339),
		"depth":                post.Depth,
		"children":             cache.Children,
		"net_rshares":          cache.RShares,
		"url":                  fmt.Sprintf("/%s/@%s/%s", post.Category, account.Name, post.Permlink),
		"active_votes":         hydrateActiveVotes(cache.Votes),
		"replies":              []interface{}{},
		"reblogged_by":         l.getRebloggedBy(ctx, post.ID),
		"body_length":          len(cache.Body),
		"author_reputation":    repToRaw(cache.AuthorRep),
		"promoted":             formatAmount(cache.Promoted),
		"payout":               cache.Payout,
		"total_payout_value":   formatAmount(totalPayout),
		"curator_payout_value": formatAmount(0),
		"pending_payout_value": formatAmount(pendingPayout),
		"last_payout":          payoutDate(cache.IsPaidout, cache.PayoutAt),
		"cashout_time":         cashoutTime,
		"total_votes":          cache.TotalVotes,
	}

	return postObj
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

// repToRaw converts a UI-ready reputation score back into its approximate
// raw steemd value. Mirrors legacy rep_to_raw (utils/normalize.py:136).
func repToRaw(rep float64) float64 {
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
	return repToRaw(rep)
}

// formatAmount returns a steem-style amount string ("X.XXX SBD").
// Mirrors legacy _amount (condenser_api/objects.py:202).
func formatAmount(amount float64) string {
	return fmt.Sprintf("%.3f SBD", amount)
}

// payoutDate returns the payout_at timestamp if paid out, nil otherwise.
// Mirrors legacy json_date(row['payout_at'] if paid else None).
func payoutDate(isPaidout bool, payoutAt time.Time) interface{} {
	if !isPaidout {
		return nil
	}
	return payoutAt.Format(time.RFC3339)
}

// LoadPostsReblogs loads posts with reblog information
func (l *PostLoader) LoadPostsReblogs(ctx context.Context, idsWithReblogs [][]int64, truncateBody int) ([]map[string]interface{}, error) {
	// Extract all post IDs and collect reblogger account IDs for batch lookup
	allIDs := make([]int64, 0, len(idsWithReblogs))
	rebloggerIDs := make([]int64, 0, len(idsWithReblogs))
	idToRebloggerID := make(map[int64]int64) // post_id -> reblogger_id

	for _, pair := range idsWithReblogs {
		if len(pair) >= 2 {
			postID := pair[0]
			rebloggerID := pair[1]
			allIDs = append(allIDs, postID)
			rebloggerIDs = append(rebloggerIDs, rebloggerID)
			idToRebloggerID[postID] = rebloggerID
		}
	}

	// Batch-load all reblogger account names in ONE query (was N+1).
	idToName := make(map[int64]string)
	if len(rebloggerIDs) > 0 {
		var accounts []models.Account
		if err := l.db.WithContext(ctx).
			Where("id IN ?", rebloggerIDs).
			Select("id", "name").
			Find(&accounts).Error; err == nil {
			for _, acc := range accounts {
				idToName[acc.ID] = acc.Name
			}
		}
	}

	// Load posts normally
	posts, err := l.LoadPosts(ctx, allIDs, truncateBody)
	if err != nil {
		return nil, err
	}

	// Add reblog information
	for i := range posts {
		postID := posts[i]["id"].(int64)
		if rebloggerID, ok := idToRebloggerID[postID]; ok {
			if reblogger, ok := idToName[rebloggerID]; ok {
				rebloggedBy, _ := posts[i]["reblogged_by"].([]interface{})
				posts[i]["reblogged_by"] = append(rebloggedBy, reblogger)
			}
		}
	}

	return posts, nil
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
