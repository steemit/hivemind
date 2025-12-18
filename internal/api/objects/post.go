package objects

import (
	"context"
	"fmt"
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

	// Build result in order
	result := make([]map[string]interface{}, 0, len(ids))
	for _, id := range ids {
		var post *models.Post
		for i := range posts {
			if posts[i].ID == id {
				post = &posts[i]
				break
			}
		}
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

	jsonMetadata := cache.JSONMeta
	if jsonMetadata == "" {
		jsonMetadata = "{}"
	}

	postObj := map[string]interface{}{
		"id":                post.ID,
		"author":            account.Name,
		"permlink":          post.Permlink,
		"category":          post.Category,
		"title":             cache.Title,
		"body":              body,
		"json_metadata":     jsonMetadata,
		"created":           post.CreatedAt.Format(time.RFC3339),
		"last_update":       cache.UpdatedAt.Format(time.RFC3339),
		"depth":             post.Depth,
		"children":          cache.Children,
		"net_rshares":       cache.RShares,
		"url":               fmt.Sprintf("/%s/@%s/%s", post.Category, account.Name, post.Permlink),
		"active_votes":      []interface{}{}, // TODO: Load active votes from cache.Votes (format needs to be parsed)
		"replies":           []interface{}{},  // TODO: Load replies (requires querying child posts)
		"reblogged_by":      l.getRebloggedBy(ctx, post.ID), // Load reblogs
		"body_length":       len(cache.Body),
		"author_reputation": account.Reputation,
		"promoted":          post.Promoted,
		"payout":            cache.Payout,
		"pending_payout_value": cache.Payout, // TODO: Check if paid out
	}

	return postObj
}

// LoadPostsReblogs loads posts with reblog information
func (l *PostLoader) LoadPostsReblogs(ctx context.Context, idsWithReblogs [][]int64, truncateBody int) ([]map[string]interface{}, error) {
	// Extract all post IDs
	allIDs := make([]int64, 0, len(idsWithReblogs))
	idToReblog := make(map[int64]string) // post_id -> reblogger

	for _, pair := range idsWithReblogs {
		if len(pair) >= 2 {
			postID := pair[0]
			rebloggerID := pair[1]
			allIDs = append(allIDs, postID)

			// Get reblogger account name
			var account models.Account
			if err := l.db.WithContext(ctx).
				Where("id = ?", rebloggerID).
				Select("name").
				First(&account).Error; err == nil {
				idToReblog[postID] = account.Name
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
		postID := int64(posts[i]["id"].(int64))
		if reblogger, ok := idToReblog[postID]; ok {
			rebloggedBy, _ := posts[i]["reblogged_by"].([]interface{})
			posts[i]["reblogged_by"] = append(rebloggedBy, reblogger)
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

