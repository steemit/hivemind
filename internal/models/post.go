package models

import (
	"database/sql"
	"time"
)

// Post represents a post or comment
type Post struct {
	ID          int64          `gorm:"primaryKey;autoIncrement;column:id"`
	ParentID    sql.NullInt64  `gorm:"column:parent_id"`
	Author      string         `gorm:"type:varchar(16);not null;column:author"`
	Permlink    string         `gorm:"type:varchar(255);not null;column:permlink"`
	Category    string         `gorm:"type:varchar(255);column:category"`
	CommunityID sql.NullInt64  `gorm:"column:community_id"`
	CreatedAt   time.Time      `gorm:"not null;column:created_at"`
	Depth       int16          `gorm:"type:smallint;column:depth"`
	IsDeleted   bool           `gorm:"not null;default:false;column:is_deleted"`
	IsPinned    bool           `gorm:"not null;default:false;column:is_pinned"`
	IsMuted     bool           `gorm:"not null;default:false;column:is_muted"`
	IsValid     bool           `gorm:"not null;default:true;column:is_valid"`
	Promoted    float64        `gorm:"type:decimal(10,3);default:0;column:promoted"`

	// Relationships
	Parent   *Post   `gorm:"foreignKey:ParentID;references:ID"`
	Children []Post  `gorm:"foreignKey:ParentID;references:ID"`
	AuthorAccount *Account `gorm:"foreignKey:Author;references:Name"`
}

// TableName specifies the table name for Post
func (Post) TableName() string {
	return "hive_posts"
}

// PostTag represents a post-to-tag mapping
type PostTag struct {
	PostID int64  `gorm:"primaryKey;column:post_id"`
	Tag    string `gorm:"type:varchar(32);primaryKey;column:tag"`
}

// TableName specifies the table name for PostTag
func (PostTag) TableName() string {
	return "hive_post_tags"
}

