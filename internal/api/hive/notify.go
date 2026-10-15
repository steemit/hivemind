package hive

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/api/objects"
	"github.com/steemit/hivemind/internal/apierrors"
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
		return nil, apierrors.PublicError("invalid parameters format")
	}

	author, _ := pMap["author"].(string)
	permlink, _ := pMap["permlink"].(string)

	if author == "" || permlink == "" {
		return nil, apierrors.PublicError("missing required parameters: author, permlink")
	}

	minScore := int32(25)
	if ms, ok := pMap["min_score"].(float64); ok {
		minScore = int32(ms)
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

	// Get post ID. Legacy get_post_id (hive_api/common.py) does NOT filter
	// is_deleted here — deleted-post notifications are excluded by the query
	// itself (COALESCE(hp.is_deleted, False) = False) — and asserts
	// 'post id not found' when the post does not resolve.
	postRepo := db.NewPostRepository(n.repo)
	post, err := postRepo.GetByAuthorPermlink(ctx.Request.Context(), author, permlink)
	if err != nil {
		return nil, err
	}
	if post == nil {
		return nil, apierrors.PublicError("post id not found")
	}

	// Query notifications (deleted-post notifications filtered by the repo).
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
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}

	minScore := int32(25)
	if ms, ok := pMap["min_score"].(float64); ok {
		minScore = int32(ms)
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

	// Get account ID. Legacy get_account_id asserts "account not found" for
	// unknown accounts (hive_api/common.py:36-40).
	accountRepo := db.NewAccountRepository(n.repo)
	acc, err := accountRepo.GetByName(ctx.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	// For community accounts (hive-*), query by community_id and force
	// min_score=0 (legacy account_notifications, notify.py).
	isCommunity := strings.HasPrefix(account, "hive-")
	if isCommunity {
		minScore = 0
	}

	notifRepo := db.NewNotificationRepository(n.repo)
	var notifications []*models.Notification
	if isCommunity {
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
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}

	minScore := int32(25)
	if ms, ok := pMap["min_score"].(float64); ok {
		minScore = int32(ms)
	}

	// Get account. Legacy get_account_id asserts "account not found" for
	// unknown accounts.
	accountRepo := db.NewAccountRepository(n.repo)
	acc, err := accountRepo.GetByName(ctx.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	// Count unread notifications. Note: legacy unread SQL has no
	// deleted-post filter and no hive-* min_score override.
	notifRepo := db.NewNotificationRepository(n.repo)
	unread, err := notifRepo.CountUnread(ctx.Request.Context(), acc.ID, acc.LastreadAt, minScore)
	if err != nil {
		return nil, err
	}

	// Legacy renders lastread as str(datetime): 'YYYY-MM-DD HH:MM:SS'
	// (space-separated, no timezone).
	lastread := acc.LastreadAt.Format("2006-01-02 15:04:05")
	if acc.LastreadAt.IsZero() {
		lastread = "1970-01-01 00:00:00"
	}

	return map[string]interface{}{
		"lastread": lastread,
		"unread":   unread,
	}, nil
}

// renderNotifications renders notifications with full details.
//
// Related entities (source/destination accounts, posts, communities) are
// batch-preloaded with one IN query per entity type instead of a per-row
// lookup, capping the query count regardless of notification volume
// (previously 4 queries per notification).
func (n *NotifyAPI) renderNotifications(ctx context.Context, notifications []*models.Notification) ([]interface{}, error) {
	result := make([]interface{}, 0, len(notifications))
	if len(notifications) == 0 {
		return result, nil
	}

	gdb := n.repo.DB()

	// Collect the referenced IDs.
	accountIDs := make(map[int64]bool)
	postIDs := make(map[int64]bool)
	communityIDs := make(map[int64]bool)
	for _, notif := range notifications {
		if notif.SrcID.Valid {
			accountIDs[notif.SrcID.Int64] = true
		}
		if notif.DstID.Valid {
			accountIDs[notif.DstID.Int64] = true
		}
		if notif.PostID.Valid {
			postIDs[notif.PostID.Int64] = true
		}
		if notif.CommunityID.Valid {
			communityIDs[notif.CommunityID.Int64] = true
		}
	}

	// Batch query 1: accounts.
	accountNames := make(map[int64]string, len(accountIDs))
	if len(accountIDs) > 0 {
		ids := int64SetToSlice(accountIDs)
		var accounts []models.Account
		if err := gdb.WithContext(ctx).Where("id IN ?", ids).
			Select("id", "name").Find(&accounts).Error; err != nil {
			return nil, err
		}
		for _, acc := range accounts {
			accountNames[acc.ID] = acc.Name
		}
	}

	// Batch query 2: posts (author/permlink).
	type postRef struct {
		Author   string
		Permlink string
	}
	postRefs := make(map[int64]postRef, len(postIDs))
	if len(postIDs) > 0 {
		ids := int64SetToSlice(postIDs)
		var posts []models.Post
		if err := gdb.WithContext(ctx).Where("id IN ?", ids).
			Select("id", "author", "permlink").Find(&posts).Error; err != nil {
			return nil, err
		}
		for _, post := range posts {
			postRefs[post.ID] = postRef{Author: post.Author, Permlink: post.Permlink}
		}
	}

	// Batch query 3: communities (name/title).
	type commRef struct {
		Name  string
		Title string
	}
	commRefs := make(map[int64]commRef, len(communityIDs))
	if len(communityIDs) > 0 {
		ids := int64SetToSlice(communityIDs)
		var comms []models.Community
		if err := gdb.WithContext(ctx).Where("id IN ?", ids).
			Select("id", "name", "title").Find(&comms).Error; err != nil {
			return nil, err
		}
		for _, comm := range comms {
			commRefs[comm.ID] = commRef{Name: comm.Name, Title: comm.Title}
		}
	}

	for _, notif := range notifications {
		var srcName, dstName, author, permlink, communityName, communityTitle string

		if notif.SrcID.Valid {
			srcName = accountNames[notif.SrcID.Int64]
		}
		if notif.DstID.Valid {
			dstName = accountNames[notif.DstID.Int64]
		}
		if notif.PostID.Valid {
			if ref, ok := postRefs[notif.PostID.Int64]; ok {
				author = ref.Author
				permlink = ref.Permlink
			}
		}
		if notif.CommunityID.Valid {
			if ref, ok := commRefs[notif.CommunityID.Int64]; ok {
				communityName = ref.Name
				communityTitle = ref.Title
			}
		}

		// Build notification object
		notifObj := map[string]interface{}{
			"id":    notif.ID,
			"type":  getNotifyTypeName(notif.Type),
			"score": notif.Score,
			// Legacy renders dates with json_date: 'T'-joined, no timezone
			// (hive_api/notify.py _render).
			"date": objects.JsonDate(notif.CreatedAt),
			"msg":  renderMessage(notif.Type, srcName, dstName, author, permlink, communityTitle, notif.Payload),
			"url":  renderURL(author, permlink, communityName, srcName, dstName),
		}

		result = append(result, notifObj)
	}

	return result, nil
}

// int64SetToSlice converts a set of int64 into a slice for IN clauses.
func int64SetToSlice(set map[int64]bool) []int64 {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	return ids
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

	// Special handling for vote notifications (legacy _render_msg, notify.py):
	// a "$x.yz" payload of at least $0.01 appends " (<payload>)" to the
	// message and re-formats the amount as "$%.2f"; below $0.01 (or a payload
	// that is not parseable as an amount) the message stays bare and the
	// payload is not inserted. Legacy crashes on an unparseable payload
	// (float()); Go treats it as the bare-message case instead.
	if typeID == models.NotifyTypeVote && payload.Valid && len(payloadStr) > 1 {
		if amt, perr := strconv.ParseFloat(payloadStr[1:], 64); perr == nil && amt >= 0.01 {
			msg += " (<payload>)"
			payloadStr = fmt.Sprintf("$%.2f", amt)
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
