package models

import (
	"database/sql"
	"time"
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

// PostStatus represents an entry in the hive_posts_status table, which tracks
// blacklist/pin/block lists. This is NOT the per-post is_pinned/is_muted/is_valid
// flags (those live on hive_posts); it is a separate append-only status table
// keyed by an auto-increment id with a list_type discriminator.
//
// Mirrors legacy schema.py hive_posts_status (DB_VERSION 29, v20 production
// shape: no UNIQUE constraint, three separate indexes).
type PostStatus struct {
	ID        int64     `gorm:"primaryKey;autoIncrement;column:id"`
	PostID    int64     `gorm:"not null;default:0;column:post_id"`
	Author    string    `gorm:"type:varchar(16);not null;default:'';column:author"`
	ListType  int16     `gorm:"type:smallint;not null;default:0;column:list_type"`
	CreatedAt time.Time `gorm:"not null;default:'1990-01-01';column:created_at"`
}

// TableName specifies the table name for PostStatus
func (PostStatus) TableName() string {
	return "hive_posts_status"
}

// PostStatus list_type values (legacy hive/server/common/helpers.py).
const (
	PostStatusArticleBlock int16 = 1 // Block a specific post
	PostStatusArticlePin   int16 = 2 // Pin a specific post
	PostStatusUserBlock    int16 = 3 // Block all posts by an author
)

// TransactionBlock represents transaction ID to block number mapping.
//
// Mirrors the legacy v19 production shape: trx_id is NULLABLE (a partial unique
// index hive_trxid_ix1 covers non-null values), and there is no primary key.
type TransactionBlock struct {
	TrxID    sql.NullString `gorm:"type:varchar(40);column:trx_id"`
	BlockNum int64          `gorm:"not null;column:block_num"`
}

// TableName specifies the table name for TransactionBlock
func (TransactionBlock) TableName() string {
	return "hive_trxid_block_num"
}
