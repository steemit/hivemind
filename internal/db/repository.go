package db

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"github.com/steemit/hivemind/internal/models"
)

// Repository provides database access methods
type Repository struct {
	db *gorm.DB
}

// NewRepository creates a new repository
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// AccountRepository provides account-related database operations
type AccountRepository struct {
	*Repository
}

// NewAccountRepository creates a new account repository
func NewAccountRepository(repo *Repository) *AccountRepository {
	return &AccountRepository{Repository: repo}
}

// GetByID retrieves an account by ID
func (r *AccountRepository) GetByID(ctx context.Context, id int64) (*models.Account, error) {
	var account models.Account
	if err := r.db.WithContext(ctx).First(&account, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &account, nil
}

// GetByName retrieves an account by name
func (r *AccountRepository) GetByName(ctx context.Context, name string) (*models.Account, error) {
	var account models.Account
	if err := r.db.WithContext(ctx).Where("name = ?", name).First(&account).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &account, nil
}

// Create creates a new account
func (r *AccountRepository) Create(ctx context.Context, account *models.Account) error {
	return r.db.WithContext(ctx).Create(account).Error
}

// Update updates an account
func (r *AccountRepository) Update(ctx context.Context, account *models.Account) error {
	return r.db.WithContext(ctx).Save(account).Error
}

// GetByNames retrieves multiple accounts by names
func (r *AccountRepository) GetByNames(ctx context.Context, names []string) ([]*models.Account, error) {
	var accounts []*models.Account
	if err := r.db.WithContext(ctx).Where("name IN ?", names).Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

// BlockRepository provides block-related database operations
type BlockRepository struct {
	*Repository
}

// NewBlockRepository creates a new block repository
func NewBlockRepository(repo *Repository) *BlockRepository {
	return &BlockRepository{Repository: repo}
}

// GetByNum retrieves a block by number
func (r *BlockRepository) GetByNum(ctx context.Context, num int64) (*models.Block, error) {
	var block models.Block
	if err := r.db.WithContext(ctx).First(&block, num).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &block, nil
}

// GetHead retrieves the head block (highest block number)
func (r *BlockRepository) GetHead(ctx context.Context) (*models.Block, error) {
	var block models.Block
	if err := r.db.WithContext(ctx).Order("num DESC").First(&block).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &block, nil
}

// Create creates a new block
func (r *BlockRepository) Create(ctx context.Context, block *models.Block) error {
	return r.db.WithContext(ctx).Create(block).Error
}

// PostRepository provides post-related database operations
type PostRepository struct {
	*Repository
}

// NewPostRepository creates a new post repository
func NewPostRepository(repo *Repository) *PostRepository {
	return &PostRepository{Repository: repo}
}

// GetByID retrieves a post by ID
func (r *PostRepository) GetByID(ctx context.Context, id int64) (*models.Post, error) {
	var post models.Post
	if err := r.db.WithContext(ctx).First(&post, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &post, nil
}

// GetByAuthorPermlink retrieves a post by author and permlink
func (r *PostRepository) GetByAuthorPermlink(ctx context.Context, author, permlink string) (*models.Post, error) {
	var post models.Post
	if err := r.db.WithContext(ctx).
		Where("author = ? AND permlink = ?", author, permlink).
		First(&post).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &post, nil
}

// Create creates a new post
func (r *PostRepository) Create(ctx context.Context, post *models.Post) error {
	return r.db.WithContext(ctx).Create(post).Error
}

// Update updates a post
func (r *PostRepository) Update(ctx context.Context, post *models.Post) error {
	return r.db.WithContext(ctx).Save(post).Error
}

// StateRepository provides state-related database operations
type StateRepository struct {
	*Repository
}

// NewStateRepository creates a new state repository
func NewStateRepository(repo *Repository) *StateRepository {
	return &StateRepository{Repository: repo}
}

// Get retrieves the current state
func (r *StateRepository) Get(ctx context.Context) (*models.State, error) {
	var state models.State
	if err := r.db.WithContext(ctx).First(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}

// Update updates the state
func (r *StateRepository) Update(ctx context.Context, state *models.State) error {
	return r.db.WithContext(ctx).Save(state).Error
}

// NotificationRepository provides notification-related database operations
type NotificationRepository struct {
	*Repository
}

// NewNotificationRepository creates a new notification repository
func NewNotificationRepository(repo *Repository) *NotificationRepository {
	return &NotificationRepository{Repository: repo}
}

// Create creates a new notification
func (r *NotificationRepository) Create(ctx context.Context, notification *models.Notification) error {
	return r.db.WithContext(ctx).Create(notification).Error
}

// GetByDstID retrieves notifications by destination account ID
func (r *NotificationRepository) GetByDstID(ctx context.Context, dstID int64, minScore int16, lastID int64, limit int) ([]*models.Notification, error) {
	var notifications []*models.Notification
	query := r.db.WithContext(ctx).
		Where("dst_id = ? AND score >= ?", dstID, minScore).
		Order("id DESC").
		Limit(limit)
	
	if lastID > 0 {
		query = query.Where("id < ?", lastID)
	}
	
	if err := query.Find(&notifications).Error; err != nil {
		return nil, err
	}
	return notifications, nil
}

// GetByCommunityID retrieves notifications by community ID
func (r *NotificationRepository) GetByCommunityID(ctx context.Context, communityID int64, minScore int16, lastID int64, limit int) ([]*models.Notification, error) {
	var notifications []*models.Notification
	query := r.db.WithContext(ctx).
		Where("community_id = ? AND score >= ?", communityID, minScore).
		Order("id DESC").
		Limit(limit)
	
	if lastID > 0 {
		query = query.Where("id < ?", lastID)
	}
	
	if err := query.Find(&notifications).Error; err != nil {
		return nil, err
	}
	return notifications, nil
}

// GetByPostID retrieves notifications by post ID
func (r *NotificationRepository) GetByPostID(ctx context.Context, postID int64, minScore int16, lastID int64, limit int) ([]*models.Notification, error) {
	var notifications []*models.Notification
	query := r.db.WithContext(ctx).
		Where("post_id = ? AND score >= ?", postID, minScore).
		Order("id DESC").
		Limit(limit)
	
	if lastID > 0 {
		query = query.Where("id < ?", lastID)
	}
	
	if err := query.Find(&notifications).Error; err != nil {
		return nil, err
	}
	return notifications, nil
}

// CountUnread counts unread notifications for an account
func (r *NotificationRepository) CountUnread(ctx context.Context, dstID int64, lastreadAt time.Time, minScore int16) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.Notification{}).
		Where("dst_id = ? AND score >= ? AND created_at > ?", dstID, minScore, lastreadAt).
		Count(&count).Error
	return count, err
}

// SetLastRead updates the lastread_at timestamp for an account
func (r *AccountRepository) SetLastRead(ctx context.Context, accountName string, date time.Time) error {
	return r.db.WithContext(ctx).
		Model(&models.Account{}).
		Where("name = ?", accountName).
		Update("lastread_at", date).Error
}

// CommunityRepository provides community-related database operations
type CommunityRepository struct {
	*Repository
}

// NewCommunityRepository creates a new community repository
func NewCommunityRepository(repo *Repository) *CommunityRepository {
	return &CommunityRepository{Repository: repo}
}

// GetByID retrieves a community by ID
func (r *CommunityRepository) GetByID(ctx context.Context, id int64) (*models.Community, error) {
	var community models.Community
	if err := r.db.WithContext(ctx).First(&community, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &community, nil
}

// GetByName retrieves a community by name
func (r *CommunityRepository) GetByName(ctx context.Context, name string) (*models.Community, error) {
	var community models.Community
	if err := r.db.WithContext(ctx).Where("name = ?", name).First(&community).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &community, nil
}

// FeedCacheRepository provides feed cache operations
type FeedCacheRepository struct {
	*Repository
}

// NewFeedCacheRepository creates a new feed cache repository
func NewFeedCacheRepository(repo *Repository) *FeedCacheRepository {
	return &FeedCacheRepository{Repository: repo}
}

// Insert inserts a post into feed cache
func (r *FeedCacheRepository) Insert(ctx context.Context, postID, accountID int64, createdAt time.Time) error {
	feedCache := &models.FeedCache{
		PostID:    postID,
		AccountID: accountID,
		CreatedAt: createdAt,
	}
	// Use ON CONFLICT DO NOTHING equivalent in GORM
	return r.db.WithContext(ctx).
		Where("post_id = ? AND account_id = ?", postID, accountID).
		FirstOrCreate(feedCache).Error
}

// Delete removes a post from feed cache
func (r *FeedCacheRepository) Delete(ctx context.Context, postID int64, accountID *int64) error {
	query := r.db.WithContext(ctx).Where("post_id = ?", postID)
	if accountID != nil {
		query = query.Where("account_id = ?", *accountID)
	}
	return query.Delete(&models.FeedCache{}).Error
}

// Rebuild rebuilds the entire feed cache
func (r *FeedCacheRepository) Rebuild(ctx context.Context, truncate bool) error {
	tx := r.db.WithContext(ctx).Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if truncate {
		if err := tx.Exec("TRUNCATE TABLE hive_feed_cache").Error; err != nil {
			tx.Rollback()
			return err
		}
	}

	// Insert all root posts (depth=0, not deleted) by their authors
	if err := tx.Exec(`
		INSERT INTO hive_feed_cache (account_id, post_id, created_at)
		SELECT hive_accounts.id, hive_posts.id, hive_posts.created_at
		FROM hive_posts
		JOIN hive_accounts ON hive_posts.author = hive_accounts.name
		WHERE depth = 0 AND is_deleted = false
		ON CONFLICT (post_id, account_id) DO NOTHING
	`).Error; err != nil {
		tx.Rollback()
		return err
	}

	// Insert all reblogs
	if err := tx.Exec(`
		INSERT INTO hive_feed_cache (account_id, post_id, created_at)
		SELECT hive_accounts.id, post_id, hive_reblogs.created_at
		FROM hive_reblogs
		JOIN hive_accounts ON hive_reblogs.account = hive_accounts.name
		ON CONFLICT (post_id, account_id) DO NOTHING
	`).Error; err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}

// ReblogRepository provides reblog-related database operations
type ReblogRepository struct {
	*Repository
}

// NewReblogRepository creates a new reblog repository
func NewReblogRepository(repo *Repository) *ReblogRepository {
	return &ReblogRepository{Repository: repo}
}

// Create creates a new reblog
func (r *ReblogRepository) Create(ctx context.Context, reblog *models.Reblog) error {
	return r.db.WithContext(ctx).Create(reblog).Error
}

// Delete deletes a reblog
func (r *ReblogRepository) Delete(ctx context.Context, account string, postID int64) error {
	return r.db.WithContext(ctx).
		Where("account = ? AND post_id = ?", account, postID).
		Delete(&models.Reblog{}).Error
}

// GetByPostID retrieves all reblogs for a post
func (r *ReblogRepository) GetByPostID(ctx context.Context, postID int64) ([]*models.Reblog, error) {
	var reblogs []*models.Reblog
	if err := r.db.WithContext(ctx).
		Where("post_id = ?", postID).
		Order("created_at DESC").
		Find(&reblogs).Error; err != nil {
		return nil, err
	}
	return reblogs, nil
}

// GetAccountNamesByPostID retrieves account names that reblogged a post
func (r *ReblogRepository) GetAccountNamesByPostID(ctx context.Context, postID int64) ([]string, error) {
	var accounts []string
	if err := r.db.WithContext(ctx).
		Model(&models.Reblog{}).
		Where("post_id = ?", postID).
		Pluck("account", &accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

// FollowRepository provides follow-related database operations
type FollowRepository struct {
	*Repository
}

// NewFollowRepository creates a new follow repository
func NewFollowRepository(repo *Repository) *FollowRepository {
	return &FollowRepository{Repository: repo}
}

// GetFollowers retrieves followers for an account
func (r *FollowRepository) GetFollowers(ctx context.Context, followingID int64, startFollowerID *int64, followType string, limit int) ([]*models.Follow, error) {
	var follows []*models.Follow
	query := r.db.WithContext(ctx).
		Where("following = ?", followingID)

	// Filter by follow type (state: 1=blog, 2=ignore, 3=both)
	if followType == "blog" {
		query = query.Where("state IN (1, 3)")
	} else if followType == "ignore" {
		query = query.Where("state IN (2, 3)")
	}

	if startFollowerID != nil {
		query = query.Where("follower < ?", *startFollowerID)
	}

	if err := query.
		Order("follower DESC").
		Limit(limit).
		Find(&follows).Error; err != nil {
		return nil, err
	}

	return follows, nil
}

// GetFollowing retrieves accounts followed by an account
func (r *FollowRepository) GetFollowing(ctx context.Context, followerID int64, startFollowingID *int64, followType string, limit int) ([]*models.Follow, error) {
	var follows []*models.Follow
	query := r.db.WithContext(ctx).
		Where("follower = ?", followerID)

	// Filter by follow type
	if followType == "blog" {
		query = query.Where("state IN (1, 3)")
	} else if followType == "ignore" {
		query = query.Where("state IN (2, 3)")
	}

	if startFollowingID != nil {
		query = query.Where("following < ?", *startFollowingID)
	}

	if err := query.
		Order("following DESC").
		Limit(limit).
		Find(&follows).Error; err != nil {
		return nil, err
	}

	return follows, nil
}

// GetByFollowerFollowing retrieves a follow relationship
func (r *FollowRepository) GetByFollowerFollowing(ctx context.Context, followerID, followingID int64) (*models.Follow, error) {
	var follow models.Follow
	if err := r.db.WithContext(ctx).
		Where("follower = ? AND following = ?", followerID, followingID).
		First(&follow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &follow, nil
}

// CreateOrUpdate creates or updates a follow relationship
func (r *FollowRepository) CreateOrUpdate(ctx context.Context, follow *models.Follow) error {
	return r.db.WithContext(ctx).
		Where("follower = ? AND following = ?", follow.Follower, follow.Following).
		Assign(*follow).
		FirstOrCreate(follow).Error
}

