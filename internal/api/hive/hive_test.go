package hive

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/internal/models"
)

// newTestDB creates a dedicated database (hive_hive_test) for this package so
// it can run concurrently with other packages' integration suites.
func newTestDB(t *testing.T) string {
	t.Helper()
	baseURL := os.Getenv("HIVE_TEST_DATABASE_URL")

	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse db url: %v", err)
	}
	u.Path = "/postgres"
	maintDSN := u.String()

	raw, err := sql.Open("pgx", maintDSN)
	if err != nil {
		t.Fatalf("open maintenance conn: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP DATABASE IF EXISTS hive_hive_test;`); err != nil {
		t.Fatalf("drop test db: %v", err)
	}
	if _, err := raw.Exec(`CREATE DATABASE hive_hive_test;`); err != nil {
		t.Fatalf("create test db: %v", err)
	}

	u.Path = "/hive_hive_test"
	return u.String()
}

func resetSchema(t *testing.T, dbURL string) {
	t.Helper()
	raw, err := sql.Open("pgx", dbURL)
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
}

// call invokes a hive API handler with a JSON params map.
func call(t *testing.T, handler func(*gin.Context, json.RawMessage) (interface{}, error), params map[string]interface{}) interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(w)
	ctx.Request = httptest.NewRequest("POST", "/", nil)
	raw, _ := json.Marshal(params)
	result, err := handler(ctx, raw)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

