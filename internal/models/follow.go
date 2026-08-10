package models

import (
	"time"
)

// Follow represents a follow relationship.
//
// Mirrors legacy schema.py hive_follows: the `state` column defaults to 1
// (blog follow), not 0. The state is a bitfield: bit 0 = blog follow,
// bit 1 = ignore.
type Follow struct {
	FollowerID  int64     `gorm:"primaryKey;column:follower"`
	FollowingID int64     `gorm:"primaryKey;column:following"`
	State       int16     `gorm:"type:smallint;not null;default:1;column:state"`
	CreatedAt   time.Time `gorm:"not null;column:created_at"`

	// Relationships
	Follower  *Account `gorm:"foreignKey:FollowerID;references:ID"`
	Following *Account `gorm:"foreignKey:FollowingID;references:ID"`
}

// TableName specifies the table name for Follow
func (Follow) TableName() string {
	return "hive_follows"
}

// Follow state bitfield constants. The schema default is FollowStateBlog (1),
// so a freshly inserted row with no explicit state represents a blog follow.
const (
	FollowStateNone   int16 = 0 // No relationship (explicitly cleared)
	FollowStateBlog   int16 = 1 // Blog follow (schema default)
	FollowStateIgnore int16 = 2 // Ignore
	FollowStateBoth   int16 = 3 // Blog follow + ignore
)
