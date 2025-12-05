package api

import (
	"encoding/json"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/api/condenser"
	"github.com/steemit/hivemind/internal/db"
	"github.com/steemit/hivemind/pkg/logging"
)

// Router sets up API routes
type Router struct {
	handler *JSONRPCHandler
	db      *db.DB
	logger  *zap.Logger
}

// NewRouter creates a new API router
func NewRouter(database *db.DB) *Router {
	handler := NewJSONRPCHandler()
	router := &Router{
		handler: handler,
		db:      database,
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

	r.handler.RegisterMethod("condenser_api.get_followers", condenserFollow.GetFollowers)
	r.handler.RegisterMethod("condenser_api.get_following", condenserFollow.GetFollowing)
	r.handler.RegisterMethod("condenser_api.get_follow_count", condenserFollow.GetFollowCount)
	r.handler.RegisterMethod("condenser_api.get_reblogged_by", condenserFollow.GetRebloggedBy)
	r.handler.RegisterMethod("condenser_api.get_content", condenserContent.GetContent)
	r.handler.RegisterMethod("condenser_api.get_content_replies", condenserContent.GetContentReplies)

	// Follow API aliases
	r.handler.RegisterMethod("follow_api.get_followers", condenserFollow.GetFollowers)
	r.handler.RegisterMethod("follow_api.get_following", condenserFollow.GetFollowing)
	r.handler.RegisterMethod("follow_api.get_follow_count", condenserFollow.GetFollowCount)

	// Bridge API - will be implemented in separate files
	// r.handler.RegisterMethod("bridge.get_post", r.getPost)
	// ... more methods

	// Hive API - will be implemented in separate files
	// r.handler.RegisterMethod("hive_api.get_account", r.getAccount)
	// ... more methods
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

