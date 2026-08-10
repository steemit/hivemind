package models

import (
	"time"
)

// Payment represents a payment operation.
//
// Mirrors legacy schema.py hive_payments: from_account/to_account are INTEGER
// foreign keys referencing hive_accounts.id (not account-name strings). The
// Go indexer resolves account names to IDs before writing.
//
// NOTE: the legacy Python schema does NOT define `memo` or `created_at`
// columns. They are retained here because the existing payment_indexer writes
// them; migration 0002 adds them to the DB so model/indexer/schema stay
// consistent.
type Payment struct {
	ID            int64   `gorm:"primaryKey;autoIncrement;column:id"`
	BlockNum      int64   `gorm:"not null;column:block_num"`
	TXIndex       int16   `gorm:"type:smallint;not null;column:tx_idx"`
	PostID        int64   `gorm:"not null;column:post_id"`
	FromAccountID int64   `gorm:"not null;column:from_account"`
	ToAccountID   int64   `gorm:"not null;column:to_account"`
	Amount        float64 `gorm:"type:decimal(10,3);not null;column:amount"`
	Token         string  `gorm:"type:varchar(5);not null;column:token"`

	// Extra columns written by the Go indexer; added by migration 0002.
	Memo      string    `gorm:"type:varchar(1024);column:memo"`
	CreatedAt time.Time `gorm:"not null;column:created_at"`

	// Relationships
	FromAccount *Account `gorm:"foreignKey:FromAccountID;references:ID"`
	ToAccount   *Account `gorm:"foreignKey:ToAccountID;references:ID"`
	Post        *Post    `gorm:"foreignKey:PostID;references:ID"`
}

// TableName specifies the table name for Payment
func (Payment) TableName() string {
	return "hive_payments"
}
