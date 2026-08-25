package hive

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// CommunityAPI provides community-related Hive API methods
type CommunityAPI struct {
	repo                 *db.Repository
	recommendCommunities []string
}

// NewCommunityAPI creates a new community API. recommendCommunities is the
// comma-separated config list (HIVE_RECOMMEND_COMMUNITIES) prepended by
// list_top_communities, mirroring legacy conf recommend_communities.
func NewCommunityAPI(repo *db.Repository, recommendCommunities string) *CommunityAPI {
	var recommended []string
	for _, name := range strings.Split(recommendCommunities, ",") {
		if name = strings.TrimSpace(name); name != "" {
			recommended = append(recommended, name)
		}
	}
	return &CommunityAPI{repo: repo, recommendCommunities: recommended}
}

// getCommunityID resolves a community name (hive-xxxxx) to its id.
// Returns 0 when not found. Mirrors legacy get_community_id.
func (c *CommunityAPI) getCommunityID(ctx context.Context, name string) (int64, error) {
	if name == "" {
		return 0, apierrors.PublicError("no comm name specified")
	}
	var comm models.Community
	err := c.repo.DB().WithContext(ctx).
		Where("name = ?", name).
		Select("id").
		First(&comm).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, nil
		}
		return 0, err
	}
	return comm.ID, nil
}

// GetCommunity handles bridge.get_community
// Full community object: metadata, leadership team, and (with observer)
// subscription status, title and role.
func (c *CommunityAPI) GetCommunity(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	name, _ := pMap["name"].(string)
	observer, _ := pMap["observer"].(string)
	if name == "" {
		return nil, apierrors.PublicError("missing required parameter: name")
	}

	cid, err := c.getCommunityID(ctx.Request.Context(), name)
	if err != nil {
		return nil, err
	}
	if cid == 0 {
		return nil, apierrors.PublicError("community not found")
	}

	communities, err := c.loadCommunities(ctx.Request.Context(), []int64{cid}, false)
	if err != nil {
		return nil, err
	}

	comm := communities[cid]
	if comm == nil {
		return nil, apierrors.PublicError("community not found")
	}

	if observer != "" {
		observerID, err := c.getAccountID(ctx.Request.Context(), observer)
		if err != nil {
			return nil, err
		}
		if observerID != 0 {
			if err := c.appendObserverRoles(ctx.Request.Context(), communities, observerID); err != nil {
				return nil, err
			}
			if err := c.appendObserverSubs(ctx.Request.Context(), communities, observerID); err != nil {
				return nil, err
			}
		}
	}

	return comm, nil
}

// GetCommunityContext handles bridge.get_community_context
// For a community/account pair: returns role, title, subscribed state.
func (c *CommunityAPI) GetCommunityContext(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	name, _ := pMap["name"].(string)
	account, _ := pMap["account"].(string)
	if name == "" || account == "" {
		return nil, apierrors.PublicError("missing required parameters: name, account")
	}

	dbCtx := ctx.Request.Context()
	cid, err := c.getCommunityID(dbCtx, name)
	if err != nil {
		return nil, err
	}
	if cid == 0 {
		return nil, apierrors.PublicError("community not found")
	}
	aid, err := c.getAccountID(dbCtx, account)
	if err != nil {
		return nil, err
	}
	if aid == 0 {
		return nil, apierrors.PublicError("account not found")
	}

	var role struct {
		RoleID int16  `gorm:"column:role_id"`
		Title  string `gorm:"column:title"`
	}
	err = c.repo.DB().WithContext(dbCtx).Raw(
		`SELECT role_id, title FROM hive_roles
		  WHERE account_id = ? AND community_id = ?`, aid, cid,
	).Scan(&role).Error
	if err != nil {
		return nil, err
	}

	var subscribed bool
	err = c.repo.DB().WithContext(dbCtx).Raw(
		`SELECT EXISTS(
		    SELECT 1 FROM hive_subscriptions
		     WHERE account_id = ? AND community_id = ?)`, aid, cid,
	).Scan(&subscribed).Error
	if err != nil {
		return nil, err
	}

	roleName := roleIDToString(role.RoleID)
	if roleName == "" {
		roleName = "guest"
	}
	return map[string]interface{}{
		"role":       roleName,
		"title":      role.Title,
		"subscribed": subscribed,
	}, nil
}

