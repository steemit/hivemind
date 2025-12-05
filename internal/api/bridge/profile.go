package bridge

import (
	"encoding/json"
	"fmt"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/db"
)

// ProfileAPI provides profile-related Bridge API methods
type ProfileAPI struct {
	repo *db.Repository
}

// NewProfileAPI creates a new profile API
func NewProfileAPI(repo *db.Repository) *ProfileAPI {
	return &ProfileAPI{repo: repo}
}

// GetProfile handles bridge.get_profile
func (pr *ProfileAPI) GetProfile(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, fmt.Errorf("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	observer, _ := pMap["observer"].(string)

	if account == "" {
		return nil, fmt.Errorf("missing required parameter: account")
	}

	accountRepo := db.NewAccountRepository(pr.repo)
	acc, err := accountRepo.GetByName(ctx.Request.Context(), account)
	if err != nil {
		return nil, err
	}
	if acc == nil {
		return nil, nil
	}

	// TODO: Build full profile object with observer context
	_ = observer

	return map[string]interface{}{
		"id":          acc.ID,
		"name":        acc.Name,
		"display_name": acc.DisplayName.String,
		"about":       acc.About.String,
		"location":    acc.Location.String,
		"website":     acc.Website.String,
		"profile_image": acc.ProfileImage,
		"cover_image":   acc.CoverImage,
		"followers":    acc.Followers,
		"following":    acc.Following,
		"reputation":   acc.Reputation,
	}, nil
}

