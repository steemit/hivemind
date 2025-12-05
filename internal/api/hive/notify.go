package hive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// NotifyAPI provides notification-related Hive API methods
type NotifyAPI struct {
	repo *db.Repository
}

// NewNotifyAPI creates a new notify API
func NewNotifyAPI(repo *db.Repository) *NotifyAPI {
	return &NotifyAPI{repo: repo}
}

// PostNotifications handles bridge.post_notifications
func (n *NotifyAPI) PostNotifications(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)

	if author == "" || permlink == "" {
		return nil, fmt.Errorf("missing required parameters: author, permlink")
	}

	minScore := int16(25)
	if ms, ok := pMap["min_score"].(float64); ok {
		minScore = int16(ms)
	}
	lastID := int64(0)
	if li, ok := pMap["last_id"].(float64); ok {
		lastID = int64(li)
	}
	limit := 100
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}

	// Get post ID
	postRepo := db.NewPostRepository(n.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil {
		return nil, err
	}
	if post == nil {
		return []interface{}{}, nil
	}

	// Query notifications
	notifRepo := db.NewNotificationRepository(n.repo)
	notifications, err := notifRepo.GetByPostID(ctx.Request.Context(), post.ID, minScore, lastID, limit)
	if err != nil {
		return nil, err
	}

	return n.renderNotifications(ctx.Request.Context(), notifications)
}

// AccountNotifications handles bridge.account_notifications
func (n *NotifyAPI) AccountNotifications(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	minScore := int16(25)
	if ms, ok := pMap["min_score"].(float64); ok {
		minScore = int16(ms)
	}
	lastID := int64(0)
	if li, ok := pMap["last_id"].(float64); ok {
		lastID = int64(li)
	}
	limit := 100
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
		if limit > 100 {
			limit = 100
		}
	}

	// Get account ID
	accountRepo := db.NewAccountRepository(n.repo)
	acc, err := accountRepo.GetByName(ctx.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return []interface{}{}, nil
	}

	// For community accounts (hive-*), query by community_id
	notifRepo := db.NewNotificationRepository(n.repo)
	var notifications []*models.Notification
	if len(account) >= 5 && account[:5] == "hive-" {
		// Get community ID (same as account ID for communities)
		notifications, err = notifRepo.GetByCommunityID(ctx.Request.Context(), acc.ID, minScore, lastID, limit)
		if err != nil {
			return nil, err
		}
	} else {
		// For regular accounts, query by dst_id
		notifications, err = notifRepo.GetByDstID(ctx.Request.Context(), acc.ID, minScore, lastID, limit)
		if err != nil {
			return nil, err
		}
	}

	return n.renderNotifications(ctx.Request.Context(), notifications)
}

// UnreadNotifications handles bridge.unread_notifications
func (n *NotifyAPI) UnreadNotifications(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	minScore := int16(25)
	if ms, ok := pMap["min_score"].(float64); ok {
		minScore = int16(ms)
	}

	// Get account
	accountRepo := db.NewAccountRepository(n.repo)
	acc, err := accountRepo.GetByName(ctx.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return map[string]interface{}{
			"lastread": "1970-01-01T00:00:00",
			"unread":   0,
		}, nil
	}

	// Count unread notifications
	notifRepo := db.NewNotificationRepository(n.repo)
	unread, err := notifRepo.CountUnread(ctx.Request.Context(), acc.ID, acc.LastreadAt, minScore)
	if err != nil {
		return nil, err
	}

	// Format lastread timestamp
	lastread := acc.LastreadAt.Format(time.RFC3339)
	if acc.LastreadAt.IsZero() {
		lastread = "1970-01-01T00:00:00"
	}

	return map[string]interface{}{
		"lastread": lastread,
		"unread":   unread,
	}, nil
}

