package hive

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/steemit/hivemind/internal/models"
)

// estimatedSp converts VESTS to SP units for display.
// Mirrors legacy hive_api/common.py estimated_sp.
func estimatedSp(vests float64) int64 {
	return int64(vests * 0.0005034)
}

// accountsByNames loads hive_api account objects by name.
// Mirrors legacy hive_api/objects.py accounts_by_name. When lite is false the
// profile fields (location, website, images) are included. An optional
// observer adds followed/muted context to each account.
func accountsByNames(ctx context.Context, db *gorm.DB, names []string, observer string, lite bool) ([]map[string]interface{}, error) {
	if len(names) == 0 {
		return []map[string]interface{}{}, nil
	}

	var accounts []models.Account
	if err := db.WithContext(ctx).
		Where("name IN ?", names).
		Find(&accounts).Error; err != nil {
		return nil, err
	}

	byID := make(map[int64]map[string]interface{}, len(accounts))
	for _, acc := range accounts {
		obj := map[string]interface{}{
			"id":           acc.ID,
			"name":         acc.Name,
			"created":      acc.CreatedAt.Format("2006-01-02"),
			"sp":           estimatedSp(acc.VoteWeight),
			"rank":         acc.Rank,
			"followers":    acc.Followers,
			"following":    acc.Following,
			"display_name": nullString(acc.DisplayName),
			"about":        nullString(acc.About),
		}
		if !lite {
			obj["location"] = nullString(acc.Location)
			obj["website"] = nullString(acc.Website)
			obj["profile_image"] = acc.ProfileImage
			obj["cover_image"] = acc.CoverImage
		}
		byID[acc.ID] = obj
	}

	if observer != "" {
		if err := appendFollowContexts(ctx, db, byID, observer, !lite); err != nil {
			return nil, err
		}
	}

	// Preserve the input names order (legacy returns rows in query order;
	// hive_api callers rely on list semantics).
	out := make([]map[string]interface{}, 0, len(names))
	for _, name := range names {
		for _, acc := range accounts {
			if acc.Name == name {
				out = append(out, byID[acc.ID])
				break
			}
		}
	}
	return out, nil
}

// appendFollowContexts adds {followed, muted} context for each account
// relative to `observer`. Mirrors legacy _follow_contexts.
func appendFollowContexts(ctx context.Context, db *gorm.DB, accounts map[int64]map[string]interface{}, observer string, includeMute bool) error {
	var observerAcc models.Account
	if err := db.WithContext(ctx).
		Where("name = ?", observer).
		Select("id").
		First(&observerAcc).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}

	ids := make([]int64, 0, len(accounts))
	for id := range accounts {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}

	var follows []models.Follow
	if err := db.WithContext(ctx).
		Where("follower = ? AND following IN ?", observerAcc.ID, ids).
		Find(&follows).Error; err != nil {
		return err
	}

	for _, f := range follows {
		acc, ok := accounts[f.FollowingID]
		if !ok {
			continue
		}
		contextObj := map[string]interface{}{"followed": f.State == 1 || f.State == 3}
		if includeMute && (f.State == 2 || f.State == 3) {
			contextObj["muted"] = true
		}
		acc["context"] = contextObj
	}
	for _, acc := range accounts {
		if _, ok := acc["context"]; !ok {
			acc["context"] = map[string]interface{}{"followed": false}
		}
	}
	return nil
}

