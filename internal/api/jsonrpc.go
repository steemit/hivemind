// JSON-RPC 2.0 framework: request parsing, batching, notifications, error
// mapping and HTTP transport behavior. All wire-level semantics follow the
// legacy stack (hive/server/serve.py + jsonrpcserver 4.0.1 behind aiohttp):
//   - arrays are dispatched as batches; the response is an array with one
//     entry per non-notification request, in request order;
//   - notifications (no id member) are executed but never answered: a body
//     consisting of a single notification yields an empty HTTP 200 body,
//     an all-notification batch yields `[]`;
//   - an empty array `[]` is rejected with a single -32600 error object
//     (legacy request schema requires minItems=1);
//   - every JSON-RPC answer is HTTP 200 with Access-Control-Allow-Origin: *
//     (legacy serve.py:264), except bodies over the configured cap which
//     are refused with HTTP 413 before parsing (§5.5);
//   - error codes/messages mirror jsonrpcserver 4.0.1 (response.py):
//     -32700 "Invalid JSON", -32600 "Invalid JSON-RPC", -32601
//     "Method not found", -32602 "Invalid parameters" (input validation:
//     legacy AssertionError/TypeError == Go apierrors.PublicError), -32000
//     "Server error" (any other handler failure; legacy never emits -32603);
//   - a handler panic is isolated to its own item: it is logged with a
//     stack and mapped to that item's -32000 "Server error" response
//     (legacy handle_exceptions maps every raised exception per item and
//     the batch survives), while gin.Recovery stays installed as the
//     process-level backstop;
//   - error.data alignment with legacy debug=True dispatch: -32601 data is
//     the bare requested method name (dispatcher.py:122-124 passes
//     data=request.method), -32700 data is the JSON parse error message
//     (dispatcher.py:222), -32600 data is null (dispatcher.py:224);
//   - unknown method names never reach Prometheus labels: the not-found
//     path records a single fixed error-counter series, because both the
//     method label and the namespace label are attacker-controlled there
//     (unbounded cardinality).

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"

	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/pkg/logging"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// maxBatchRequests caps how many calls a single JSON-RPC batch body may
// carry. The legacy stack (aiohttp + jsonrpcserver 4.0.1) has no explicit
// batch-item cap — its only bound is the 1MB aiohttp client_max_size. Go
// enforces an explicit item cap on top of the body-size cap as a pre-auth
// abuse guard.
const maxBatchRequests = 100

// DefaultMaxBodyBytes is the fallback request-body cap used when
// NewJSONRPCHandler is called with a non-positive limit (e.g. tests). The
// production default lives in pkg/config (HIVE_HTTP_MAX_BODY_BYTES, same
// 10MB value); pkg/ must not import internal/, so the constant is declared
// on both sides on purpose.
const DefaultMaxBodyBytes int64 = 10 << 20 // 10MB

// JSONRPCRequest represents a JSON-RPC 2.0 request. ID is kept as raw JSON
// so a missing id member (a notification) is distinguishable from an
// explicit "id": null, which legacy jsonrpcserver 4.0.1 treats as a
// regular request id (request.py: NOID sentinel vs None).
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// isNotification reports whether the request carries no id member and is
// therefore a JSON-RPC notification: it may be executed but must never be
// answered, not even on error (legacy handle_exceptions finally-clause).
func (r *JSONRPCRequest) isNotification() bool {
	return len(r.ID) == 0
}

// JSONRPCResponse represents a JSON-RPC 2.0 response. See MarshalJSON for
// the result/error key shape.
type JSONRPCResponse struct {
	JSONRPC string
	ID      json.RawMessage
	Result  interface{}
	Error   *JSONRPCError
}

type jsonrpcResponseResult struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result"`
}

type jsonrpcResponseError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   *JSONRPCError   `json:"error"`
}

// MarshalJSON serializes a response with exactly one of the "result" or
// "error" members:
//   - success responses always include "result", even when the handler
//     returned nil, which must serialize as "result": null. The legacy
//     server always emits the result key; the previous
//     `json:"result,omitempty"` tag silently dropped it for nil results
//     and broke fail-closed response comparison (P1-18);
//   - error responses never include "result".
func (r JSONRPCResponse) MarshalJSON() ([]byte, error) {
	if r.Error != nil {
		return json.Marshal(jsonrpcResponseError{
			JSONRPC: r.JSONRPC,
			ID:      r.ID,
			Error:   r.Error,
		})
	}
	return json.Marshal(jsonrpcResponseResult{
		JSONRPC: r.JSONRPC,
		ID:      r.ID,
		Result:  r.Result,
	})
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
	methods      map[string]MethodHandler
	logger       *zap.Logger
	maxBodyBytes int64
}