// ListCommunities handles bridge.list_communities
// Paginated community list with optional full-text query and sort.
func (c *CommunityAPI) ListCommunities(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	last, _ := pMap["last"].(string)
	query, _ := pMap["query"].(string)
	sortKey, _ := pMap["sort"].(string)
	if sortKey == "" {
		sortKey = "rank"
	}
	observer, _ := pMap["observer"].(string)
	limit := validLimitParam(pMap, 100, 100)

	if sortKey != "rank" && sortKey != "new" && sortKey != "subs" {
		return nil, apierrors.PublicError("invalid sort")
	}

	var field, order string
	switch sortKey {
	case "rank":
		field, order = "rank", "ASC"
	case "new":
		field, order = "created_at", "DESC"
	case "subs":
		field, order = "subscribers", "DESC"
	}

	where := []string{}
	args := []interface{}{}
	if query != "" {
		where = append(where, "to_tsvector('english', title || ' ' || about) @@ plainto_tsquery(?)")
		args = append(args, query)
	}
	if field == "rank" {
		where = append(where, "rank > 0")
	}
	if last != "" {
		cmp := ">"
		if order == "DESC" {
			cmp = "<"
		}
		where = append(where, field+" "+cmp+" (SELECT "+field+" FROM hive_communities WHERE name = ?)")
		args = append(args, last)
	}

	sql := "SELECT id FROM hive_communities"
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	sql += " ORDER BY " + field + " " + order + " LIMIT ?"
	args = append(args, limit)

	dbCtx := ctx.Request.Context()
	var ids []int64
	if err := c.repo.DB().WithContext(dbCtx).Raw(sql, args...).Scan(&ids).Error; err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []interface{}{}, nil
	}

	communities, err := c.loadCommunities(dbCtx, ids, true)
	if err != nil {
		return nil, err
	}

	if observer != "" {
		observerID, err := c.getAccountID(dbCtx, observer)
		if err != nil {
			return nil, err
		}
		if observerID != 0 {
			if err := c.appendObserverSubs(dbCtx, communities, observerID); err != nil {
				return nil, err
			}
			if err := c.appendObserverRoles(dbCtx, communities, observerID); err != nil {
				return nil, err
			}
		}
	}
	if err := c.appendAdmins(dbCtx, communities); err != nil {
		return nil, err
	}

	out := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		if comm := communities[id]; comm != nil {
			out = append(out, comm)
		}
	}
	return out, nil
}

// ListTopCommunities handles bridge.list_top_communities
// Configured recommended communities first, then by rank. Returns (name, title) pairs.
func (c *CommunityAPI) ListTopCommunities(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		// Tolerate empty params ([] or null) like legacy no-arg calls.
		pMap = map[string]interface{}{}
	}
	limit := validLimitParam(pMap, 25, 99)
	if limit >= 100 {
		return nil, apierrors.PublicError("limit must be below 100")
	}

	custom := []interface{}{}
	if len(c.recommendCommunities) > 0 {
		var rows []struct {
			Name  string `gorm:"column:name"`
			Title string `gorm:"column:title"`
		}
		if err := c.repo.DB().WithContext(ctx.Request.Context()).
			Table("hive_communities").
			Select("name, title").
			Where("name IN ?", c.recommendCommunities).
			Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, r := range rows {
			custom = append(custom, []interface{}{r.Name, r.Title})
		}
	}

	if len(custom) >= limit {
		return custom[:limit], nil
	}

	var ranked []struct {
		Name  string `gorm:"column:name"`
		Title string `gorm:"column:title"`
	}
	rankQuery := c.repo.DB().WithContext(ctx.Request.Context()).
		Table("hive_communities").
		Select("name, title").
		Where("rank > 0")
	// Don't repeat communities already covered by the recommended list.
	if len(c.recommendCommunities) > 0 {
		rankQuery = rankQuery.Where("name NOT IN ?", c.recommendCommunities)
	}
	if err := rankQuery.
		Order("rank").
		Limit(limit - len(custom)).
		Find(&ranked).Error; err != nil {
		return nil, err
	}
	for _, r := range ranked {
		custom = append(custom, []interface{}{r.Name, r.Title})
	}
	return custom, nil
}

// ListPopCommunities handles bridge.list_pop_communities
// Communities ranked by new subscribers in the last 30 days.
func (c *CommunityAPI) ListPopCommunities(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}
	limit := validLimitParam(pMap, 25, 25)

	var rows []struct {
		Name  string `gorm:"column:name"`
		Title string `gorm:"column:title"`
	}
	cutoff := time.Now().AddDate(0, 0, -30)
	if err := c.repo.DB().WithContext(ctx.Request.Context()).Raw(
		`SELECT c.name, c.title
		   FROM hive_communities c
		   JOIN (
		            SELECT community_id, COUNT(*) AS newsubs
		              FROM hive_subscriptions
		             WHERE created_at > ?
		          GROUP BY community_id
		       ) stats
		     ON stats.community_id = c.id
		 ORDER BY newsubs DESC
		    LIMIT ?`, cutoff, limit,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]interface{}, 0, len(rows))
	for _, r := range rows {
		out = append(out, []interface{}{r.Name, r.Title})
	}
	return out, nil
}

