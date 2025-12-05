package models

import (
	"time"
)

// Reblog represents a reblog (resteem) relationship
type Reblog struct {
	Account   string    `gorm:"primaryKey;type:varchar(16);column:account"`
	PostID    int64     `gorm:"primaryKey;column:post_id"`
	CreatedAt time.Time `gorm:"not null;column:created_at"`

	// Relationships
	AccountObj *Account `gorm:"foreignKey:Account;references:Name"`
	Post       *Post    `gorm:"foreignKey:PostID;references:ID"`
}

// TableName specifies the table name for Reblog
func (Reblog) TableName() string {
	return "hive_reblogs"
}