// NewJSONRPCHandler creates a new JSON-RPC handler. maxBodyBytes caps the
// request body (http.MaxBytesReader, HTTP 413 beyond it); a non-positive
// value selects DefaultMaxBodyBytes.
func NewJSONRPCHandler(maxBodyBytes int64) *JSONRPCHandler {
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	return &JSONRPCHandler{
		methods:      make(map[string]MethodHandler),
		logger:       logging.GetLogger().With(zap.String("component", "jsonrpc")),
		maxBodyBytes: maxBodyBytes,
	}
}

// RegisterMethod registers a method handler
func (h *JSONRPCHandler) RegisterMethod(method string, handler MethodHandler) {
	h.methods[method] = handler
}

// RegisteredMethods returns the sorted names of every registered method.
// The registry golden test locks this set against the legacy server's
// method registry (serve.py build_methods) plus the registered Go-only
// additions, so any registry change has to be made explicitly.
func (h *JSONRPCHandler) RegisteredMethods() []string {
	names := make([]string, 0, len(h.methods))
	for name := range h.methods {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Handle handles a JSON-RPC request or batch of requests (POST /).
func (h *JSONRPCHandler) Handle(c *gin.Context) {
	ctx, span := telemetry.StartSpanWithName(c.Request.Context(), "jsonrpc.handle")
	defer span.End()
	c.Request = c.Request.WithContext(ctx)

	body, ok := h.readBody(c)
	if !ok {
		return
	}

	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		h.handleBatch(c, span, trimmed)
		return
	}
	h.handleSingle(c, span, trimmed)
}

// readBody enforces the request-body cap (§5.5) and returns the raw body.
// It writes the HTTP response itself and returns false when the request
// must not be dispatched. The cap is applied before any JSON parsing so an
// oversized payload cannot allocate unbounded memory pre-auth; legacy
// relies on aiohttp's 1MB client_max_size (also a transport-level 413).
func (h *JSONRPCHandler) readBody(c *gin.Context) ([]byte, bool) {
	if c.Request.Body != nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.maxBodyBytes)
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			telemetry.RecordError("jsonrpc", "http", "body_too_large")
			h.logger.Warn("request body exceeds limit",
				zap.Int64("limit", h.maxBodyBytes))
			c.Header("Access-Control-Allow-Origin", "*")
			// Transport-level rejection (same as legacy aiohttp 413);
			// not a JSON-RPC error object.
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return nil, false
		}
		telemetry.RecordError("jsonrpc", "http", "parse_error")
		h.logger.Warn("failed to read request body", zap.Error(err))
		// Do NOT surface err here: transport-level read failures carry
		// server socket details ("read tcp a:b->c:d: ..."), which the
		// #396 sanitizer must keep out of responses.
		h.sendErrorResponse(c, parseErrorResponse(nil))
		return nil, false
	}
	return body, true
}

// handleSingle dispatches one request object.
func (h *JSONRPCHandler) handleSingle(c *gin.Context, span trace.Span, body []byte) {
	req, errResp := parseJSONRPCRequest(body)
	if errResp != nil {
		recordFrameworkError(span, errResp)
		telemetry.RecordError("jsonrpc", "http", frameworkErrorReason(errResp))
		h.logger.Debug("rejected malformed JSON-RPC request",
			zap.Int("code", errResp.Error.Code),
			zap.String("message", errResp.Error.Message))
		h.sendErrorResponse(c, errResp)
		return
	}

	resp := h.executeRequest(c, span, req)
	if resp == nil {
		// Notification: executed, never answered. Legacy returns an
		// empty HTTP 200 body (serve.py `return web.Response()`).
		c.Status(http.StatusOK)
		return
	}
	h.sendResponse(c, resp)
}

