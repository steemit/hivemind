package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

const (
	// START_BLOCK is the block number when community features started
	START_BLOCK = 37500000
)

// CommunityIndexer handles community-related indexing
type CommunityIndexer struct {
	repo          *db.Repository
	logger        *zap.Logger
	notifyIndexer *NotifyIndexer
}

// NewCommunityIndexer creates a new community indexer
func NewCommunityIndexer(repo *db.Repository, logger *zap.Logger) *CommunityIndexer {
	return &CommunityIndexer{
		repo:          repo,
		logger:        logger,
		notifyIndexer: NewNotifyIndexer(repo),
	}
}

// Register checks if newly registered accounts are communities and registers them
func (ci *CommunityIndexer) Register(ctx context.Context, tx *gorm.DB, accountNames []string, blockDate time.Time) error {
	// Community name pattern: hive-1xxxxx (where x is 4-6 digits)
	communityPattern := regexp.MustCompile(`^hive-[1]\d{4,6}$`)

	accountRepo := db.NewAccountRepository(ci.repo)

	for _, name := range accountNames {
		if !communityPattern.MatchString(name) {
			continue
		}

		// Get account ID
		account, err := accountRepo.GetByName(ctx, name)
		if err != nil {
			ci.logger.Warn("Failed to get account for community registration",
				zap.String("name", name),
				zap.Error(err))
			continue
		}
		if account == nil {
			continue
		}

		// Extract type_id from name (5th character)
		typeID := int16(name[5] - '0')

		// Create community
		community := &models.Community{
			ID:        account.ID,
			Name:      name,
			TypeID:    typeID,
			CreatedAt: blockDate,
		}

		if err := tx.WithContext(ctx).Create(community).Error; err != nil {
			// Community might already exist
			ci.logger.Debug("Community already exists or creation failed",
				zap.String("name", name),
				zap.Error(err))
			continue
		}

		// Create owner role
		role := &models.Role{
			CommunityID: account.ID,
			AccountID:   account.ID,
			Role:        models.RoleOwner,
			CreatedAt:   blockDate,
		}

		if err := tx.WithContext(ctx).Create(role).Error; err != nil {
			ci.logger.Warn("Failed to create owner role",
				zap.String("community", name),
				zap.Error(err))
		}

		// Send new_community notification
		communityID := account.ID
		ci.notifyIndexer.Write(ctx, models.NotifyTypeNewCommunity, blockDate, nil, &communityID, &communityID, nil, nil, nil)

		ci.logger.Info("Registered new community",
			zap.String("name", name),
			zap.Int64("id", account.ID))
	}

	return nil
}

// ProcessCommunityOp processes a community custom_json operation
func (ci *CommunityIndexer) ProcessCommunityOp(ctx context.Context, tx *gorm.DB, actor string, opJSON string, blockDate time.Time) error {
	// Parse JSON
	var op map[string]interface{}
	if err := json.Unmarshal([]byte(opJSON), &op); err != nil {
		return fmt.Errorf("failed to parse community op JSON: %w", err)
	}

	// Get action
	action, ok := op["type"].(string)
	if !ok {
		return fmt.Errorf("missing action type in community op")
	}

	// Get community name
	communityName, ok := op["community"].(string)
	if !ok {
		return fmt.Errorf("missing community in community op")
	}

	// Get community ID
	communityID, err := ci.getCommunityID(ctx, tx, communityName)
	if err != nil {
		return fmt.Errorf("failed to get community ID: %w", err)
	}
	if communityID == 0 {
		return fmt.Errorf("community not found: %s", communityName)
	}

	// Get actor ID
	accountRepo := db.NewAccountRepository(ci.repo)
	actorAccount, err := accountRepo.GetByName(ctx, actor)
	if err != nil || actorAccount == nil {
		return fmt.Errorf("actor account not found: %s", actor)
	}

	// Process based on action
	switch action {
	case "subscribe":
		return ci.processSubscribe(ctx, tx, actorAccount.ID, communityID, blockDate)
	case "unsubscribe":
		return ci.processUnsubscribe(ctx, tx, actorAccount.ID, communityID)
	case "setRole":
		return ci.processSetRole(ctx, tx, op, actorAccount.ID, communityID, blockDate)
	case "setUserTitle":
		return ci.processSetUserTitle(ctx, tx, op, actorAccount.ID, communityID, blockDate)
	case "updateProps":
		return ci.processUpdateProps(ctx, tx, op, actorAccount.ID, communityID, blockDate)
	case "mutePost", "unmutePost", "pinPost", "unpinPost", "flagPost":
		return ci.processPostAction(ctx, tx, action, op, actorAccount.ID, communityID, blockDate)
	default:
		ci.logger.Warn("Unknown community action",
			zap.String("action", action),
			zap.String("community", communityName))
		return nil
	}
}

