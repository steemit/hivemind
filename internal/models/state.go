package models

import (
	"database/sql"
)

// State represents the database state
type State struct {
	BlockNum      int64          `gorm:"primaryKey;autoIncrement:false;column:block_num"`
	DBVersion     int64          `gorm:"not null;column:db_version"`
	SteemPerMVest float64        `gorm:"type:decimal(8,3);not null;column:steem_per_mvest"`
	USDPerSteem   float64        `gorm:"type:decimal(8,3);not null;column:usd_per_steem"`
	SBDPerSteem   float64        `gorm:"type:decimal(8,3);not null;column:sbd_per_steem"`
	DGPO          sql.NullString `gorm:"type:text;not null;column:dgpo"`
}

// TableName specifies the table name for State
func (State) TableName() string {
	return "hive_state"
}

// PostStatus represents post status flags
type PostStatus struct {
	PostID    int64 `gorm:"primaryKey;column:post_id"`
	IsPinned  bool  `gorm:"not null;default:false;column:is_pinned"`
	IsMuted   bool  `gorm:"not null;default:false;column:is_muted"`
	IsValid   bool  `gorm:"not null;default:true;column:is_valid"`
}

// TableName specifies the table name for PostStatus
func (PostStatus) TableName() string {
	return "hive_posts_status"
}

// TransactionBlock represents transaction ID to block number mapping
type TransactionBlock struct {
	TrxID    string `gorm:"primaryKey;type:varchar(40);column:trx_id"`
	BlockNum int64  `gorm:"not null;column:block_num"`
}

// TableName specifies the table name for TransactionBlock
func (TransactionBlock) TableName() string {
	return "hive_trxid_block_num"
}