// ListCommunityRoles handles bridge.list_community_roles
// Community members with a non-guest role, ordered by role then name.
func (c *CommunityAPI) ListCommunityRoles(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	community, _ := pMap["community"].(string)
	if community == "" {
		return nil, apierrors.PublicError("missing required parameter: community")
	}
	last, _ := pMap["last"].(string)
	limit := validLimitParam(pMap, 50, 100)

	dbCtx := ctx.Request.Context()
	cid, err := c.getCommunityID(dbCtx, community)
	if err != nil {
		return nil, err
	}

	seek := ""
	args := []interface{}{cid}
	if last != "" {
		// Resolve the start account's current role in this community
		// (guest when they have no role row). Unknown account = invalid start.
		var lrole struct {
			RoleID int16 `gorm:"column:role_id"`
		}
		err := c.repo.DB().WithContext(dbCtx).Raw(
			`SELECT r.role_id FROM hive_roles r
			   JOIN hive_accounts a ON r.account_id = a.id
			  WHERE a.name = ? AND r.community_id = ?`, last, cid,
		).Scan(&lrole).Error
		if err != nil {
			return nil, err
		}
		var exists int64
		if err := c.repo.DB().WithContext(dbCtx).Raw(
			`SELECT COUNT(*) FROM hive_accounts WHERE name = ?`, last,
		).Scan(&exists).Error; err != nil {
			return nil, err
		}
		if exists == 0 {
			return nil, apierrors.PublicError("invalid start")
		}
		seek = ` AND (r.role_id < ? OR (r.role_id = ? AND a.name > ?))`
		args = append(args, lrole.RoleID, lrole.RoleID, last)
	}

	sql := `SELECT a.name, r.role_id, r.title FROM hive_roles r
	          JOIN hive_accounts a ON r.account_id = a.id
	         WHERE r.community_id = ?` + seek + `
	           AND r.role_id != 0
	      ORDER BY r.role_id DESC, a.name LIMIT ?`
	args = append(args, limit)

	var rows []struct {
		Name   string `gorm:"column:name"`
		RoleID int16  `gorm:"column:role_id"`
		Title  string `gorm:"column:title"`
	}
	if err := c.repo.DB().WithContext(dbCtx).Raw(sql, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]interface{}, 0, len(rows))
	for _, r := range rows {
		out = append(out, []interface{}{r.Name, roleIDToString(r.RoleID), r.Title})
	}
	return out, nil
}

// ListSubscribers handles bridge.list_subscribers
// Most recent 250 subscribers of a community, with role and title.
func (c *CommunityAPI) ListSubscribers(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	community, _ := pMap["community"].(string)
	if community == "" {
		return nil, apierrors.PublicError("missing required parameter: community")
	}

	dbCtx := ctx.Request.Context()
	cid, err := c.getCommunityID(dbCtx, community)
	if err != nil {
		return nil, err
	}
	if cid == 0 {
		return nil, apierrors.PublicError("community not found")
	}

	var rows []struct {
		Name      string    `gorm:"column:name"`
		RoleID    *int16    `gorm:"column:role_id"`
		Title     *string   `gorm:"column:title"`
		CreatedAt time.Time `gorm:"column:created_at"`
	}
	if err := c.repo.DB().WithContext(dbCtx).Raw(
		`SELECT ha.name, hr.role_id, hr.title, hs.created_at
		   FROM hive_subscriptions hs
		  LEFT JOIN hive_roles hr ON hs.account_id = hr.account_id
		                          AND hs.community_id = hr.community_id
		   JOIN hive_accounts ha ON hs.account_id = ha.id
		  WHERE hs.community_id = ?
	       ORDER BY hs.created_at DESC
	          LIMIT 250`, cid,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]interface{}, 0, len(rows))
	for _, r := range rows {
		role := int16(0)
		if r.RoleID != nil {
			role = *r.RoleID
		}
		title := ""
		if r.Title != nil {
			title = *r.Title
		}
		out = append(out, []interface{}{r.Name, roleIDToString(role), title, r.CreatedAt.Format(time.RFC3339)})
	}
	return out, nil
}

