package models

import (
	"database/sql"
	"time"
)

// Account represents a Steem account
type Account struct {
	ID          int64          `gorm:"primaryKey;autoIncrement;column:id"`
	Name        string         `gorm:"type:varchar(16);not null;uniqueIndex:hive_accounts_ux1;column:name"`
	CreatedAt   time.Time      `gorm:"not null;column:created_at"`
	Reputation  float64        `gorm:"type:float(6);not null;default:25;column:reputation"`
	
	// Profile fields
	DisplayName sql.NullString `gorm:"type:varchar(20);column:display_name"`
	About       sql.NullString `gorm:"type:varchar(160);column:about"`
	Location    sql.NullString `gorm:"type:varchar(30);column:location"`
	Website     sql.NullString `gorm:"type:varchar(100);column:website"`
	ProfileImage string        `gorm:"type:varchar(1024);not null;default:'';column:profile_image"`
	CoverImage   string        `gorm:"type:varchar(1024);not null;default:'';column:cover_image"`
	
	// Social stats
	Followers   int64         `gorm:"not null;default:0;column:followers"`
	Following   int64         `gorm:"not null;default:0;column:following"`
	
	// Voting
	Proxy       string        `gorm:"type:varchar(16);not null;default:'';column:proxy"`
	PostCount   int64         `gorm:"not null;default:0;column:post_count"`
	ProxyWeight float64       `gorm:"type:float(6);not null;default:0;column:proxy_weight"`
	VoteWeight  float64       `gorm:"type:float(6);not null;default:0;column:vote_weight"`
	Rank        int64         `gorm:"not null;default:0;column:rank"`
	
	// Activity tracking
	LastreadAt time.Time     `gorm:"not null;default:'1970-01-01 00:00:00';column:lastread_at"`
	ActiveAt   time.Time     `gorm:"not null;default:'1970-01-01 00:00:00';column:active_at"`
	CachedAt   time.Time     `gorm:"not null;default:'1970-01-01 00:00:00';column:cached_at"`
	
	// Raw data
	RawJSON    sql.NullString `gorm:"type:text;column:raw_json"`
}

// TableName specifies the table name for Account
func (Account) TableName() string {
	return "hive_accounts"
}

