package models

import (
	"database/sql"
	"time"
)

// FeedCache represents a feed cache entry (posts + reblogs)
type FeedCache struct {
	PostID    int64     `gorm:"primaryKey;column:post_id"`
	AccountID int64     `gorm:"primaryKey;column:account_id"`
	CreatedAt time.Time `gorm:"not null;column:created_at"`

	// Relationships
	Account *Account `gorm:"foreignKey:AccountID;references:ID"`
	Post    *Post    `gorm:"foreignKey:PostID;references:ID"`
}

// TableName specifies the table name for FeedCache
func (FeedCache) TableName() string {
	return "hive_feed_cache"
}

// PostCache represents cached post data with computed fields (hive_posts_cache).
//
// Mirrors legacy schema.py hive_posts_cache (32 columns, DB_VERSION 29).
// NOTE: the bulk JSON column is named `json` (NOT `json_metadata`) in the
// legacy schema. The API layer exposes it under the condenser-compatible
// `json_metadata` key — that mapping lives in the API handlers, not here.
type PostCache struct {
	PostID      int64         `gorm:"primaryKey;column:post_id"`
	Author      string        `gorm:"type:varchar(16);not null;column:author"`
	Permlink    string        `gorm:"type:varchar(255);not null;column:permlink"`
	Category    string        `gorm:"type:varchar(255);not null;default:'';column:category"`
	CommunityID sql.NullInt64 `gorm:"column:community_id"`
	Depth       int16         `gorm:"type:smallint;not null;default:0;column:depth"`
	Children    int16         `gorm:"type:smallint;not null;default:0;column:children"`
	AuthorRep   float64       `gorm:"type:float(6);not null;default:0;column:author_rep"`
	FlagWeight  float64       `gorm:"type:float(6);not null;default:0;column:flag_weight"`
	TotalVotes  int64         `gorm:"not null;default:0;column:total_votes"`
	UpVotes     int64         `gorm:"not null;default:0;column:up_votes"`
	Title       string        `gorm:"type:varchar(255);not null;default:'';column:title"`
	Preview     string        `gorm:"type:varchar(1024);not null;default:'';column:preview"`
	ImgURL      string        `gorm:"type:varchar(1024);not null;default:'';column:img_url"`
	Payout      float64       `gorm:"type:decimal(10,3);not null;default:0;column:payout"`
	Promoted    float64       `gorm:"type:decimal(10,3);not null;default:0;column:promoted"`
	CreatedAt   time.Time     `gorm:"not null;default:'1990-01-01';column:created_at"`
	PayoutAt    time.Time     `gorm:"not null;default:'1990-01-01';column:payout_at"`
	UpdatedAt   time.Time     `gorm:"not null;default:'1990-01-01';column:updated_at"`
	IsPaidout   bool          `gorm:"not null;default:false;column:is_paidout"`
	IsNSFW      bool          `gorm:"not null;default:false;column:is_nsfw"`
	IsDeclined  bool          `gorm:"not null;default:false;column:is_declined"`
	IsFullPower bool          `gorm:"not null;default:false;column:is_full_power"`
	IsHidden    bool          `gorm:"not null;default:false;column:is_hidden"`
	IsGrayed    bool          `gorm:"not null;default:false;column:is_grayed"`

	// Computed / frequently-changing fields
	RShares int64   `gorm:"not null;default:0;column:rshares"`
	SCTrend float64 `gorm:"type:float(6);not null;default:0;column:sc_trend"`
	SCHot   float64 `gorm:"type:float(6);not null;default:0;column:sc_hot"`

	// Large/variable fields
	Body    string `gorm:"type:text;column:body"`
	Votes   string `gorm:"type:text;column:votes"`
	JSON    string `gorm:"type:text;column:json"`
	RawJSON string `gorm:"type:text;column:raw_json"`

	// Relationships
	Post *Post `gorm:"foreignKey:PostID;references:ID"`
}

// TableName specifies the table name for PostCache
func (PostCache) TableName() string {
	return "hive_posts_cache"
}

// PostCacheTemp mirrors PostCache in the hive_posts_cache_temp table, which
// holds a rolling 90-day hot-data window pruned by the CacheSync background
// job. It is identical to PostCache plus a _synced_at column tracking the
// last dual-write.
type PostCacheTemp struct {
	PostID      int64         `gorm:"primaryKey;column:post_id"`
	Author      string        `gorm:"type:varchar(16);not null;column:author"`
	Permlink    string        `gorm:"type:varchar(255);not null;column:permlink"`
	Category    string        `gorm:"type:varchar(255);not null;default:'';column:category"`
	CommunityID sql.NullInt64 `gorm:"column:community_id"`
	Depth       int16         `gorm:"type:smallint;not null;default:0;column:depth"`
	Children    int16         `gorm:"type:smallint;not null;default:0;column:children"`
	AuthorRep   float64       `gorm:"type:float(6);not null;default:0;column:author_rep"`
	FlagWeight  float64       `gorm:"type:float(6);not null;default:0;column:flag_weight"`
	TotalVotes  int64         `gorm:"not null;default:0;column:total_votes"`
	UpVotes     int64         `gorm:"not null;default:0;column:up_votes"`
	Title       string        `gorm:"type:varchar(255);not null;default:'';column:title"`
	Preview     string        `gorm:"type:varchar(1024);not null;default:'';column:preview"`
	ImgURL      string        `gorm:"type:varchar(1024);not null;default:'';column:img_url"`
	Payout      float64       `gorm:"type:decimal(10,3);not null;default:0;column:payout"`
	Promoted    float64       `gorm:"type:decimal(10,3);not null;default:0;column:promoted"`
	CreatedAt   time.Time     `gorm:"not null;default:'1990-01-01';column:created_at"`
	PayoutAt    time.Time     `gorm:"not null;default:'1990-01-01';column:payout_at"`
	UpdatedAt   time.Time     `gorm:"not null;default:'1990-01-01';column:updated_at"`
	IsPaidout   bool          `gorm:"not null;default:false;column:is_paidout"`
	IsNSFW      bool          `gorm:"not null;default:false;column:is_nsfw"`
	IsDeclined  bool          `gorm:"not null;default:false;column:is_declined"`
	IsFullPower bool          `gorm:"not null;default:false;column:is_full_power"`
	IsHidden    bool          `gorm:"not null;default:false;column:is_hidden"`
	IsGrayed    bool          `gorm:"not null;default:false;column:is_grayed"`

	RShares int64   `gorm:"not null;default:0;column:rshares"`
	SCTrend float64 `gorm:"type:float(6);not null;default:0;column:sc_trend"`
	SCHot   float64 `gorm:"type:float(6);not null;default:0;column:sc_hot"`

	Body     string       `gorm:"type:text;column:body"`
	Votes    string       `gorm:"type:text;column:votes"`
	JSON     string       `gorm:"type:text;column:json"`
	RawJSON  string       `gorm:"type:text;column:raw_json"`
	SyncedAt sql.NullTime `gorm:"column:_synced_at"`
}

// TableName specifies the table name for PostCacheTemp
func (PostCacheTemp) TableName() string {
	return "hive_posts_cache_temp"
}
