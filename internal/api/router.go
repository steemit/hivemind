package api

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/api/bridge"
	"github.com/steemit/hivemind/internal/api/condenser"
	"github.com/steemit/hivemind/internal/api/hive"
	"github.com/steemit/hivemind/internal/cache"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/pkg/logging"
)

// Router sets up API routes
type Router struct {
	handler *JSONRPCHandler
	db      *db.DB
	cache   *cache.Cache
	logger  *zap.Logger
}

// NewRouter creates a new API router
func NewRouter(database *db.DB, redisCache *cache.Cache) *Router {
	handler := NewJSONRPCHandler()
	router := &Router{
		handler: handler,
		db:      database,
		cache:   redisCache,
		logger:  logging.GetLogger().With(zap.String("component", "api-router")),
	}

	// Register all API methods
	router.registerMethods()

	return router
}

// SetupRoutes sets up all API routes
func (r *Router) SetupRoutes(engine *gin.Engine) {
	// Health check endpoints
	engine.GET("/health", r.healthHandler)
	engine.GET("/.well-known/healthcheck.json", r.healthHandler)

	// JSON-RPC endpoint
	engine.POST("/", r.handler.Handle)
}

// registerMethods registers all API methods
func (r *Router) registerMethods() {
	repo := db.NewRepository(r.db.DB)

	// Hive core API
	r.handler.RegisterMethod("hive.db_head_state", r.dbHeadState)

	// Condenser API
	condenserFollow := condenser.NewFollowAPI(repo)
	condenserContent := condenser.NewContentAPI(repo)
	condenserDiscussions := condenser.NewDiscussionsAPI(repo, r.db)

	r.handler.RegisterMethod("condenser_api.get_followers", condenserFollow.GetFollowers)
	r.handler.RegisterMethod("condenser_api.get_following", condenserFollow.GetFollowing)
	r.handler.RegisterMethod("condenser_api.get_follow_count", condenserFollow.GetFollowCount)
	r.handler.RegisterMethod("condenser_api.get_reblogged_by", condenserFollow.GetRebloggedBy)
	r.handler.RegisterMethod("condenser_api.get_content", condenserContent.GetContent)
	r.handler.RegisterMethod("condenser_api.get_content_replies", condenserContent.GetContentReplies)
	
	// Discussion queries
	r.handler.RegisterMethod("condenser_api.get_discussions_by_trending", condenserDiscussions.GetDiscussionsByTrending)
	r.handler.RegisterMethod("condenser_api.get_discussions_by_hot", condenserDiscussions.GetDiscussionsByHot)
	r.handler.RegisterMethod("condenser_api.get_discussions_by_created", condenserDiscussions.GetDiscussionsByCreated)
	r.handler.RegisterMethod("condenser_api.get_discussions_by_promoted", condenserDiscussions.GetDiscussionsByPromoted)
	r.handler.RegisterMethod("condenser_api.get_discussions_by_blog", condenserDiscussions.GetDiscussionsByBlog)
	r.handler.RegisterMethod("condenser_api.get_discussions_by_feed", condenserDiscussions.GetDiscussionsByFeed)
	
	// Blog and Tags API
	condenserBlog := condenser.NewBlogAPI(repo, r.db)
	condenserTags := condenser.NewTagsAPI(repo, r.db)
	
	r.handler.RegisterMethod("condenser_api.get_blog", condenserBlog.GetBlog)
	r.handler.RegisterMethod("condenser_api.get_blog_entries", condenserBlog.GetBlogEntries)
	r.handler.RegisterMethod("condenser_api.get_trending_tags", condenserTags.GetTrendingTags)
	r.handler.RegisterMethod("condenser_api.get_account_reputations", condenserTags.GetAccountReputations)
	
	// Follow API aliases for blog
	r.handler.RegisterMethod("follow_api.get_blog", condenserBlog.GetBlog)
	r.handler.RegisterMethod("follow_api.get_blog_entries", condenserBlog.GetBlogEntries)
	
	// Misc API
	condenserMisc := condenser.NewMiscAPI(repo, r.db)
	
	r.handler.RegisterMethod("condenser_api.get_discussions_by_comments", condenserMisc.GetDiscussionsByComments)
	r.handler.RegisterMethod("condenser_api.get_replies_by_last_update", condenserMisc.GetRepliesByLastUpdate)
	r.handler.RegisterMethod("condenser_api.get_discussions_by_author_before_date", condenserMisc.GetDiscussionsByAuthorBeforeDate)
	r.handler.RegisterMethod("condenser_api.get_post_discussions_by_payout", condenserMisc.GetPostDiscussionsByPayout)
	r.handler.RegisterMethod("condenser_api.get_comment_discussions_by_payout", condenserMisc.GetCommentDiscussionsByPayout)
	r.handler.RegisterMethod("condenser_api.get_transaction", condenserMisc.GetTransaction)
	r.handler.RegisterMethod("condenser_api.get_state", condenserMisc.GetState)
	r.handler.RegisterMethod("condenser_api.get_account_votes", condenserMisc.GetAccountVotes)
	
	// Tags API aliases
	r.handler.RegisterMethod("tags_api.get_discussions_by_trending", condenserDiscussions.GetDiscussionsByTrending)
	r.handler.RegisterMethod("tags_api.get_discussions_by_hot", condenserDiscussions.GetDiscussionsByHot)
	r.handler.RegisterMethod("tags_api.get_discussions_by_created", condenserDiscussions.GetDiscussionsByCreated)
	r.handler.RegisterMethod("tags_api.get_discussions_by_promoted", condenserDiscussions.GetDiscussionsByPromoted)
	r.handler.RegisterMethod("tags_api.get_discussions_by_blog", condenserDiscussions.GetDiscussionsByBlog)
	r.handler.RegisterMethod("tags_api.get_discussions_by_comments", condenserMisc.GetDiscussionsByComments)

	// Follow API aliases
	r.handler.RegisterMethod("follow_api.get_followers", condenserFollow.GetFollowers)
	r.handler.RegisterMethod("follow_api.get_following", condenserFollow.GetFollowing)
	r.handler.RegisterMethod("follow_api.get_follow_count", condenserFollow.GetFollowCount)

	// Bridge API
	bridgePost := bridge.NewPostAPI(repo)
	bridgeProfile := bridge.NewProfileAPI(repo)
	bridgeRanked := bridge.NewRankedAPI(repo, r.db, r.cache)

	r.handler.RegisterMethod("bridge.get_post", bridgePost.GetPost)
	r.handler.RegisterMethod("bridge.get_profile", bridgeProfile.GetProfile)
	r.handler.RegisterMethod("bridge.get_ranked_posts", bridgeRanked.GetRankedPosts)
	r.handler.RegisterMethod("bridge.get_account_posts", bridgeRanked.GetAccountPosts)
	r.handler.RegisterMethod("bridge.get_trending_topics", bridgeRanked.GetTrendingTopics)

	// Hive API
	hivePublic := hive.NewPublicAPI(repo)
	hiveCommunity := hive.NewCommunityAPI(repo)
	hiveNotify := hive.NewNotifyAPI(repo)

	r.handler.RegisterMethod("hive_api.get_account", hivePublic.GetAccount)
	r.handler.RegisterMethod("hive_api.get_accounts", hivePublic.GetAccounts)
	r.handler.RegisterMethod("hive_api.list_followers", hivePublic.ListFollowers)
	r.handler.RegisterMethod("hive_api.list_following", hivePublic.ListFollowing)
	r.handler.RegisterMethod("hive_api.list_all_muted", hivePublic.ListAllMuted)
	r.handler.RegisterMethod("hive_api.list_account_blog", hivePublic.ListAccountBlog)
	r.handler.RegisterMethod("hive_api.list_account_posts", hivePublic.ListAccountPosts)
	r.handler.RegisterMethod("hive_api.list_account_feed", hivePublic.ListAccountFeed)

	// Community API (also registered under bridge namespace)
	r.handler.RegisterMethod("bridge.get_community", hiveCommunity.GetCommunity)
	r.handler.RegisterMethod("bridge.get_community_context", hiveCommunity.GetCommunityContext)
	r.handler.RegisterMethod("bridge.list_communities", hiveCommunity.ListCommunities)
	r.handler.RegisterMethod("bridge.list_top_communities", hiveCommunity.ListTopCommunities)
	r.handler.RegisterMethod("bridge.list_pop_communities", hiveCommunity.ListPopCommunities)
	r.handler.RegisterMethod("bridge.list_community_roles", hiveCommunity.ListCommunityRoles)
	r.handler.RegisterMethod("bridge.list_subscribers", hiveCommunity.ListSubscribers)
	r.handler.RegisterMethod("bridge.list_all_subscriptions", hiveCommunity.ListAllSubscriptions)

	// Notification API
	r.handler.RegisterMethod("bridge.post_notifications", hiveNotify.PostNotifications)
	r.handler.RegisterMethod("bridge.account_notifications", hiveNotify.AccountNotifications)
	r.handler.RegisterMethod("bridge.unread_notifications", hiveNotify.UnreadNotifications)
}

// healthHandler handles health check requests
func (r *Router) healthHandler(c *gin.Context) {
	c.JSON(200, gin.H{
		"status":  "OK",
		"service": "hivemind-api",
	})
}

// dbHeadState returns database head state
func (r *Router) dbHeadState(c *gin.Context, params json.RawMessage) (interface{}, error) {
	// TODO: Implement database head state query
	return gin.H{
		"db_head_block": 0,
		"db_head_time":  "",
		"db_head_age":   0,
	}, nil
}