// getCommunityID gets community ID by name
func (ci *CommunityIndexer) getCommunityID(ctx context.Context, tx *gorm.DB, name string) (int64, error) {
	var community models.Community
	err := tx.WithContext(ctx).
		Where("name = ?", name).
		Select("id").
		First(&community).Error
	if err != nil {
		return 0, err
	}
	return community.ID, nil
}

// processSubscribe handles subscribe action
func (ci *CommunityIndexer) processSubscribe(ctx context.Context, tx *gorm.DB, accountID, communityID int64, blockDate time.Time) error {
	subscription := &models.Subscription{
		AccountID:   accountID,
		CommunityID: communityID,
		CreatedAt:   blockDate,
	}

	// Use FirstOrCreate to handle duplicates
	if err := tx.WithContext(ctx).
		Where("account_id = ? AND community_id = ?", accountID, communityID).
		FirstOrCreate(subscription).Error; err != nil {
		return fmt.Errorf("failed to create subscription: %w", err)
	}

	// Update community subscriber count
	if err := tx.WithContext(ctx).
		Model(&models.Community{}).
		Where("id = ?", communityID).
		Update("subscribers", gorm.Expr("subscribers + 1")).Error; err != nil {
		ci.logger.Warn("Failed to update subscriber count", zap.Error(err))
	}

	// Send subscribe notification (to community)
	ci.notifyIndexer.Write(ctx, models.NotifyTypeSubscribe, blockDate, &accountID, nil, &communityID, nil, nil, nil)

	return nil
}

// processUnsubscribe handles unsubscribe action
func (ci *CommunityIndexer) processUnsubscribe(ctx context.Context, tx *gorm.DB, accountID, communityID int64) error {
	// Delete subscription
	if err := tx.WithContext(ctx).
		Where("account_id = ? AND community_id = ?", accountID, communityID).
		Delete(&models.Subscription{}).Error; err != nil {
		return fmt.Errorf("failed to delete subscription: %w", err)
	}

	// Update community subscriber count
	if err := tx.WithContext(ctx).
		Model(&models.Community{}).
		Where("id = ?", communityID).
		Update("subscribers", gorm.Expr("GREATEST(0, subscribers - 1)")).Error; err != nil {
		ci.logger.Warn("Failed to update subscriber count", zap.Error(err))
	}

	return nil
}

// processSetRole handles setRole action
func (ci *CommunityIndexer) processSetRole(ctx context.Context, tx *gorm.DB, op map[string]interface{}, actorID, communityID int64, blockDate time.Time) error {
	accountName, _ := op["account"].(string)
	roleName, _ := op["role"].(string)

	if accountName == "" || roleName == "" {
		return fmt.Errorf("missing account or role in setRole op")
	}

	// Get account ID
	accountRepo := db.NewAccountRepository(ci.repo)
	account, err := accountRepo.GetByName(ctx, accountName)
	if err != nil || account == nil {
		return fmt.Errorf("account not found: %s", accountName)
	}

	// Map role name to role ID
	roleID := ci.roleNameToID(roleName)

	// Create or update role
	role := &models.Role{
		CommunityID: communityID,
		AccountID:   account.ID,
		Role:        roleID,
		CreatedAt:   blockDate,
	}

	if err := tx.WithContext(ctx).
		Where("community_id = ? AND account_id = ?", communityID, account.ID).
		Assign(models.Role{Role: roleID, CreatedAt: blockDate}).
		FirstOrCreate(role).Error; err != nil {
		return fmt.Errorf("failed to set role: %w", err)
	}

	// Send set_role notification
	payload := roleName
	ci.notifyIndexer.Write(ctx, models.NotifyTypeSetRole, blockDate, &actorID, &account.ID, &communityID, nil, &payload, nil)

	return nil
}

// processSetUserTitle handles setUserTitle action
func (ci *CommunityIndexer) processSetUserTitle(ctx context.Context, tx *gorm.DB, op map[string]interface{}, actorID, communityID int64, blockDate time.Time) error {
	accountName, _ := op["account"].(string)
	title, _ := op["title"].(string)

	if accountName == "" {
		return fmt.Errorf("missing account in setUserTitle op")
	}

	// Get account ID
	accountRepo := db.NewAccountRepository(ci.repo)
	account, err := accountRepo.GetByName(ctx, accountName)
	if err != nil || account == nil {
		return fmt.Errorf("account not found: %s", accountName)
	}

	// Update role title
	if err := tx.WithContext(ctx).
		Model(&models.Role{}).
		Where("community_id = ? AND account_id = ?", communityID, account.ID).
		Update("title", title).Error; err != nil {
		return fmt.Errorf("failed to set user title: %w", err)
	}

	// Send set_label notification
	payload := title
	ci.notifyIndexer.Write(ctx, models.NotifyTypeSetLabel, blockDate, &actorID, &account.ID, &communityID, nil, &payload, nil)

	return nil
}

