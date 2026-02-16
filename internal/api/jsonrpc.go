package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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
	JSONRPC string       `json:"jsonrpc"`
	ID      interface{}  `json:"id"`
	Result  interface{}  `json:"result,omitempty"`
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
	startTime := time.Now()

	// Create span for JSON-RPC handling
	ctx, span := telemetry.StartSpanWithName(c.Request.Context(), "jsonrpc.handle")
	defer span.End()

	// Update context with span
	c.Request = c.Request.WithContext(ctx)

	var req JSONRPCRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		telemetry.RecordError("jsonrpc", "parse", "parse_error")
		h.sendError(c, nil, -32700, "Parse error", err)
		return
	}

	// Add method to span attributes
	span.SetAttributes(
		attribute.String("rpc.method", req.Method),
		attribute.String("rpc.jsonrpc", req.JSONRPC),
	)

	// Record params as span event
	telemetry.RecordSpanParams(span, req.Params)

	// Validate JSON-RPC version
	if req.JSONRPC != "2.0" {
		telemetry.RecordError("jsonrpc", req.Method, "invalid_request")
		h.sendError(c, req.ID, -32600, "Invalid Request", fmt.Errorf("invalid jsonrpc version"))
		return
	}

	// Parse namespace and method
	namespace, method := parseMethod(req.Method)
	span.SetAttributes(
		attribute.String("rpc.namespace", namespace),
		attribute.String("rpc.method_name", method),
	)

	// Find method handler
	handler, ok := h.methods[req.Method]
	if !ok {
		telemetry.RecordError(namespace, method, "method_not_found")
		h.sendError(c, req.ID, -32601, "Method not found", fmt.Errorf("method %s not found", req.Method))
		return
	}

	// Create child span for method execution
	methodCtx, methodSpan := telemetry.StartSpanWithName(ctx, req.Method)
	defer methodSpan.End()

	// Call handler with context containing span
	c.Request = c.Request.WithContext(methodCtx)
	result, err := handler(c, req.Params)

	// Calculate duration
	duration := time.Since(startTime).Seconds()

	if err != nil {
		telemetry.RecordError(namespace, method, "server_error")
		telemetry.RecordRequest(namespace, method, "error", duration)
		telemetry.RecordSpanError(methodSpan, err)
		h.sendError(c, req.ID, -32000, "Server error", err)
		return
	}

	// Record success metrics
	telemetry.RecordRequest(namespace, method, "success", duration)
	telemetry.SetSpanSuccess(methodSpan)

	// Send response
	h.sendResponse(c, req.ID, result)
}

// parseMethod parses a method string into namespace and method name
func parseMethod(fullMethod string) (namespace, method string) {
	parts := strings.SplitN(fullMethod, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "unknown", fullMethod
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
	// Record error in current span if available
	span := trace.SpanFromContext(c.Request.Context())
	if span.IsRecording() {
		span.SetStatus(codes.Error, message)
		if err != nil {
			span.RecordError(err)
		}
	}

	if err != nil {
		h.logger.Error("JSON-RPC error",
			zap.String("message", message),
			zap.Int("code", code),
			zap.Error(err))
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