// TestCommunityAndPublicMethods covers the hive_api community methods and
// the follow/blog list methods against a real Postgres schema.
//
// Requires a live Postgres; skipped when HIVE_TEST_DATABASE_URL is not set.
func TestCommunityAndPublicMethods(t *testing.T) {
	if os.Getenv("HIVE_TEST_DATABASE_URL") == "" {
		t.Skip("HIVE_TEST_DATABASE_URL not set; skipping hive_api integration test")
	}
	dbURL := newTestDB(t)
	resetSchema(t, dbURL)
	if err := db.RunMigrations(dbURL, 0); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gdb, err := gorm.Open(postgres.Open(dbURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open gorm: %v", err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	defer sqlDB.Close()

	seedHiveFixture(t, gdb)

	gin.SetMode(gin.TestMode)
	repo := db.NewRepository(gdb)
	wrapped := &db.DB{DB: gdb}
	public := NewPublicAPI(repo, wrapped)
	community := NewCommunityAPI(repo, "hive-123456")

	// --- community methods ---

	comm := call(t, community.GetCommunity, map[string]interface{}{"name": "hive-123456"}).(map[string]interface{})
	if comm["name"] != "hive-123456" || comm["title"] != "Test Community" {
		t.Errorf("get_community: got %v", comm)
	}
	team, ok := comm["team"].([]interface{})
	if !ok || len(team) != 1 {
		t.Fatalf("get_community: expected team of 1, got %v", comm["team"])
	}
	if team[0].([]interface{})[0] != "alice" || team[0].([]interface{})[1] != "owner" {
		t.Errorf("get_community team: got %v", team[0])
	}

	// Observer context: alice is owner + subscribed.
	commObs := call(t, community.GetCommunity, map[string]interface{}{"name": "hive-123456", "observer": "alice"}).(map[string]interface{})
	context := commObs["context"].(map[string]interface{})
	if context["role"] != "owner" || context["subscribed"] != true {
		t.Errorf("get_community observer context: got %v", context)
	}

	ctxRes := call(t, community.GetCommunityContext, map[string]interface{}{"name": "hive-123456", "account": "bob"}).(map[string]interface{})
	if ctxRes["role"] != "member" || ctxRes["subscribed"] != true {
		t.Errorf("get_community_context bob: got %v", ctxRes)
	}
	ctxRes = call(t, community.GetCommunityContext, map[string]interface{}{"name": "hive-123456", "account": "carol"}).(map[string]interface{})
	if ctxRes["role"] != "guest" || ctxRes["subscribed"] != false {
		t.Errorf("get_community_context carol: got %v", ctxRes)
	}

	list := call(t, community.ListCommunities, map[string]interface{}{}).([]interface{})
	if len(list) != 1 || list[0].(map[string]interface{})["name"] != "hive-123456" {
		t.Errorf("list_communities: got %v", list)
	}

	top := call(t, community.ListTopCommunities, map[string]interface{}{"limit": 5}).([]interface{})
	if len(top) != 1 || top[0].([]interface{})[0] != "hive-123456" {
		t.Errorf("list_top_communities: got %v", top)
	}

	pop := call(t, community.ListPopCommunities, map[string]interface{}{}).([]interface{})
	if len(pop) != 1 || pop[0].([]interface{})[0] != "hive-123456" {
		t.Errorf("list_pop_communities: got %v", pop)
	}

	roles := call(t, community.ListCommunityRoles, map[string]interface{}{"community": "hive-123456"}).([]interface{})
	if len(roles) != 2 { // alice owner, bob member
		t.Fatalf("list_community_roles: expected 2, got %v", roles)
	}
	if roles[0].([]interface{})[0] != "alice" || roles[0].([]interface{})[1] != "owner" {
		t.Errorf("list_community_roles order: got %v", roles[0])
	}

	subs := call(t, community.ListSubscribers, map[string]interface{}{"community": "hive-123456"}).([]interface{})
	if len(subs) != 2 {
		t.Fatalf("list_subscribers: expected 2, got %v", subs)
	}

	allSubs := call(t, community.ListAllSubscriptions, map[string]interface{}{"account": "bob"}).([]interface{})
	if len(allSubs) != 1 {
		t.Fatalf("list_all_subscriptions: expected 1, got %v", allSubs)
	}
	if allSubs[0].([]interface{})[0] != "hive-123456" || allSubs[0].([]interface{})[2] != "member" {
		t.Errorf("list_all_subscriptions: got %v", allSubs[0])
	}

	// --- public methods ---

	acc := call(t, public.GetAccount, map[string]interface{}{"name": "alice"}).(map[string]interface{})
	if acc["name"] != "alice" || acc["location"] != nil && acc["location"] != "" {
		t.Errorf("get_account: got %v", acc)
	}
	if _, ok := acc["website"]; !ok {
		t.Errorf("get_account: non-lite profile fields missing: %v", acc)
	}

	accs := call(t, public.GetAccounts, map[string]interface{}{"names": []interface{}{"alice", "bob"}}).([]map[string]interface{})
	if len(accs) != 2 || accs[0]["name"] != "alice" {
		t.Fatalf("get_accounts: got %v", accs)
	}
	if _, ok := accs[0]["website"]; ok {
		t.Errorf("get_accounts: lite shape should omit website")
	}

	followers := call(t, public.ListFollowers, map[string]interface{}{"account": "bob"}).([]map[string]interface{})
	if len(followers) != 1 || followers[0]["name"] != "alice" {
		t.Fatalf("list_followers: got %v", followers)
	}
	// Pure-ignore (state=2) follows are excluded from list_following
	// (legacy filters state IN (1,3) for the 'blog' follow type).
	following := call(t, public.ListFollowing, map[string]interface{}{"account": "alice"}).([]map[string]interface{})
	if len(following) != 2 || following[0]["name"] != "bob" || following[1]["name"] != "carol" {
		t.Fatalf("list_following: got %v", following)
	}

	muted := call(t, public.ListAllMuted, map[string]interface{}{"account": "alice"}).([]string)
	if len(muted) != 1 || muted[0] != "mallory" {
		t.Fatalf("list_all_muted: got %v", muted)
	}

	// blog: alice's own post + reblogged bob post.
	blog := call(t, public.ListAccountBlog, map[string]interface{}{"account": "alice"}).(map[string]interface{})
	blogPosts := blog["posts"].([]map[string]interface{})
	if len(blogPosts) != 2 || blogPosts[0]["url"] != "bob/post-b" {
		t.Fatalf("list_account_blog: got %v", blogPosts)
	}

	// posts: alice's own comments (comment-x).
	posts := call(t, public.ListAccountPosts, map[string]interface{}{"account": "alice"}).(map[string]interface{})
	postsList := posts["posts"].([]map[string]interface{})
	if len(postsList) != 1 || postsList[0]["url"] != "alice/comment-x" {
		t.Fatalf("list_account_posts: got %v", postsList)
	}

	// feed: bob's post surfaces with reblogged_by attribution.
	feed := call(t, public.ListAccountFeed, map[string]interface{}{"account": "alice"}).(map[string]interface{})
	feedPosts := feed["posts"].([]map[string]interface{})
	if len(feedPosts) != 1 || feedPosts[0]["url"] != "bob/post-b" {
		t.Fatalf("list_account_feed: got %v", feedPosts)
	}
	if rby := feedPosts[0]["reblogged_by"]; rby == nil {
		t.Errorf("list_account_feed: expected reblogged_by on feed post")
	}
}

// seedHiveFixture builds accounts, a community with roles/subscriptions,
// follows (one muted), posts with cache rows, and feed cache entries.
func seedHiveFixture(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()

	for _, name := range []string{"alice", "bob", "carol", "mallory"} {
		if err := gdb.Create(&models.Account{Name: name, CreatedAt: now, Rank: 1}).Error; err != nil {
			t.Fatalf("seed account %s: %v", name, err)
		}
	}
	var alice, bob models.Account
	if err := gdb.Where("name = ?", "alice").First(&alice).Error; err != nil {
		t.Fatalf("load alice: %v", err)
	}
	if err := gdb.Where("name = ?", "bob").First(&bob).Error; err != nil {
		t.Fatalf("load bob: %v", err)
	}

	// Community with owner alice and member bob; both subscribed.
	comm := models.Community{Name: "hive-123456", Title: "Test Community", Rank: 1, CreatedAt: now}
	comm.ID = 123456
	if err := gdb.Create(&comm).Error; err != nil {
		t.Fatalf("seed community: %v", err)
	}
	roles := []models.Role{
		{CommunityID: comm.ID, AccountID: alice.ID, RoleID: 8, CreatedAt: now},
		{CommunityID: comm.ID, AccountID: bob.ID, RoleID: 2, CreatedAt: now},
	}
	for i := range roles {
		if err := gdb.Create(&roles[i]).Error; err != nil {
			t.Fatalf("seed role: %v", err)
		}
	}
	subs := []models.Subscription{
		{CommunityID: comm.ID, AccountID: alice.ID, CreatedAt: now},
		{CommunityID: comm.ID, AccountID: bob.ID, CreatedAt: now},
	}
	for i := range subs {
		if err := gdb.Create(&subs[i]).Error; err != nil {
			t.Fatalf("seed subscription: %v", err)
		}
	}

	// alice follows bob + carol (blog) and mallory (ignored only).
	follows := []models.Follow{
		{FollowerID: alice.ID, FollowingID: bob.ID, State: 1, CreatedAt: now.Add(-2 * time.Hour)},
		{FollowerID: alice.ID, FollowingID: mustAccountID(t, gdb, "carol"), State: 1, CreatedAt: now.Add(-30 * time.Hour)},
		{FollowerID: alice.ID, FollowingID: mustAccountID(t, gdb, "mallory"), State: 2, CreatedAt: now.Add(-1 * time.Hour)},
	}
	for i := range follows {
		if err := gdb.Create(&follows[i]).Error; err != nil {
			t.Fatalf("seed follow: %v", err)
		}
	}

	// Posts: alice/post-a (root), bob/post-b (root), alice/comment-x (comment on bob's).
	mk := func(id int64, parent int64, author, permlink string, depth int16) models.Post {
		p := models.Post{Author: author, Permlink: permlink, Category: "test", CreatedAt: now, Depth: depth}
		p.ID = id
		if parent != 0 {
			p.ParentID.Valid = true
			p.ParentID.Int64 = parent
		}
		return p
	}
	posts := []models.Post{
		mk(1, 0, "alice", "post-a", 0),
		mk(2, 0, "bob", "post-b", 0),
		mk(3, 2, "alice", "comment-x", 1),
	}
	for i := range posts {
		if err := gdb.Create(&posts[i]).Error; err != nil {
			t.Fatalf("seed post: %v", err)
		}
	}

	// Cache rows for the lite post loader.
	caches := []models.PostCache{
		{PostID: 1, Author: "alice", Permlink: "post-a", Category: "test", Title: "A", CreatedAt: now, PayoutAt: now, UpdatedAt: now},
		{PostID: 2, Author: "bob", Permlink: "post-b", Category: "test", Title: "B", CreatedAt: now, PayoutAt: now, UpdatedAt: now},
		{PostID: 3, Author: "alice", Permlink: "comment-x", Category: "test", Title: "C", CreatedAt: now, PayoutAt: now, UpdatedAt: now},
	}
	for i := range caches {
		if err := gdb.Create(&caches[i]).Error; err != nil {
			t.Fatalf("seed cache: %v", err)
		}
	}

	// Blog (alice: own post-a + reblog of bob's post-b) and bob's feed row.
	feeds := []models.FeedCache{
		{PostID: 1, AccountID: alice.ID, CreatedAt: now.Add(-2 * time.Hour)},
		{PostID: 2, AccountID: alice.ID, CreatedAt: now.Add(-1 * time.Hour)},
		{PostID: 2, AccountID: bob.ID, CreatedAt: now.Add(-3 * time.Hour)},
		{PostID: 2, AccountID: mustAccountID(t, gdb, "carol"), CreatedAt: now.Add(-4 * time.Hour)},
	}
	for i := range feeds {
		if err := gdb.Create(&feeds[i]).Error; err != nil {
			t.Fatalf("seed feed: %v", err)
		}
	}
}

func mustAccountID(t *testing.T, gdb *gorm.DB, name string) int64 {
	t.Helper()
	var acc models.Account
	if err := gdb.Where("name = ?", name).First(&acc).Error; err != nil {
		t.Fatalf("load account %s: %v", name, err)
	}
	return acc.ID
}
