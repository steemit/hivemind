package models

import (
	"time"
)

// Follow represents a follow relationship
type Follow struct {
	FollowerID  int64     `gorm:"primaryKey;column:follower"`
	FollowingID int64     `gorm:"primaryKey;column:following"`
	State       int16     `gorm:"type:smallint;not null;default:0;column:state"`
	CreatedAt   time.Time `gorm:"not null;column:created_at"`

	// Relationships
	Follower  *Account `gorm:"foreignKey:FollowerID;references:ID"`
	Following *Account `gorm:"foreignKey:FollowingID;references:ID"`
}

// TableName specifies the table name for Follow
func (Follow) TableName() string {
	return "hive_follows"
}

// Follow state constants
const (
	FollowStateNone   int16 = 0 // No relationship
	FollowStateBlog   int16 = 1 // Blog follow
	FollowStateIgnore int16 = 2 // Ignore
	FollowStateBoth   int16 = 3 // Blog follow + ignore
)

