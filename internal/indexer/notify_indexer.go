package indexer

import (
	"context"
	"database/sql"
	"time"

	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
	"github.com/steemit/hivemind/pkg/logging"
)

// NotifyIndexer handles notification creation and management
type NotifyIndexer struct {
	repo *db.Repository
}

// NewNotifyIndexer creates a new notification indexer
func NewNotifyIndexer(repo *db.Repository) *NotifyIndexer {
	return &NotifyIndexer{repo: repo}
}

// DefaultScore is the default notification score
const DefaultScore int16 = 35

// Write creates a new notification
func (n *NotifyIndexer) Write(ctx context.Context, typeID int16, when time.Time, srcID, dstID *int64, communityID, postID *int64, payload *string, score *int16) error {
	notif := &models.Notification{
		Type:      typeID,
		CreatedAt: when,
		Score:     DefaultScore,
	}

	if score != nil {
		notif.Score = *score
	}

	if srcID != nil {
		notif.SrcID = sql.NullInt64{Int64: *srcID, Valid: true}
	}

	if dstID != nil {
		notif.DstID = sql.NullInt64{Int64: *dstID, Valid: true}
	}

	if communityID != nil {
		notif.CommunityID = sql.NullInt64{Int64: *communityID, Valid: true}
	}

	if postID != nil {
		notif.PostID = sql.NullInt64{Int64: *postID, Valid: true}
	}

	if payload != nil {
		notif.Payload = sql.NullString{String: *payload, Valid: true}
	}

	// Log certain notification types
	ignoreTypes := map[int16]bool{
		models.NotifyTypeReply:        true,
		models.NotifyTypeReplyComment: true,
		models.NotifyTypeReblog:       true,
		models.NotifyTypeFollow:       true,
		models.NotifyTypeMention:      true,
		models.NotifyTypeVote:         true,
	}

	if !ignoreTypes[typeID] {
		logging.GetLogger().Info("[NOTIFY]",
			zap.String("type", getNotifyTypeName(typeID)),
			zap.Int64("src_id", getInt64(srcID)),
			zap.Int64("dst_id", getInt64(dstID)),
			zap.Int64("post_id", getInt64(postID)),
			zap.Int64("community_id", getInt64(communityID)),
			zap.String("payload", getString(payload)),
			zap.Int16("score", notif.Score))
	}

	notifRepo := db.NewNotificationRepository(n.repo)
	return notifRepo.Create(ctx, notif)
}

// SetLastRead updates the lastread_at timestamp for an account
func (n *NotifyIndexer) SetLastRead(ctx context.Context, accountName string, date time.Time) error {
	accountRepo := db.NewAccountRepository(n.repo)
	return accountRepo.SetLastRead(ctx, accountName, date)
}

// Helper functions
func getInt64(ptr *int64) int64 {
	if ptr == nil {
		return 0
	}
	return *ptr
}

func getString(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return *ptr
}

func getNotifyTypeName(typeID int16) string {
	names := map[int16]string{
		models.NotifyTypeNewCommunity: "new_community",
		models.NotifyTypeSetRole:      "set_role",
		models.NotifyTypeSetProps:     "set_props",
		models.NotifyTypeSetLabel:     "set_label",
		models.NotifyTypeMutePost:     "mute_post",
		models.NotifyTypeUnmutePost:   "unmute_post",
		models.NotifyTypePinPost:      "pin_post",
		models.NotifyTypeUnpinPost:    "unpin_post",
		models.NotifyTypeFlagPost:     "flag_post",
		models.NotifyTypeError:        "error",
		models.NotifyTypeSubscribe:    "subscribe",
		models.NotifyTypeReply:        "reply",
		models.NotifyTypeReplyComment: "reply_comment",
		models.NotifyTypeReblog:       "reblog",
		models.NotifyTypeFollow:       "follow",
		models.NotifyTypeMention:      "mention",
		models.NotifyTypeVote:         "vote",
	}
	if name, ok := names[typeID]; ok {
		return name
	}
	return "unknown"
}