// ListAllSubscriptions handles bridge.list_all_subscriptions
// All communities `account` subscribes to, with role and title in each.
func (c *CommunityAPI) ListAllSubscriptions(ctx *gin.Context, params json.RawMessage) (interface{}, error) {
	var pMap map[string]interface{}
	if err := json.Unmarshal(params, &pMap); err != nil {
		return nil, apierrors.PublicError("invalid parameters format")
	}

	account, _ := pMap["account"].(string)
	if account == "" {
		return nil, apierrors.PublicError("missing required parameter: account")
	}

	dbCtx := ctx.Request.Context()
	accountID, err := c.getAccountID(dbCtx, account)
	if err != nil {
		return nil, err
	}
	if accountID == 0 {
		return nil, apierrors.Publicf("account not found: `%s`", account)
	}

	var rows []struct {
		Name   string  `gorm:"column:name"`
		Title  string  `gorm:"column:title"`
		RoleID *int16  `gorm:"column:role_id"`
		RTitle *string `gorm:"column:rtitle"`
	}
	if err := c.repo.DB().WithContext(dbCtx).Raw(
		`SELECT c.name, c.title, r.role_id, r.title AS rtitle
		   FROM hive_communities c
		   JOIN hive_subscriptions s ON c.id = s.community_id
		  LEFT JOIN hive_roles r ON r.account_id = s.account_id
		                         AND r.community_id = c.id
		  WHERE s.account_id = ?
	       ORDER BY COALESCE(r.role_id, 0) DESC, c.rank`, accountID,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]interface{}, 0, len(rows))
	for _, r := range rows {
		role := int16(0)
		if r.RoleID != nil {
			role = *r.RoleID
		}
		title := ""
		if r.RTitle != nil {
			title = *r.RTitle
		}
		out = append(out, []interface{}{r.Name, r.Title, roleIDToString(role), title})
	}
	return out, nil
}

// loadCommunities builds community objects keyed by id. When lite is false
// the description/flag_text/settings fields and the leadership team are
// included. Mirrors legacy load_communities.
func (c *CommunityAPI) loadCommunities(ctx context.Context, ids []int64, lite bool) (map[int64]map[string]interface{}, error) {
	if len(ids) == 0 {
		return map[int64]map[string]interface{}{}, nil
	}

	var comms []models.Community
	if err := c.repo.DB().WithContext(ctx).
		Where("id IN ?", ids).
		Find(&comms).Error; err != nil {
		return nil, err
	}

	out := make(map[int64]map[string]interface{}, len(comms))
	teamIDs := make([]int64, 0)
	for _, comm := range comms {
		title := comm.Title
		if title == "" {
			title = "@" + comm.Name
		}
		obj := map[string]interface{}{
			"id":          comm.ID,
			"name":        comm.Name,
			"title":       title,
			"about":       comm.About,
			"lang":        comm.Lang,
			"type_id":     comm.TypeID,
			"is_nsfw":     comm.IsNSFW,
			"subscribers": comm.Subscribers,
			"sum_pending": comm.SumPending,
			"num_pending": comm.NumPending,
			"num_authors": comm.NumAuthors,
			"created_at":  comm.CreatedAt.Format(time.RFC3339),
			"avatar_url":  comm.AvatarURL,
			"context":     map[string]interface{}{},
		}
		if !lite {
			obj["description"] = comm.Description
			obj["flag_text"] = comm.FlagText
			settings := map[string]interface{}{}
			if comm.Settings.Valid && comm.Settings.String != "" {
				_ = json.Unmarshal([]byte(comm.Settings.String), &settings)
			}
			obj["settings"] = settings
			teamIDs = append(teamIDs, comm.ID)
		}
		out[comm.ID] = obj
	}

	// Attach leadership team (roles 4-8) for full loads.
	if len(teamIDs) > 0 {
		var team []struct {
			CommunityID int64  `gorm:"column:community_id"`
			Name        string `gorm:"column:name"`
			RoleID      int16  `gorm:"column:role_id"`
			Title       string `gorm:"column:title"`
		}
		if err := c.repo.DB().WithContext(ctx).Raw(
			`SELECT r.community_id, a.name, r.role_id, r.title
			   FROM hive_roles r
			   JOIN hive_accounts a ON r.account_id = a.id
			  WHERE r.community_id IN ? AND r.role_id BETWEEN 4 AND 8
		   ORDER BY r.role_id DESC`, teamIDs,
		).Scan(&team).Error; err != nil {
			return nil, err
		}
		teamByCID := make(map[int64][]interface{})
		for _, t := range team {
			teamByCID[t.CommunityID] = append(teamByCID[t.CommunityID],
				[]interface{}{t.Name, roleIDToString(t.RoleID), t.Title})
		}
		for _, cid := range teamIDs {
			if obj := out[cid]; obj != nil {
				if team, ok := teamByCID[cid]; ok {
					obj["team"] = team
				} else {
					obj["team"] = []interface{}{}
				}
			}
		}
	}

	return out, nil
}

