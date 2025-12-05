package hive

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
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

	minScore := 25
	if ms, ok := pMap["min_score"].(float64); ok {
		minScore = int(ms)
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

	// TODO: Query notifications from hive_notifs
	_ = minScore
	_ = lastID
	_ = limit

	return []interface{}{}, nil
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

	minScore := 25
	if ms, ok := pMap["min_score"].(float64); ok {
		minScore = int(ms)
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

	// TODO: Query notifications from hive_notifs
	_ = minScore
	_ = lastID
	_ = limit

	return []interface{}{}, nil
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

	minScore := 25
	if ms, ok := pMap["min_score"].(float64); ok {
		minScore = int(ms)
	}

	// TODO: Query unread notification count
	_ = minScore

	return map[string]interface{}{
		"lastread": "",
		"unread":   0,
	}, nil
}

