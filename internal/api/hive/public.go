package hive

import (
	"encoding/json"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/steemit/hivemind/internal/api/condenser"
	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
)

// PublicAPI provides public Hive API methods
type PublicAPI struct {
	repo   *db.Repository
	cursor *condenser.Cursor
}

// NewPublicAPI creates a new public API
func NewPublicAPI(repo *db.Repository, database *db.DB) *PublicAPI {
	return &PublicAPI{
		repo:   repo,
		cursor: condenser.NewCursor(database.DB),
	}
}

// GetAccount handles hive_api.get_account
// Returns the full account object (non-lite), with observer follow context.
func (p *PublicAPI) GetAccount(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	name, _ := pMap["name"].(string)
	observer, _ := pMap["observer"].(string)

	if name == "" {
		return nil, apierrors.PublicError("name cannot be blank")
	}
	name, err := apierrors.ValidAccount(name, false)
	if err != nil {
		return nil, err
	}

	accounts, err := accountsByNames(ctx.Request.Context(), p.repo.DB(), []string{name}, observer, false)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, nil
	}
	return accounts[0], nil
}

// GetAccounts handles hive_api.get_accounts
// Returns lite account objects, with observer followed context.
func (p *PublicAPI) GetAccounts(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	namesInterface, ok := pMap["names"].([]interface{})
	if !ok || len(namesInterface) == 0 {
		return nil, apierrors.PublicError("names must be a non-empty list")
	}
	if len(namesInterface) >= 100 {
		return nil, apierrors.PublicError("too many accounts requested")
	}
	observer, _ := pMap["observer"].(string)

	names := make([]string, 0, len(namesInterface))
	for _, n := range namesInterface {
		name, _ := n.(string)
		if name == "" {
			continue
		}
		valid, err := apierrors.ValidAccount(name, false)
		if err != nil {
			return nil, err
		}
		names = append(names, valid)
	}

	return accountsByNames(ctx.Request.Context(), p.repo.DB(), names, observer, true)
}

// ListFollowers handles hive_api.list_followers
// Returns lite accounts following `account`.
func (p *PublicAPI) ListFollowers(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}
	account, err := apierrors.ValidAccount(account, false)
	if err != nil {
		return nil, err
	}
	start, _ := pMap["start"].(string)
	if start != "" {
		start, err = apierrors.ValidAccount(start, false)
		if err != nil {
			return nil, err
		}
	}
	observer, _ := pMap["observer"].(string)
	limit := validLimitParam(pMap, 50, 100)

	names, err := p.cursor.GetFollowerNames(ctx.Request.Context(), account, start, limit)
	if err != nil {
		return nil, err
	}
	return accountsByNames(ctx.Request.Context(), p.repo.DB(), names, observer, true)
}

// ListFollowing handles hive_api.list_following
// Returns lite accounts `account` follows.
func (p *PublicAPI) ListFollowing(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}
	account, err := apierrors.ValidAccount(account, false)
	if err != nil {
		return nil, err
	}
	start, _ := pMap["start"].(string)
	if start != "" {
		start, err = apierrors.ValidAccount(start, false)
		if err != nil {
			return nil, err
		}
	}
	observer, _ := pMap["observer"].(string)
	limit := validLimitParam(pMap, 50, 100)

	names, err := p.cursor.GetFollowingNames(ctx.Request.Context(), account, start, limit)
	if err != nil {
		return nil, err
	}
	return accountsByNames(ctx.Request.Context(), p.repo.DB(), names, observer, true)
}

// ListAllMuted handles hive_api.list_all_muted
// Returns names of all accounts muted by `account`.
func (p *PublicAPI) ListAllMuted(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}
	account, err := apierrors.ValidAccount(account, false)
	if err != nil {
		return nil, err
	}

	return p.cursor.GetMutedNames(ctx.Request.Context(), account)
}

