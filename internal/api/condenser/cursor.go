package condenser

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// Cursor provides cursor-based pagination queries
type Cursor struct {
	db *gorm.DB
}

// NewCursor creates a new cursor
func NewCursor(database *gorm.DB) *Cursor {
	return &Cursor{db: database}
}

// GetPostIDByAuthorPermlink gets post ID by author and permlink
func (c *Cursor) GetPostIDByAuthorPermlink(ctx context.Context, author, permlink string) (int64, error) {
	var post models.Post
	err := c.db.WithContext(ctx).
		Where("author = ? AND permlink = ?", author, permlink).
		Select("id").
		First(&post).Error
	
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, nil
		}
		return 0, err
	}
	return post.ID, nil
}

// GetPostIDsByQuery gets post IDs for a given query
func (c *Cursor) GetPostIDsByQuery(ctx context.Context, sort, startAuthor, startPermlink string, limit int, tag string) ([]int64, error) {
	// Validate sort type
	validSorts := map[string]bool{
		"trending":        true,
		"hot":             true,
		"created":         true,
		"promoted":        true,
		"payout":           true,
		"payout_comments": true,
	}
	if !validSorts[sort] {
		return nil, fmt.Errorf("invalid sort type: %s", sort)
	}

	// Build query based on sort type
	query := c.db.WithContext(ctx).
		Model(&models.PostCache{}).
		Select("post_id")

	// Apply filters based on sort type
	switch sort {
	case "trending":
		query = query.Where("is_paidout = ?", false).
			Order("sc_trend DESC")
	case "hot":
		query = query.Where("is_paidout = ?", false).
			Order("sc_hot DESC")
	case "created":
		query = query.Where("depth = ?", 0).
			Order("post_id DESC")
	case "promoted":
		query = query.Where("is_paidout = ? AND promoted > ?", false, 0).
			Order("promoted DESC")
	case "payout":
		query = query.Where("is_paidout = ? AND depth = ?", false, 0).
			Order("payout DESC")
	case "payout_comments":
		query = query.Where("is_paidout = ? AND depth > ?", false, 0).
			Order("payout DESC")
	}

	// Filter by tag if provided
	if tag != "" {
		if tag[:5] == "hive-" {
			// Community tag
			query = query.Where("category = ?", tag)
			if sort == "trending" || sort == "hot" {
				query = query.Where("depth = ?", 0)
			}
		} else {
			// Regular tag - join with hive_post_tags
			query = query.Joins("INNER JOIN hive_post_tags ON hive_posts_cache.post_id = hive_post_tags.post_id").
				Where("hive_post_tags.tag = ?", tag)
		}
	}

	// Handle pagination
	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, startAuthor, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []int64{}, nil
		}

		// Get the sort field value for the start post
		var startValue interface{}
		switch sort {
		case "trending":
			var cache models.PostCache
			if err := c.db.WithContext(ctx).Where("post_id = ?", startID).Select("sc_trend").First(&cache).Error; err == nil {
				startValue = cache.SCTrend
			}
		case "hot":
			var cache models.PostCache
			if err := c.db.WithContext(ctx).Where("post_id = ?", startID).Select("sc_hot").First(&cache).Error; err == nil {
				startValue = cache.SCHot
			}
		case "created", "payout", "payout_comments":
			startValue = startID
		}

		if startValue != nil {
			switch sort {
			case "trending":
				query = query.Where("sc_trend <= ?", startValue)
			case "hot":
				query = query.Where("sc_hot <= ?", startValue)
			case "created", "payout", "payout_comments":
				query = query.Where("post_id <= ?", startValue)
			}
		}
	}

	// Apply limit
	query = query.Limit(limit)

	// Execute query
	var results []struct {
		PostID int64 `gorm:"column:post_id"`
	}
	if err := query.Scan(&results).Error; err != nil {
		return nil, err
	}

	// Extract post IDs
	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.PostID
	}

	return ids, nil
}

// GetPostIDsByBlog gets post IDs for an account's blog
func (c *Cursor) GetPostIDsByBlog(ctx context.Context, account string, startAuthor, startPermlink string, limit int) ([]int64, error) {
	// Get account ID
	accountRepo := db.NewAccountRepository(db.NewRepository(c.db))
	acc, err := accountRepo.GetByName(ctx, account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return []int64{}, nil
	}

	query := c.db.WithContext(ctx).
		Model(&models.FeedCache{}).
		Select("post_id").
		Where("account_id = ?", acc.ID)

	// Handle pagination
	if startPermlink != "" {
		startID, err := c.GetPostIDByAuthorPermlink(ctx, startAuthor, startPermlink)
		if err != nil {
			return nil, err
		}
		if startID == 0 {
			return []int64{}, nil
		}

		// Get created_at for start post
		var startCache models.FeedCache
		if err := c.db.WithContext(ctx).
			Where("account_id = ? AND post_id = ?", acc.ID, startID).
			First(&startCache).Error; err == nil {
			query = query.Where("created_at <= ?", startCache.CreatedAt)
		}
	}

	query = query.Order("created_at DESC").Limit(limit)

	var results []struct {
		PostID int64 `gorm:"column:post_id"`
	}
	if err := query.Scan(&results).Error; err != nil {
		return nil, err
	}

	ids := make([]int64, len(results))
	for i, r := range results {
		ids[i] = r.PostID
	}

	return ids, nil
}

