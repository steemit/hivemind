package models

import (
	"sort"
	"testing"
)

// TestTableNameUniqueness ensures no two models accidentally share a table
// name (a common copy-paste bug when adding models).
func TestTableNameUniqueness(t *testing.T) {
	tables := []string{
		(State{}).TableName(),
		(Block{}).TableName(),
		(Account{}).TableName(),
		(Post{}).TableName(),
		(PostTag{}).TableName(),
		(Follow{}).TableName(),
		(Reblog{}).TableName(),
		(Payment{}).TableName(),
		(FeedCache{}).TableName(),
		(PostCache{}).TableName(),
		(PostCacheTemp{}).TableName(),
		(Community{}).TableName(),
		(Role{}).TableName(),
		(Subscription{}).TableName(),
		(Notification{}).TableName(),
		(PostStatus{}).TableName(),
		(TransactionBlock{}).TableName(),
	}
	sort.Strings(tables)
	for i := 1; i < len(tables); i++ {
		if tables[i] == tables[i-1] {
			t.Errorf("duplicate table name: %q", tables[i])
		}
	}
}

// TestPostStatusListTypeConstants pins the list_type values to the legacy
// semantics so future refactors cannot silently shift them.
func TestPostStatusListTypeConstants(t *testing.T) {
	if PostStatusArticleBlock != 1 {
		t.Errorf("PostStatusArticleBlock = %d, want 1", PostStatusArticleBlock)
	}
	if PostStatusArticlePin != 2 {
		t.Errorf("PostStatusArticlePin = %d, want 2", PostStatusArticlePin)
	}
	if PostStatusUserBlock != 3 {
		t.Errorf("PostStatusUserBlock = %d, want 3", PostStatusUserBlock)
	}
}

// TestFollowStateConstants pins the state bitfield values and documents that
// the schema default is blog follow (1).
func TestFollowStateConstants(t *testing.T) {
	if FollowStateBlog != 1 {
		t.Errorf("FollowStateBlog = %d, want 1 (schema default)", FollowStateBlog)
	}
	if FollowStateIgnore != 2 {
		t.Errorf("FollowStateIgnore = %d, want 2", FollowStateIgnore)
	}
	if FollowStateBoth != 3 {
		t.Errorf("FollowStateBoth = %d, want 3", FollowStateBoth)
	}
}
