package models

import (
	"time"
)

// Block represents a blockchain block
type Block struct {
	Num       int64     `gorm:"primaryKey;autoIncrement:false;column:num"`
	Hash      string    `gorm:"type:char(40);not null;uniqueIndex:hive_blocks_ux1"`
	Prev      *string   `gorm:"type:char(40);column:prev"`
	TXs       int16     `gorm:"type:smallint;not null;default:0;column:txs"`
	Ops       int16     `gorm:"type:smallint;not null;default:0;column:ops"`
	CreatedAt time.Time `gorm:"not null;column:created_at"`

	// Foreign key relationship
	PrevBlock *Block `gorm:"foreignKey:Prev;references:Hash"`
}

// TableName specifies the table name for Block
func (Block) TableName() string {
	return "hive_blocks"
}

