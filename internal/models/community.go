package models

import (
	"database/sql"
	"time"
)

// Community represents a community
type Community struct {
	ID          int64          `gorm:"primaryKey;autoIncrement:false;column:id"`
	TypeID      int16          `gorm:"type:smallint;not null;column:type_id"`
	Lang        string         `gorm:"type:char(2);not null;default:'en';column:lang"`
	Name        string         `gorm:"type:varchar(16);not null;uniqueIndex:hive_communities_ux1;column:name"`
	Title       string         `gorm:"type:varchar(32);not null;default:'';column:title"`
	CreatedAt   time.Time      `gorm:"not null;column:created_at"`
	SumPending  int64          `gorm:"not null;default:0;column:sum_pending"`
	NumPending  int64          `gorm:"not null;default:0;column:num_pending"`
	NumAuthors  int64          `gorm:"not null;default:0;column:num_authors"`
	Rank        int64          `gorm:"not null;default:0;column:rank"`
	Subscribers int64          `gorm:"not null;default:0;column:subscribers"`
	IsNSFW      bool           `gorm:"not null;default:false;column:is_nsfw"`
	About       string         `gorm:"type:varchar(120);not null;default:'';column:about"`
	PrimaryTag  string         `gorm:"type:varchar(32);not null;default:'';column:primary_tag"`
	Category    string         `gorm:"type:varchar(32);not null;default:'';column:category"`
	AvatarURL   string         `gorm:"type:varchar(1024);not null;default:'';column:avatar_url"`
	Description string         `gorm:"type:varchar(5000);not null;default:'';column:description"`
	FlagText    string         `gorm:"type:varchar(5000);not null;default:'';column:flag_text"`
	Settings    sql.NullString `gorm:"type:text;default:'{}';column:settings"`
}

// TableName specifies the table name for Community
func (Community) TableName() string {
	return "hive_communities"
}

// Community type constants
const (
	CommunityTypeTopic   int16 = 1 // Topic community
	CommunityTypeJournal int16 = 2 // Journal community
	CommunityTypeCouncil int16 = 3 // Council community
)

// Role represents a community role
type Role struct {
	CommunityID int64          `gorm:"primaryKey;column:community_id"`
	AccountID   int64          `gorm:"primaryKey;column:account_id"`
	Role        int16          `gorm:"type:smallint;not null;default:0;column:role"`
	Title       sql.NullString `gorm:"type:varchar(140);column:title"`
	CreatedAt   time.Time      `gorm:"not null;column:created_at"`

	// Relationships
	Community *Community `gorm:"foreignKey:CommunityID;references:ID"`
	Account   *Account   `gorm:"foreignKey:AccountID;references:ID"`
}

// TableName specifies the table name for Role
func (Role) TableName() string {
	return "hive_roles"
}

// Role constants
const (
	RoleMuted  int16 = -2 // Muted
	RoleGuest  int16 = 0  // Guest
	RoleMember int16 = 2  // Member
	RoleMod    int16 = 4  // Moderator
	RoleAdmin  int16 = 6  // Admin
	RoleOwner  int16 = 8  // Owner
)

// Subscription represents a community subscription
type Subscription struct {
	CommunityID int64     `gorm:"primaryKey;column:community_id"`
	AccountID   int64     `gorm:"primaryKey;column:account_id"`
	CreatedAt   time.Time `gorm:"not null;column:created_at"`

	// Relationships
	Community *Community `gorm:"foreignKey:CommunityID;references:ID"`
	Account   *Account   `gorm:"foreignKey:AccountID;references:ID"`
}

// TableName specifies the table name for Subscription
func (Subscription) TableName() string {
	return "hive_subscriptions"
}