// processUpdateProps handles updateProps action
func (ci *CommunityIndexer) processUpdateProps(ctx context.Context, tx *gorm.DB, op map[string]interface{}, actorID, communityID int64, blockDate time.Time) error {
	props, ok := op["props"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("missing props in updateProps op")
	}

	// Update community properties
	updates := make(map[string]interface{})
	for key, value := range props {
		updates[key] = value
	}

	if len(updates) > 0 {
		if err := tx.WithContext(ctx).
			Model(&models.Community{}).
			Where("id = ?", communityID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("failed to update community props: %w", err)
		}
	}

	// Send set_props notification
	propsJSON, _ := json.Marshal(props)
	payload := string(propsJSON)
	ci.notifyIndexer.Write(ctx, models.NotifyTypeSetProps, blockDate, &actorID, nil, &communityID, nil, &payload, nil)

	return nil
}

// processPostAction handles post-related actions (mute, unmute, pin, unpin, flag)
func (ci *CommunityIndexer) processPostAction(ctx context.Context, tx *gorm.DB, action string, op map[string]interface{}, actorID, communityID int64, blockDate time.Time) error {
	author, _ := op["account"].(string)
	permlink, _ := op["permlink"].(string)

	if author == "" || permlink == "" {
		return fmt.Errorf("missing account or permlink in %s op", action)
	}

	// Get post ID
	postRepo := db.NewPostRepository(ci.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx, author, permlink)
	if err != nil || post == nil {
		return fmt.Errorf("post not found: %s/%s", author, permlink)
	}

	// Get notes from op (if any)
	notes, _ := op["notes"].(string)
	var payload *string
	if notes != "" {
		payload = &notes
	}

	// Update post status based on action and send notification
	switch action {
	case "mutePost":
		if err := tx.WithContext(ctx).
			Model(&models.Post{}).
			Where("id = ?", post.ID).
			Update("is_muted", true).Error; err != nil {
			return fmt.Errorf("failed to mute post: %w", err)
		}
		postID := post.ID
		ci.notifyIndexer.Write(ctx, models.NotifyTypeMutePost, blockDate, &actorID, nil, &communityID, &postID, payload, nil)
	case "unmutePost":
		if err := tx.WithContext(ctx).
			Model(&models.Post{}).
			Where("id = ?", post.ID).
			Update("is_muted", false).Error; err != nil {
			return fmt.Errorf("failed to unmute post: %w", err)
		}
		postID := post.ID
		ci.notifyIndexer.Write(ctx, models.NotifyTypeUnmutePost, blockDate, &actorID, nil, &communityID, &postID, payload, nil)
	case "pinPost":
		if err := tx.WithContext(ctx).
			Model(&models.Post{}).
			Where("id = ?", post.ID).
			Update("is_pinned", true).Error; err != nil {
			return fmt.Errorf("failed to pin post: %w", err)
		}
		postID := post.ID
		ci.notifyIndexer.Write(ctx, models.NotifyTypePinPost, blockDate, &actorID, nil, &communityID, &postID, payload, nil)
	case "unpinPost":
		if err := tx.WithContext(ctx).
			Model(&models.Post{}).
			Where("id = ?", post.ID).
			Update("is_pinned", false).Error; err != nil {
			return fmt.Errorf("failed to unpin post: %w", err)
		}
		postID := post.ID
		ci.notifyIndexer.Write(ctx, models.NotifyTypeUnpinPost, blockDate, &actorID, nil, &communityID, &postID, payload, nil)
	case "flagPost":
		// Flagging might require additional logic
		ci.logger.Debug("Flag post action", zap.Int64("post_id", post.ID))
		postID := post.ID
		ci.notifyIndexer.Write(ctx, models.NotifyTypeFlagPost, blockDate, &actorID, nil, &communityID, &postID, payload, nil)
	}

	return nil
}

// roleNameToID converts role name to role ID
func (ci *CommunityIndexer) roleNameToID(roleName string) int16 {
	switch roleName {
	case "muted":
		return models.RoleMuted
	case "guest":
		return models.RoleGuest
	case "member":
		return models.RoleMember
	case "mod":
		return models.RoleMod
	case "admin":
		return models.RoleAdmin
	case "owner":
		return models.RoleOwner
	default:
		return models.RoleGuest
	}
}

