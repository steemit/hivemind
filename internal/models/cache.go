package models

import (
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

// PostCache represents cached post data with computed fields
type PostCache struct {
	PostID    int64     `gorm:"primaryKey;column:post_id"`
	Author    string    `gorm:"type:varchar(16);not null;column:author"`
	Permlink  string    `gorm:"type:varchar(255);not null;column:permlink"`
	Category  string    `gorm:"type:varchar(255);column:category"`
	Title     string    `gorm:"type:varchar(255);column:title"`
	Body      string    `gorm:"type:text;column:body"`
	JSONMeta  string    `gorm:"type:text;column:json_metadata"`
	CreatedAt time.Time `gorm:"not null;column:created_at"`
	UpdatedAt time.Time `gorm:"not null;column:updated_at"`
	
	// Computed fields
	SCTrend   float64   `gorm:"type:float;column:sc_trend"`
	SCHot     float64   `gorm:"type:float;column:sc_hot"`
	Payout    float64   `gorm:"type:decimal(10,3);column:payout"`
	RShares   int64     `gorm:"column:rshares"`
	Votes     string    `gorm:"type:text;column:votes"`
	Children  int64     `gorm:"column:children"`
	Preview   string    `gorm:"type:varchar(1024);column:preview"`
	ImgURL    string    `gorm:"type:varchar(1024);column:img_url"`
	
	// Relationships
	Post *Post `gorm:"foreignKey:PostID;references:ID"`
}

// TableName specifies the table name for PostCache
func (PostCache) TableName() string {
	return "hive_posts_cache"
}