// appendObserverRoles fills context.role / context.title for each community
// relative to the observer. Mirrors legacy _append_observer_roles.
func (c *CommunityAPI) appendObserverRoles(ctx context.Context, communities map[int64]map[string]interface{}, observerID int64) error {
	ids := make([]int64, 0, len(communities))
	for cid := range communities {
		ids = append(ids, cid)
	}
	if len(ids) == 0 {
		return nil
	}

	var roles []models.Role
	if err := c.repo.DB().WithContext(ctx).
		Where("account_id = ? AND community_id IN ?", observerID, ids).
		Find(&roles).Error; err != nil {
		return err
	}

	roleByCID := make(map[int64]models.Role, len(roles))
	for _, r := range roles {
		roleByCID[r.CommunityID] = r
	}
	for cid, comm := range communities {
		contextObj, _ := comm["context"].(map[string]interface{})
		if contextObj == nil {
			contextObj = map[string]interface{}{}
			comm["context"] = contextObj
		}
		if r, ok := roleByCID[cid]; ok {
			contextObj["role"] = roleIDToString(r.RoleID)
			contextObj["title"] = r.Title
		} else {
			contextObj["role"] = "guest"
			contextObj["title"] = ""
		}
	}
	return nil
}

// appendObserverSubs fills context.subscribed for each community relative to
// the observer. Mirrors legacy _append_observer_subs.
func (c *CommunityAPI) appendObserverSubs(ctx context.Context, communities map[int64]map[string]interface{}, observerID int64) error {
	ids := make([]int64, 0, len(communities))
	for cid := range communities {
		ids = append(ids, cid)
	}
	if len(ids) == 0 {
		return nil
	}

	var subIDs []int64
	if err := c.repo.DB().WithContext(ctx).
		Model(&models.Subscription{}).
		Where("account_id = ? AND community_id IN ?", observerID, ids).
		Pluck("community_id", &subIDs).Error; err != nil {
		return err
	}

	subSet := make(map[int64]bool, len(subIDs))
	for _, cid := range subIDs {
		subSet[cid] = true
	}
	for cid, comm := range communities {
		contextObj, _ := comm["context"].(map[string]interface{})
		if contextObj == nil {
			contextObj = map[string]interface{}{}
			comm["context"] = contextObj
		}
		contextObj["subscribed"] = subSet[cid]
	}
	return nil
}

// appendAdmins fills the admins list (role_id = 6) for each community.
// Mirrors legacy _append_admins.
func (c *CommunityAPI) appendAdmins(ctx context.Context, communities map[int64]map[string]interface{}) error {
	ids := make([]int64, 0, len(communities))
	for cid := range communities {
		ids = append(ids, cid)
	}
	if len(ids) == 0 {
		return nil
	}

	var rows []struct {
		CommunityID int64  `gorm:"column:community_id"`
		Name        string `gorm:"column:name"`
	}
	if err := c.repo.DB().WithContext(ctx).Raw(
		`SELECT r.community_id, a.name
		   FROM hive_roles r
		   JOIN hive_accounts a ON r.account_id = a.id
		  WHERE r.role_id = 6 AND r.community_id IN ?`, ids,
	).Scan(&rows).Error; err != nil {
		return err
	}

	for _, row := range rows {
		comm := communities[row.CommunityID]
		if comm == nil {
			continue
		}
		admins, _ := comm["admins"].([]interface{})
		comm["admins"] = append(admins, row.Name)
	}
	return nil
}

// getAccountID resolves an account name to its hive_accounts.id.
func (c *CommunityAPI) getAccountID(ctx context.Context, name string) (int64, error) {
	if name == "" {
		return 0, apierrors.PublicError("no account name specified")
	}
	var acc models.Account
	err := c.repo.DB().WithContext(ctx).
		Where("name = ?", name).
		Select("id").
		First(&acc).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return 0, nil
		}
		return 0, err
	}
	return acc.ID, nil
}

// roleIDToString maps a numeric role_id to its display name; unknown ids map
// to "guest" (legacy ROLES lookup falls back through dict.get in callers).
func roleIDToString(roleID int16) string {
	switch roleID {
	case -2:
		return "muted"
	case 0:
		return "guest"
	case 2:
		return "member"
	case 4:
		return "mod"
	case 6:
		return "admin"
	case 8:
		return "owner"
	}
	return "guest"
}
