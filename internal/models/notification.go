package models

import (
	"database/sql"
	"time"
)

// Notification represents a notification
type Notification struct {
	ID          int64          `gorm:"primaryKey;autoIncrement;column:id"`
	Type        int16          `gorm:"type:smallint;not null;column:type_id"`
	Score       int16          `gorm:"type:smallint;not null;default:0;column:score"`
	CreatedAt   time.Time      `gorm:"not null;column:created_at"`
	SrcID       sql.NullInt64  `gorm:"column:src_id"`
	DstID       sql.NullInt64  `gorm:"column:dst_id"`
	CommunityID sql.NullInt64  `gorm:"column:community_id"`
	PostID      sql.NullInt64  `gorm:"column:post_id"`
	Payload     sql.NullString `gorm:"type:text;column:payload"`

	// Relationships
	Src       *Account   `gorm:"foreignKey:SrcID;references:ID"`
	Dst       *Account   `gorm:"foreignKey:DstID;references:ID"`
	Community *Community `gorm:"foreignKey:CommunityID;references:ID"`
	Post      *Post      `gorm:"foreignKey:PostID;references:ID"`
}

// TableName specifies the table name for Notification
func (Notification) TableName() string {
	return "hive_notifs"
}

// Notification type constants
const (
	NotifyTypeNewCommunity  int16 = 1
	NotifyTypeSetRole       int16 = 2
	NotifyTypeSetProps      int16 = 3
	NotifyTypeSetLabel      int16 = 4
	NotifyTypeMutePost      int16 = 5
	NotifyTypeUnmutePost    int16 = 6
	NotifyTypePinPost       int16 = 7
	NotifyTypeUnpinPost     int16 = 8
	NotifyTypeFlagPost      int16 = 9
	NotifyTypeError          int16 = 10
	NotifyTypeSubscribe     int16 = 11
	NotifyTypeReply          int16 = 12
	NotifyTypeReplyComment   int16 = 13
	NotifyTypeReblog         int16 = 14
	NotifyTypeFollow         int16 = 15
	NotifyTypeMention        int16 = 16
	NotifyTypeVote           int16 = 17
)

