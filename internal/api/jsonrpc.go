package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/steemit/hivemind/pkg/logging"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// JSONRPCRequest represents a JSON-RPC 2.0 request
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// JSONRPCResponse represents a JSON-RPC 2.0 response
type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC error
type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// MethodHandler is a function that handles a JSON-RPC method
type MethodHandler func(ctx *gin.Context, params json.RawMessage) (interface{}, error)

// JSONRPCHandler handles JSON-RPC requests
type JSONRPCHandler struct {
	methods map[string]MethodHandler
	logger  *zap.Logger
}

// NewJSONRPCHandler creates a new JSON-RPC handler
func NewJSONRPCHandler() *JSONRPCHandler {
	return &JSONRPCHandler{
		methods: make(map[string]MethodHandler),
		logger:  logging.GetLogger().With(zap.String("component", "jsonrpc")),
	}
}

// RegisterMethod registers a method handler
func (h *JSONRPCHandler) RegisterMethod(method string, handler MethodHandler) {
	h.methods[method] = handler
}

// Handle handles a JSON-RPC request
func (h *JSONRPCHandler) Handle(c *gin.Context) {
	_, span := telemetry.StartSpan(c.Request.Context(), "jsonrpc.handle")
	defer span.End()

	var req JSONRPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		h.sendError(c, nil, -32700, "Parse error", err)
		return
	}

	// Validate JSON-RPC version
	if req.JSONRPC != "2.0" {
		h.sendError(c, req.ID, -32600, "Invalid Request", fmt.Errorf("invalid jsonrpc version"))
		return
	}

	// Find method handler
	handler, ok := h.methods[req.Method]
	if !ok {
		h.sendError(c, req.ID, -32601, "Method not found", fmt.Errorf("method %s not found", req.Method))
		return
	}

	// Call handler
	result, err := handler(c, req.Params)
	if err != nil {
		// For now, treat all errors as internal server errors
		// TODO: Add error type checking for better error codes
		h.sendError(c, req.ID, -32000, "Server error", err)
		return
	}

	// Send response
	h.sendResponse(c, req.ID, result)
}

// sendResponse sends a successful JSON-RPC response
func (h *JSONRPCHandler) sendResponse(c *gin.Context, id interface{}, result interface{}) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	c.JSON(http.StatusOK, resp)
}

// sendError sends an error JSON-RPC response
func (h *JSONRPCHandler) sendError(c *gin.Context, id interface{}, code int, message string, err error) {
	if err != nil {
		h.logger.Error("JSON-RPC error", zap.String("message", message), zap.Error(err))
	}

	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    err.Error(),
		},
	}
	c.JSON(http.StatusOK, resp)
}

// Standard JSON-RPC error codes
const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternalError  = -32603
)