// renderNotifications renders notifications with full details
func (n *NotifyAPI) renderNotifications(ctx context.Context, notifications []*models.Notification) ([]interface{}, error) {
	result := make([]interface{}, 0, len(notifications))

	accountRepo := db.NewAccountRepository(n.repo)
	postRepo := db.NewPostRepository(n.repo)
	communityRepo := db.NewCommunityRepository(n.repo)

	for _, notif := range notifications {
		// Load related entities
		var srcName, dstName, author, permlink, communityName, communityTitle string

		if notif.SrcID.Valid {
			src, err := accountRepo.GetByID(ctx, notif.SrcID.Int64)
			if err == nil && src != nil {
				srcName = src.Name
			}
		}

		if notif.DstID.Valid {
			dst, err := accountRepo.GetByID(ctx, notif.DstID.Int64)
			if err == nil && dst != nil {
				dstName = dst.Name
			}
		}

		if notif.PostID.Valid {
			post, err := postRepo.GetByID(ctx, notif.PostID.Int64)
			if err == nil && post != nil {
				author = post.Author
				permlink = post.Permlink
			}
		}

		if notif.CommunityID.Valid {
			comm, err := communityRepo.GetByID(ctx, notif.CommunityID.Int64)
			if err == nil && comm != nil {
				communityName = comm.Name
				communityTitle = comm.Title
			}
		}

		// Build notification object
		notifObj := map[string]interface{}{
			"id":    notif.ID,
			"type":  getNotifyTypeName(notif.Type),
			"score": notif.Score,
			"date":  notif.CreatedAt.Format(time.RFC3339),
			"msg":   renderMessage(notif.Type, srcName, dstName, author, permlink, communityTitle, notif.Payload),
			"url":   renderURL(author, permlink, communityName, srcName, dstName),
		}

		result = append(result, notifObj)
	}

	return result, nil
}

// Helper functions
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

func renderMessage(typeID int16, src, dst, author, permlink, communityTitle string, payload sql.NullString) string {
	payloadStr := "null"
	if payload.Valid {
		payloadStr = payload.String
	}

	msgTemplates := map[int16]string{
		models.NotifyTypeNewCommunity: "<dst> was created",
		models.NotifyTypeSetRole:      "<src> set <dst> <payload>",
		models.NotifyTypeSetProps:     "<src> set properties <payload>",
		models.NotifyTypeSetLabel:     "<src> label <dst> <payload>",
		models.NotifyTypeMutePost:     "<src> mute <post> - <payload>",
		models.NotifyTypeUnmutePost:   "<src> unmute <post> - <payload>",
		models.NotifyTypePinPost:      "<src> pin <post>",
		models.NotifyTypeUnpinPost:    "<src> unpin <post>",
		models.NotifyTypeFlagPost:     "<src> flag <post> - <payload>",
		models.NotifyTypeSubscribe:    "<src> subscribed to <comm>",
		models.NotifyTypeError:        "error: <payload>",
		models.NotifyTypeReblog:       "<src> resteemed your post",
		models.NotifyTypeFollow:       "<src> followed you",
		models.NotifyTypeReply:        "<src> replied to your post",
		models.NotifyTypeReplyComment: "<src> replied to your comment",
		models.NotifyTypeMention:      "<src> mentioned you",
		models.NotifyTypeVote:         "<src> voted on your post",
	}

	msg, ok := msgTemplates[typeID]
	if !ok {
		return "unknown notification"
	}

	// Special handling for vote notifications
	if typeID == models.NotifyTypeVote && payload.Valid && len(payloadStr) > 1 {
		// Parse amount from payload (format: "$0.123")
		if payloadStr[0] == '$' {
			amt := payloadStr[1:]
			if len(amt) > 0 {
				msg += " (<payload>)"
			}
		}
	}

	// Replace placeholders
	if dst != "" {
		msg = replaceAll(msg, "<dst>", "@"+dst)
	}
	if src != "" {
		msg = replaceAll(msg, "<src>", "@"+src)
	}
	if author != "" && permlink != "" {
		postURL := "@" + author + "/" + permlink
		msg = replaceAll(msg, "<post>", postURL)
	}
	if communityTitle != "" {
		msg = replaceAll(msg, "<comm>", communityTitle)
	}
	msg = replaceAll(msg, "<payload>", payloadStr)

	return msg
}

func renderURL(author, permlink, community, src, dst string) string {
	if permlink != "" && author != "" {
		return "@" + author + "/" + permlink
	}
	if community != "" {
		return "trending/" + community
	}
	if src != "" {
		return "@" + src
	}
	if dst != "" {
		return "@" + dst
	}
	return ""
}

func replaceAll(s, old, new string) string {
	result := s
	for {
		replaced := result
		if len(old) > 0 {
			replaced = ""
			start := 0
			for {
				idx := findSubstring(result, old, start)
				if idx == -1 {
					replaced += result[start:]
					break
				}
				replaced += result[start:idx] + new
				start = idx + len(old)
			}
		}
		if replaced == result {
			break
		}
		result = replaced
	}
	return result
}

func findSubstring(s, substr string, start int) int {
	if start >= len(s) {
		return -1
	}
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