// ListAccountBlog handles hive_api.list_account_blog
// Blog feed: the account's own posts plus reblogs.
func (p *PublicAPI) ListAccountBlog(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}
	account, err := apierrors.ValidAccount(account, false)
	if err != nil {
		return nil, err
	}
	observer, _ := pMap["observer"].(string)
	limit := validLimitParam(pMap, 10, 50)
	lastPost, _ := pMap["last_post"].(string)

	startAuthor, startPermlink := splitLastPost(lastPost)

	ids, err := p.cursor.GetPostIDsByBlog(ctx.Request.Context(), account, startAuthor, startPermlink, limit)
	if err != nil {
		return nil, err
	}
	return postsByID(ctx.Request.Context(), p.repo.DB(), ids, observer, true)
}

// ListAccountPosts handles hive_api.list_account_posts
// The account's own posts and comments (no reblogs).
func (p *PublicAPI) ListAccountPosts(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}
	account, err := apierrors.ValidAccount(account, false)
	if err != nil {
		return nil, err
	}
	observer, _ := pMap["observer"].(string)
	limit := validLimitParam(pMap, 10, 50)
	lastPost, _ := pMap["last_post"].(string)

	_, startPermlink := splitLastPost(lastPost)

	ids, err := p.cursor.GetPostIDsByAccountComments(ctx.Request.Context(), account, startPermlink, limit)
	if err != nil {
		return nil, err
	}
	return postsByID(ctx.Request.Context(), p.repo.DB(), ids, observer, true)
}

// ListAccountFeed handles hive_api.list_account_feed
// Posts and resteems from everyone `account` follows, with reblog attribution.
func (p *PublicAPI) ListAccountFeed(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}
	account, err := apierrors.ValidAccount(account, false)
	if err != nil {
		return nil, err
	}
	observer, _ := pMap["observer"].(string)
	limit := validLimitParam(pMap, 10, 50)
	lastPost, _ := pMap["last_post"].(string)

	startAuthor, startPermlink := splitLastPost(lastPost)

	entries, err := p.cursor.GetPostIDsByFeedWithReblog(ctx.Request.Context(), account, startAuthor, startPermlink, limit)
	if err != nil {
		return nil, err
	}

	ids := make([]int64, 0, len(entries))
	rebloggers := make(map[int64]string, len(entries))
	for _, e := range entries {
		ids = append(ids, e.PostID)
		rebloggers[e.PostID] = e.Accounts
	}

	result, err := postsByID(ctx.Request.Context(), p.repo.DB(), ids, observer, true)
	if err != nil {
		return nil, err
	}

	// Merge reblogged_by into the post objects (mirrors legacy list_account_feed).
	if posts, ok := result["posts"].([]map[string]interface{}); ok {
		for _, post := range posts {
			pid, _ := post["id"].(int64)
			csv, ok := rebloggers[pid]
			if !ok || csv == "" {
				continue
			}
			author, _ := post["author"].(string)
			seen := make(map[string]bool)
			rby := make([]string, 0, 4)
			for _, name := range strings.Split(csv, ",") {
				if name == "" || name == author || seen[name] {
					continue
				}
				seen[name] = true
				rby = append(rby, name)
			}
			if len(rby) > 0 {
				post["reblogged_by"] = rby
			}
		}
	}

	return result, nil
}

// splitLastPost splits an optional "author/permlink" cursor into its parts.
// Mirrors legacy split_url(allow_empty=True).
func splitLastPost(lastPost string) (string, string) {
	if lastPost == "" {
		return "", ""
	}
	parts := strings.SplitN(lastPost, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// validLimitParam extracts and clamps a limit from a params map.
func validLimitParam(pMap map[string]interface{}, def, ubound int) int {
	limit := def
	if l, ok := pMap["limit"].(float64); ok {
		limit = int(l)
	}
	if limit, err := apierrors.ValidLimit(limit, ubound); err == nil {
		return limit
	}
	return def
}