// postsByID loads lite hive_api post objects by ID, returned in input order
// together with the distinct authors. Mirrors legacy posts_by_id
// (hive_api/objects.py): {"posts": [...], "accounts": [...]}.
func postsByID(ctx context.Context, db *gorm.DB, ids []int64, observer string, lite bool) (map[string]interface{}, error) {
	if len(ids) == 0 {
		return map[string]interface{}{
			"posts":    []map[string]interface{}{},
			"accounts": []map[string]interface{}{},
		}, nil
	}

	var caches []models.PostCache
	if err := db.WithContext(ctx).
		Where("post_id IN ?", ids).
		Find(&caches).Error; err != nil {
		return nil, err
	}
	cacheMap := make(map[int64]*models.PostCache, len(caches))
	for i := range caches {
		cacheMap[caches[i].PostID] = &caches[i]
	}

	var posts []models.Post
	if err := db.WithContext(ctx).
		Where("id IN ?", ids).
		Find(&posts).Error; err != nil {
		return nil, err
	}
	postMap := make(map[int64]*models.Post, len(posts))
	for i := range posts {
		postMap[posts[i].ID] = &posts[i]
	}

	authorSet := make(map[string]bool)
	byID := make(map[int64]map[string]interface{}, len(ids))
	for _, id := range ids {
		post, cache := postMap[id], cacheMap[id]
		if post == nil || cache == nil {
			continue
		}

		obj := map[string]interface{}{
			"id":         id,
			"author":     cache.Author,
			"url":        cache.Author + "/" + cache.Permlink,
			"title":      cache.Title,
			"payout":     cache.Payout,
			"promoted":   cache.Promoted,
			"created_at": cache.CreatedAt.Format(time.RFC3339),
			"payout_at":  cache.PayoutAt.Format(time.RFC3339),
			"is_paidout": cache.IsPaidout,
			"rshares":    cache.RShares,
			"top_votes":  topVotes(cache.Votes, 5),
			"thumb_url":  cache.ImgURL,
			"is_nsfw":    cache.IsNSFW,
		}
		if lite {
			obj["preview"] = cache.Preview
		} else {
			obj["body"] = cache.Body
			obj["updated_at"] = cache.UpdatedAt.Format(time.RFC3339)
			obj["json_metadata"] = cache.JSON
		}
		obj["parent_id"] = nullInt64(post.ParentID)
		obj["community_id"] = nullInt64(post.CommunityID)
		obj["category"] = post.Category
		obj["is_muted"] = post.IsMuted
		obj["is_valid"] = post.IsValid

		authorSet[cache.Author] = true
		byID[id] = obj
	}

	// Recover from cache inconsistency by dropping missing ids (legacy warns).
	orderedIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := byID[id]; ok {
			orderedIDs = append(orderedIDs, id)
		}
	}

	postList := make([]map[string]interface{}, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		postList = append(postList, byID[id])
	}

	authors := make([]string, 0, len(authorSet))
	for name := range authorSet {
		authors = append(authors, name)
	}
	sort.Strings(authors)

	accounts, err := accountsByNames(ctx, db, authors, observer, true)
	if err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"posts":    postList,
		"accounts": accounts,
	}, nil
}

// topVotes parses the votes CSV and returns the `limit` largest by abs
// rshares as [voter, rshares] pairs. Mirrors legacy _top_votes.
func topVotes(voteCSV string, limit int) []interface{} {
	if voteCSV == "" {
		return []interface{}{}
	}
	type vote struct {
		voter   string
		rshares int64
	}
	votes := make([]vote, 0, 16)
	for _, line := range strings.Split(voteCSV, "\n") {
		parts := strings.Split(line, ",")
		if len(parts) < 2 {
			continue
		}
		rshares, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		votes = append(votes, vote{voter: parts[0], rshares: rshares})
	}
	sort.SliceStable(votes, func(i, j int) bool {
		return abs64(votes[i].rshares) > abs64(votes[j].rshares)
	})
	if len(votes) > limit {
		votes = votes[:limit]
	}
	out := make([]interface{}, 0, len(votes))
	for _, v := range votes {
		out = append(out, []interface{}{v.voter, v.rshares})
	}
	return out
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// nullString unwraps sql.NullString for JSON output (nil when NULL).
func nullString(ns sql.NullString) interface{} {
	if ns.Valid {
		return ns.String
	}
	return nil
}

// nullInt64 unwraps sql.NullInt64 for JSON output (nil when NULL).
func nullInt64(v sql.NullInt64) interface{} {
	if v.Valid {
		return v.Int64
	}
	return nil
}
