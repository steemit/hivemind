package indexer

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// PostIndexer handles post indexing
type PostIndexer struct {
	repo          *db.Repository
	logger        *zap.Logger
	notifyIndexer *NotifyIndexer
}

// NewPostIndexer creates a new post indexer
func NewPostIndexer(repo *db.Repository, logger *zap.Logger) *PostIndexer {
	return &PostIndexer{
		repo:          repo,
		logger:        logger,
		notifyIndexer: NewNotifyIndexer(repo),
	}
}

// ProcessComment processes a comment operation (create or update post)
func (pi *PostIndexer) ProcessComment(ctx context.Context, tx *gorm.DB, op map[string]interface{}, blockDate time.Time) error {
	author, _ := op["author"].(string)
	permlink, _ := op["permlink"].(string)
	parentAuthor, _ := op["parent_author"].(string)
	parentPermlink, _ := op["parent_permlink"].(string)
	// title and body will be stored in post cache later
	_, _ = op["title"].(string), op["body"].(string)
	jsonMeta, _ := op["json_metadata"].(string)

	postRepo := db.NewPostRepository(pi.repo)

	// Check if post already exists
	existing, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
	if err != nil {
		return fmt.Errorf("failed to check post existence: %w", err)
	}

	if existing != nil {
		// Update existing post
		if existing.IsDeleted {
			// Undelete
			existing.IsDeleted = false
			existing.CreatedAt = blockDate
		} else {
			// Update content
			// Note: In Steem, posts can be edited, but we track the latest version
		}
		return tx.WithContext(ctx).Save(existing).Error
	}

	// Create new post
	post := &models.Post{
		Author:    author,
		Permlink:  permlink,
		Category:  extractCategory(jsonMeta),
		CreatedAt: blockDate,
		Depth:     0,
		IsDeleted: false,
		IsValid:   true,
	}

	// Set parent if this is a comment
	if parentAuthor != "" && parentPermlink != "" {
		parent, err := postRepo.GetByAuthorPermlink(ctx, parentAuthor, parentPermlink)
		if err == nil && parent != nil {
			post.ParentID = sql.NullInt64{Int64: parent.ID, Valid: true}
			post.Depth = parent.Depth + 1
			post.Category = parent.Category
			// Inherit community_id, is_valid, is_muted from parent
			post.CommunityID = parent.CommunityID
			post.IsValid = parent.IsValid
			post.IsMuted = parent.IsMuted
		}
	}

	// Determine community from category
	// TODO: Implement community detection logic

	if err := tx.WithContext(ctx).Create(post).Error; err != nil {
		return fmt.Errorf("failed to create post: %w", err)
	}

	// Check for post errors and send notification
	// TODO: Implement error detection logic (e.g., invalid JSON metadata, etc.)
	// For now, we skip error notifications during initial sync
	// if postError != "" {
	//     accountRepo := db.NewAccountRepository(pi.repo)
	//     account, err := accountRepo.GetByName(ctx, author)
	//     if err == nil && account != nil {
	//         postID := post.ID
	//         pi.notifyIndexer.Write(ctx, models.NotifyTypeError, blockDate, nil, &account.ID, nil, &postID, &postError, nil)
	//     }
	// }

	// Insert into feed cache if root post (depth=0)
	if post.Depth == 0 {
		accountRepo := db.NewAccountRepository(pi.repo)
		account, err := accountRepo.GetByName(ctx, author)
		if err == nil && account != nil {
			feedCache := &models.FeedCache{
				PostID:    post.ID,
				AccountID: account.ID,
				CreatedAt: blockDate,
			}
			tx.WithContext(ctx).Create(feedCache)
		}
	}

	pi.logger.Debug("Created post",
		zap.String("author", author),
		zap.String("permlink", permlink),
		zap.Int16("depth", post.Depth))

	return nil
}

// MarkPostDirty marks a post as needing cache update (e.g., after vote)
func (pi *PostIndexer) MarkPostDirty(ctx context.Context, tx *gorm.DB, author, permlink string) error {
	// This is a placeholder for future cache update logic
	// For now, we just log that the post needs updating
	// TODO: Implement proper cache dirty tracking
	pi.logger.Debug("Post marked dirty for cache update",
		zap.String("author", author),
		zap.String("permlink", permlink))
	return nil
}

// ProcessDelete processes a delete comment operation
func (pi *PostIndexer) ProcessDelete(ctx context.Context, tx *gorm.DB, op map[string]interface{}) error {
	author, _ := op["author"].(string)
	permlink, _ := op["permlink"].(string)

	postRepo := db.NewPostRepository(pi.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
	if err != nil {
		return fmt.Errorf("failed to get post: %w", err)
	}
	if post == nil {
		return nil // Post doesn't exist, nothing to delete
	}

	// Mark as deleted
	post.IsDeleted = true
	if err := tx.WithContext(ctx).Save(post).Error; err != nil {
		return fmt.Errorf("failed to delete post: %w", err)
	}

	// Remove from feed cache if root post
	if post.Depth == 0 {
		accountRepo := db.NewAccountRepository(pi.repo)
		account, err := accountRepo.GetByName(ctx, author)
		if err == nil && account != nil {
			tx.WithContext(ctx).Where("post_id = ? AND account_id = ?", post.ID, account.ID).
				Delete(&models.FeedCache{})
		}
	}

	pi.logger.Debug("Deleted post",
		zap.String("author", author),
		zap.String("permlink", permlink))

	return nil
}

// extractCategory extracts category from JSON metadata
// In Steem, category is typically the first tag in the tags array
func extractCategory(jsonMeta string) string {
	if jsonMeta == "" {
		return ""
	}

	// Parse JSON metadata
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(jsonMeta), &meta); err != nil {
		// Invalid JSON, return empty
		return ""
	}

	// Try to get tags array
	tags, ok := meta["tags"].([]interface{})
	if !ok {
		// Tags might be a single string (legacy format)
		if tagStr, ok := meta["tags"].(string); ok {
			// Split by space and take first
			parts := strings.Fields(tagStr)
			if len(parts) > 0 {
				return strings.ToLower(strings.Trim(parts[0], "# "))
			}
		}
		return ""
	}

	// Get first tag as category
	if len(tags) > 0 {
		if tag, ok := tags[0].(string); ok {
			// Clean up tag: remove # prefix, trim, lowercase, max 32 chars
			tag = strings.TrimPrefix(tag, "#")
			tag = strings.TrimSpace(tag)
			tag = strings.ToLower(tag)
			if len(tag) > 32 {
				tag = tag[:32]
			}
			return tag
		}
	}

	return ""
}
