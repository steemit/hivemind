package models

import (
	"time"
)

// Payment represents a payment operation
type Payment struct {
	ID        int64     `gorm:"primaryKey;autoIncrement;column:id"`
	BlockNum  int64     `gorm:"not null;column:block_num"`
	TXIndex   int16     `gorm:"type:smallint;not null;column:tx_idx"`
	From      string    `gorm:"type:varchar(16);not null;column:from_account"`
	To        string    `gorm:"type:varchar(16);not null;column:to_account"`
	Amount    float64   `gorm:"type:decimal(10,3);not null;column:amount"`
	Token     string    `gorm:"type:varchar(5);not null;column:token"`
	Memo      string    `gorm:"type:varchar(1024);column:memo"`
	PostID    int64     `gorm:"column:post_id"`
	CreatedAt time.Time `gorm:"not null;column:created_at"`

	// Relationships
	FromAccount *Account `gorm:"foreignKey:From;references:Name"`
	ToAccount   *Account `gorm:"foreignKey:To;references:Name"`
	Post        *Post    `gorm:"foreignKey:PostID;references:ID"`
}

// TableName specifies the table name for Payment
func (Payment) TableName() string {
	return "hive_payments"
}

