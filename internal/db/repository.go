package db

import (
	"context"
	"errors"

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