// handleBatch dispatches an array body item by item.
func (h *JSONRPCHandler) handleBatch(c *gin.Context, span trace.Span, body []byte) {
	var items []json.RawMessage
	if err := json.Unmarshal(body, &items); err != nil {
		resp := parseErrorResponse(err)
		recordFrameworkError(span, resp)
		telemetry.RecordError("jsonrpc", "batch", "parse_error")
		h.sendErrorResponse(c, resp)
		return
	}
	span.SetAttributes(attribute.Int("rpc.batch_size", len(items)))

	if len(items) == 0 {
		// Legacy request schema requires minItems=1: an empty array is a
		// single -32600 error object, not an empty batch response.
		resp := frameworkErrorResponse(ErrInvalidRequest, msgInvalidJSONRPC)
		recordFrameworkError(span, resp)
		telemetry.RecordError("jsonrpc", "batch", "empty_batch")
		h.sendErrorResponse(c, resp)
		return
	}
	if len(items) > maxBatchRequests {
		resp := errorResponse(nil, ErrInvalidRequest, msgInvalidJSONRPC,
			apierrors.Publicf("batch of %d calls exceeds limit of %d", len(items), maxBatchRequests))
		recordFrameworkError(span, resp)
		telemetry.RecordError("jsonrpc", "batch", "batch_too_large")
		h.logger.Warn("batch request exceeds item limit",
			zap.Int("size", len(items)),
			zap.Int("limit", maxBatchRequests))
		h.sendErrorResponse(c, resp)
		return
	}

	responses := make([]JSONRPCResponse, 0, len(items))
	for _, item := range items {
		req, errResp := parseJSONRPCRequest(item)
		if errResp != nil {
			// Structural failure: answer this item with id null; the
			// rest of the batch is unaffected. (Legacy schema
			// validation rejects the WHOLE batch; the per-item answer
			// is the JSON-RPC 2.0 spec behavior and strictly more
			// robust — registered deviation.)
			responses = append(responses, *errResp)
			continue
		}
		if resp := h.executeRequest(c, span, req); resp != nil {
			responses = append(responses, *resp)
		}
	}

	// An all-notification batch yields an empty array: legacy
	// BatchResponse.wanted is always true, so serve.py serializes `[]`
	// with HTTP 200.
	c.Header("Access-Control-Allow-Origin", "*")
	c.JSON(http.StatusOK, responses)
}

// executeRequest dispatches one parsed request and returns the response —
// or nil when the request is a notification: notifications are executed
// but never answered, not even on error (legacy handle_exceptions
// finally-clause replaces any response with NotificationResponse).
func (h *JSONRPCHandler) executeRequest(c *gin.Context, span trace.Span, req *JSONRPCRequest) *JSONRPCResponse {
	resp := h.dispatchRequest(c, span, req)
	if req.isNotification() {
		return nil
	}
	return resp
}

// dispatchRequest runs one request through lookup, spans and error
// mapping and always returns a response object (caller decides whether to
// send it).
func (h *JSONRPCHandler) dispatchRequest(c *gin.Context, span trace.Span, req *JSONRPCRequest) *JSONRPCResponse {
	start := time.Now()

	span.SetAttributes(
		attribute.String("rpc.method", req.Method),
		attribute.String("rpc.jsonrpc", req.JSONRPC),
	)
	telemetry.RecordSpanParams(span, req.Params)

	namespace, method := parseMethod(req.Method)
	span.SetAttributes(
		attribute.String("rpc.namespace", namespace),
		attribute.String("rpc.method_name", method),
	)
	if req.isNotification() {
		span.SetAttributes(attribute.Bool("rpc.notification", true))
	}

	handler, ok := h.methods[req.Method]
	if !ok {
		// Metrics cardinality guard: the requested method string is
		// attacker-controlled, and so is its namespace (the segment
		// before the first dot), so neither may become a Prometheus
		// label here — an unauthenticated caller could otherwise mint
		// unlimited hivemind_requests_total / histogram series that
		// stay resident forever. Record one fixed series on the error
		// counter only and skip the request counter and histogram.
		telemetry.RecordError("unknown", "unknown", "method_not_found")
		// Legacy debug dispatch answers with data = the bare method name
		// (dispatcher.py:122-124: data=request.method).
		return errorResponse(req.ID, ErrMethodNotFound, msgMethodNotFound, req.Method)
	}

	methodCtx, methodSpan := telemetry.StartSpanWithName(c.Request.Context(), req.Method)
	defer methodSpan.End()
	c.Request = c.Request.WithContext(methodCtx)
	result, err := h.callHandler(c, req, handler)

	duration := time.Since(start).Seconds()
	if err != nil {
		// Method-level refusals (apierrors.NotSupportedError: dummy
		// endpoints, not-implemented methods) answer exactly like the
		// legacy ApiError class: jsonrpcserver 4.0.1 routes them through
		// ExceptionResponse → -32000 "Server error". The legacy response
		// additionally carries debug data ("ApiError: <message>"); Go
		// deliberately omits error.data on -32000 responses — a
		// registered divergence whitelisted in the comparison tooling
		// (the comparator judges error.message only; planned PR#421).
		var notSupported apierrors.NotSupportedError
		if errors.As(err, &notSupported) {
			telemetry.RecordError(namespace, method, "not_supported")
			telemetry.RecordRequest(namespace, method, "error", duration)
			telemetry.RecordSpanError(methodSpan, err)
			h.logger.Info("JSON-RPC method refused (not supported)",
				zap.String("method", req.Method),
				zap.Int("code", ErrServerError),
				zap.Error(err))
			return errorResponse(req.ID, ErrServerError, msgServerError, nil)
		}

		// Legacy dispatcher (jsonrpcserver 4.0.1 handle_exceptions):
		// AssertionError/TypeError (input validation) map to -32602
		// Invalid parameters; every other exception maps to -32000
		// Server error. Go's apierrors.PublicError is the counterpart
		// of the validation class. Legacy never emits -32603, so
		// neither do we (ErrInternalError stays spec-reserved).
		var pub apierrors.PublicError
		if errors.As(err, &pub) {
			telemetry.RecordError(namespace, method, "invalid_params")
			telemetry.RecordRequest(namespace, method, "error", duration)
			telemetry.RecordSpanError(methodSpan, err)
			h.logger.Debug("JSON-RPC invalid params",
				zap.String("method", req.Method), zap.Error(err))
			return errorResponse(req.ID, ErrInvalidParams, msgInvalidParams, pub)
		}
		telemetry.RecordError(namespace, method, "server_error")
		telemetry.RecordRequest(namespace, method, "error", duration)
		telemetry.RecordSpanError(methodSpan, err)
		h.logger.Error("JSON-RPC server error",
			zap.String("method", req.Method),
			zap.Int("code", ErrServerError),
			zap.Error(err))
		return errorResponse(req.ID, ErrServerError, msgServerError, nil)
	}

	telemetry.RecordRequest(namespace, method, "success", duration)
	telemetry.SetSpanSuccess(methodSpan)

	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result:  result,
	}
}

