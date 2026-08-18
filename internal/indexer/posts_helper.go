package indexer

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// PostsHelper mirrors the hive_posts lookups the legacy Posts class provided
// to CachedPost (get_id / last_id / batch url→id resolution).
type PostsHelper struct {
	db *gorm.DB
}

// NewPostsHelper creates a PostsHelper.
func NewPostsHelper(db *gorm.DB) *PostsHelper {
	return &PostsHelper{db: db}
}

// GetID returns the hive_posts.id for author/permlink.
func (p *PostsHelper) GetID(ctx context.Context, author, permlink string) (int64, error) {
	var id int64
	err := p.db.WithContext(ctx).
		Table("hive_posts").
		Select("id").
		Where("author = ? AND permlink = ?", author, permlink).
		Scan(&id).Error
	if err != nil {
		return 0, err
	}
	if id == 0 {
		return 0, fmt.Errorf("post %s/%s not found", author, permlink)
	}
	return id, nil
}

// LastID returns MAX(id) from hive_posts.
func (p *PostsHelper) LastID(ctx context.Context) (int64, error) {
	var id int64
	err := p.db.WithContext(ctx).
		Table("hive_posts").
		Select("COALESCE(MAX(id), 0)").
		Scan(&id).Error
	return id, err
}

// URLsToIDs resolves a set of author/permlink URLs to ids in two queries
// (authors IN + permlinks IN, then exact pair matching). Used by
// CachedPost.loadNoids.
func (p *PostsHelper) URLsToIDs(ctx context.Context, urls map[string]bool) (map[string]int64, error) {
	out := map[string]int64{}
	if len(urls) == 0 {
		return out, nil
	}
	authors := map[string]bool{}
	permlinks := map[string]bool{}
	for url := range urls {
		a, pl, ok := splitURL(url)
		if !ok {
			continue
		}
		authors[a] = true
		permlinks[pl] = true
	}
	alist := keysOf(authors)
	pllist := keysOf(permlinks)

	type row struct {
		ID       int64
		Author   string
		Permlink string
	}
	var rows []row
	if err := p.db.WithContext(ctx).
		Table("hive_posts").
		Select("id", "author", "permlink").
		Where("author IN ? AND permlink IN ?", alist, pllist).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	byURL := map[string]int64{}
	for _, r := range rows {
		byURL[r.Author+"/"+r.Permlink] = r.ID
	}
	for url := range urls {
		if id, ok := byURL[url]; ok {
			out[url] = id
		}
	}
	return out, nil
}

func splitURL(url string) (string, string, bool) {
	for i := 0; i < len(url); i++ {
		if url[i] == '/' {
			return url[:i], url[i+1:], true
		}
	}
	return "", "", false
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