// handlerPanicError wraps a panic value recovered from a method handler
// so dispatchRequest's regular error mapping turns it into a per-item
// -32000 "Server error" response.
type handlerPanicError struct {
	value interface{}
}

func (e *handlerPanicError) Error() string {
	return fmt.Sprintf("jsonrpc handler panic: %v", e.value)
}

// callHandler invokes one method handler with per-item panic isolation.
// Legacy handle_exceptions (dispatcher.py:117-135) maps any exception a
// method raises to that item's -32000 response and lets the rest of the
// batch survive; without this guard a Go panic unwinds straight to
// gin.Recovery, which aborts the whole HTTP request with an empty 500
// body and discards every response already computed. The panic is
// converted to an error after being logged with its stack; gin.Recovery
// remains installed as the process-level backstop for panics outside
// handler invocation.
func (h *JSONRPCHandler) callHandler(c *gin.Context, req *JSONRPCRequest, handler MethodHandler) (result interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("JSON-RPC handler panicked",
				zap.String("method", req.Method),
				zap.Any("panic", r),
				zap.Stack("stack"))
			err = &handlerPanicError{value: r}
		}
	}()
	return handler(c, req.Params)
}

// parseMethod parses a method string into namespace and method name
func parseMethod(fullMethod string) (namespace, method string) {
	parts := strings.SplitN(fullMethod, ".", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "unknown", fullMethod
}

// parseJSONRPCRequest parses and validates a single request object,
// mirroring the legacy jsonrpcserver 4.0.1 request schema:
//   - jsonrpc must be the literal "2.0";
//   - method must be present (an empty-but-present string is allowed and
//     later fails with -32601, exactly as in legacy);
//   - id, when present, must be a string, number or null;
//   - params, when present, must be an array or object (null is rejected,
//     per the legacy schema's type: ["array","object"]).
//
// On failure it returns the JSON-RPC error response with id null:
// -32700 for undecodable JSON, -32600 for schema violations.
func parseJSONRPCRequest(body []byte) (*JSONRPCRequest, *JSONRPCResponse) {
	var raw struct {
		JSONRPC *string         `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  *string         `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		if json.Valid(body) {
			// Valid JSON that is not a request object (bare string,
			// number, null, array item, or wrong member types).
			return nil, frameworkErrorResponse(ErrInvalidRequest, msgInvalidJSONRPC)
		}
		return nil, parseErrorResponse(err)
	}
	if raw.JSONRPC == nil || *raw.JSONRPC != "2.0" {
		return nil, frameworkErrorResponse(ErrInvalidRequest, msgInvalidJSONRPC)
	}
	if raw.Method == nil {
		return nil, frameworkErrorResponse(ErrInvalidRequest, msgInvalidJSONRPC)
	}
	if !validJSONRPCID(raw.ID) {
		return nil, frameworkErrorResponse(ErrInvalidRequest, msgInvalidJSONRPC)
	}
	if !validJSONRPCParams(raw.Params) {
		return nil, frameworkErrorResponse(ErrInvalidRequest, msgInvalidJSONRPC)
	}
	return &JSONRPCRequest{
		JSONRPC: *raw.JSONRPC,
		ID:      raw.ID,
		Method:  *raw.Method,
		Params:  raw.Params,
	}, nil
}

// validJSONRPCID accepts an absent id (notification) or a string / number
// / null id, as the legacy schema does.
func validJSONRPCID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	switch v.(type) {
	case nil, string, float64:
		return true
	default: // bool, array, object
		return false
	}
}

// validJSONRPCParams accepts absent params or an array/object value; the
// legacy schema rejects null params outright.
func validJSONRPCParams(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && (t[0] == '[' || t[0] == '{')
}

// frameworkErrorResponse builds a response for requests that failed
// parsing/validation, where the id is unknowable (legacy: id null, with
// an explicit "data": null member because legacy runs dispatch with
// debug=True).
func frameworkErrorResponse(code int, message string) *JSONRPCResponse {
	return errorResponse(nil, code, message, json.RawMessage("null"))
}

// parseErrorResponse builds the -32700 response. Legacy dispatch runs
// with debug=True and fills error.data with the json.JSONDecodeError
// message (dispatcher.py:222); the Go counterpart is the encoding/json
// error, which is derived from the client's input bytes and carries no
// server internals (#396 sanitizer), so exposing it is safe and keeps
// parity with legacy's non-null data.
func parseErrorResponse(err error) *JSONRPCResponse {
	if err == nil {
		return errorResponse(nil, ErrParseError, msgInvalidJSON, nil)
	}
	return errorResponse(nil, ErrParseError, msgInvalidJSON, err.Error())
}

// errorResponse builds an error response; data is exposed verbatim, so
// callers must only pass client-safe detail (apierrors.PublicError or the
// explicit null above) — internal error detail stays in logs/spans only
// (#396 sanitizer).
func errorResponse(id json.RawMessage, code int, message string, data interface{}) *JSONRPCResponse {
	return &JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

func recordFrameworkError(span trace.Span, resp *JSONRPCResponse) {
	if span.IsRecording() {
		span.SetStatus(codes.Error, resp.Error.Message)
	}
}

func frameworkErrorReason(resp *JSONRPCResponse) string {
	if resp.Error.Code == ErrParseError {
		return "parse_error"
	}
	return "invalid_request"
}

// sendResponse sends a successful JSON-RPC response with the CORS header
// legacy sets on every JSON-RPC answer (serve.py:264).
func (h *JSONRPCHandler) sendResponse(c *gin.Context, resp *JSONRPCResponse) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.JSON(http.StatusOK, resp)
}

// sendErrorResponse sends an error JSON-RPC response with the CORS header.
func (h *JSONRPCHandler) sendErrorResponse(c *gin.Context, resp *JSONRPCResponse) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.JSON(http.StatusOK, resp)
}

// CORSPreflightHandler answers OPTIONS preflights for the JSON-RPC
// endpoint. The legacy server sets Access-Control-Allow-Origin: * only on
// POST responses and has no OPTIONS route (aiohttp replies 405); handling
// the preflight here is an additive fix so browser clients can call the
// API without a proxy.
func CORSPreflightHandler(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "POST, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type")
	c.Header("Access-Control-Max-Age", "86400")
	c.AbortWithStatus(http.StatusNoContent)
}

// Error messages match the legacy jsonrpcserver 4.0.1 strings (response.py)
// so response comparison stays neutral.
const (
	msgInvalidJSON    = "Invalid JSON"       // -32700
	msgInvalidJSONRPC = "Invalid JSON-RPC"   // -32600
	msgMethodNotFound = "Method not found"   // -32601
	msgInvalidParams  = "Invalid parameters" // -32602
	msgServerError    = "Server error"       // -32000
)

// Standard JSON-RPC error codes (jsonrpcserver 4.0.1 status.py). Legacy
// never emits -32603 (it maps unhandled exceptions to -32000); the
// constant stays defined for spec completeness.
const (
	ErrParseError     = -32700
	ErrInvalidRequest = -32600
	ErrMethodNotFound = -32601
	ErrInvalidParams  = -32602
	ErrInternalError  = -32603 // reserved; unused for legacy parity
	ErrServerError    = -32000
)
