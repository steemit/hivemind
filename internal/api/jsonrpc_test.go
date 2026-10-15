// Framework-level tests for the JSON-RPC handler: response shapes (the
// P1-18 null-result contract), batching, notifications, error-code
// mapping, CORS and the request-body cap. All cases go through a real gin
// engine with real registered methods, asserting against the legacy stack
// (aiohttp + jsonrpcserver 4.0.1) semantics documented in jsonrpc.go.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/steemit/hivemind/internal/apierrors"
	"github.com/steemit/hivemind/pkg/telemetry"
)

// newTestEngine builds a gin engine around a JSONRPCHandler with a set of
// lightweight methods exercising every framework outcome (success, nil
// result, public error, internal error). maxBodyBytes <= 0 selects the
// framework default.
func newTestEngine(maxBodyBytes int64) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewJSONRPCHandler(maxBodyBytes)
	h.RegisterMethod("test.echo", func(c *gin.Context, params json.RawMessage) (interface{}, error) {
		return gin.H{"params": string(params)}, nil
	})
	h.RegisterMethod("test.null", func(c *gin.Context, params json.RawMessage) (interface{}, error) {
		return nil, nil
	})
	h.RegisterMethod("test.invalid_params", func(c *gin.Context, params json.RawMessage) (interface{}, error) {
		return nil, apierrors.PublicError("invalid account name length: `x`")
	})
	h.RegisterMethod("test.internal_error", func(c *gin.Context, params json.RawMessage) (interface{}, error) {
		return nil, errors.New("sql: connection refused")
	})
	h.RegisterMethod("test.panic", func(c *gin.Context, params json.RawMessage) (interface{}, error) {
		panic("boom: handler exploded")
	})
	engine := gin.New()
	engine.POST("/", h.Handle)
	engine.OPTIONS("/", CORSPreflightHandler)
	return engine
}

func doPost(t *testing.T, engine *gin.Engine, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func doOptions(t *testing.T, engine *gin.Engine) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

func decodeObject(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	if w.Body.Len() == 0 {
		t.Fatalf("expected a JSON object body, got empty body (status %d)", w.Code)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &obj); err != nil {
		t.Fatalf("response is not a JSON object: %q (err %v)", w.Body.String(), err)
	}
	return obj
}

func decodeArray(t *testing.T, w *httptest.ResponseRecorder) []interface{} {
	t.Helper()
	if w.Body.Len() == 0 {
		t.Fatalf("expected a JSON array body, got empty body (status %d)", w.Code)
	}
	var arr []interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &arr); err != nil {
		t.Fatalf("response is not a JSON array: %q (err %v)", w.Body.String(), err)
	}
	return arr
}

func asObject(t *testing.T, v interface{}) map[string]interface{} {
	t.Helper()
	obj, ok := v.(map[string]interface{})
	if !ok {
		t.Fatalf("expected a JSON object, got %T %v", v, v)
	}
	return obj
}

func errCode(t *testing.T, obj map[string]interface{}) float64 {
	t.Helper()
	e := asObject(t, obj["error"])
	code, ok := e["code"].(float64)
	if !ok {
		t.Fatalf("expected numeric error code, got %v", e)
	}
	return code
}

func errMessage(t *testing.T, obj map[string]interface{}) string {
	t.Helper()
	e := asObject(t, obj["error"])
	msg, _ := e["message"].(string)
	return msg
}

func errData(t *testing.T, obj map[string]interface{}) interface{} {
	t.Helper()
	e := asObject(t, obj["error"])
	return e["data"]
}

// --- Single request: success shapes -------------------------------------

func TestSuccessResponseShape(t *testing.T) {
	engine := newTestEngine(0)
	w := doPost(t, engine, `{"jsonrpc":"2.0","method":"test.echo","id":1,"params":["a"]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	obj := decodeObject(t, w)
	if obj["jsonrpc"] != "2.0" {
		t.Fatalf("jsonrpc = %v, want 2.0", obj["jsonrpc"])
	}
	if obj["id"] != float64(1) {
		t.Fatalf("id = %v, want 1", obj["id"])
	}
	result := asObject(t, obj["result"])
	if result["params"] != `["a"]` {
		t.Fatalf("result.params = %v", result["params"])
	}
	if _, present := obj["error"]; present {
		t.Fatalf("success response must not carry an error member: %v", obj)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

// TestNullResultShape pins the P1-18 contract: a handler returning
// (nil, nil) must produce an explicit "result": null member. The legacy
// server always emits the result key; omitempty used to drop it, which
// the fail-closed comparison tooling (#420) depends on.
func TestNullResultShape(t *testing.T) {
	engine := newTestEngine(0)
	w := doPost(t, engine, `{"jsonrpc":"2.0","method":"test.null","id":7}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"result":null`) {
		t.Fatalf("body %q must contain \"result\":null", w.Body.String())
	}
	obj := decodeObject(t, w)
	result, present := obj["result"]
	if !present {
		t.Fatalf("result member missing from %v", obj)
	}
	if result != nil {
		t.Fatalf("result = %v, want JSON null", result)
	}
	if _, present := obj["error"]; present {
		t.Fatalf("null-result success response must not carry error: %v", obj)
	}
}

func TestIDTypesEchoed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want interface{}
	}{
		{"number id", `{"jsonrpc":"2.0","method":"test.echo","id":42}`, float64(42)},
		{"string id", `{"jsonrpc":"2.0","method":"test.echo","id":"abc"}`, "abc"},
		{"explicit null id", `{"jsonrpc":"2.0","method":"test.echo","id":null}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doPost(t, newTestEngine(0), tc.body)
			obj := decodeObject(t, w)
			if _, present := obj["id"]; !present {
				t.Fatalf("id member missing from %v", obj)
			}
			got := obj["id"]
			if tc.want == nil && got != nil {
				t.Fatalf("id = %v, want null", got)
			}
			if tc.want != nil && got != tc.want {
				t.Fatalf("id = %v, want %v", got, tc.want)
			}
			if _, present := obj["result"]; !present {
				t.Fatalf("result member missing from %v", obj)
			}
		})
	}
}

// A large integer id beyond float64 precision (2^53) must be echoed
// byte-for-byte: ids are carried as raw JSON, never decoded to float64,
// so client-visible id values cannot be silently distorted.
func TestLargeIntegerIDByteEcho(t *testing.T) {
	const id = `12345678901234567890`
	w := doPost(t, newTestEngine(0), `{"jsonrpc":"2.0","method":"test.echo","id":`+id+`}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"id":`+id) {
		t.Fatalf("id not echoed byte-exactly: %q", w.Body.String())
	}
	obj := decodeObject(t, w)
	if _, present := obj["result"]; !present {
		t.Fatalf("result member missing: %v", obj)
	}
}

func TestParamsFormsPassedThrough(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		// Legacy schema rejects non-array/object params at the framework
		// level, so only these forms reach the handler.
		{"array params", `{"jsonrpc":"2.0","method":"test.echo","id":1,"params":[1,2]}`, `[1,2]`},
		{"object params", `{"jsonrpc":"2.0","method":"test.echo","id":1,"params":{"a":1}}`, `{"a":1}`},
		{"absent params", `{"jsonrpc":"2.0","method":"test.echo","id":1}`, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doPost(t, newTestEngine(0), tc.body)
			obj := decodeObject(t, w)
			result := asObject(t, obj["result"])
			if result["params"] != tc.want {
				t.Fatalf("handler params = %q, want %q", result["params"], tc.want)
			}
		})
	}
}

// --- Single request: framework rejections -------------------------------

func TestParseError(t *testing.T) {
	engine := newTestEngine(0)
	w := doPost(t, engine, `{"jsonrpc":"2.0","method":`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (legacy always answers 200)", w.Code)
	}
	obj := decodeObject(t, w)
	if code := errCode(t, obj); code != -32700 {
		t.Fatalf("code = %v, want -32700", code)
	}
	if msg := errMessage(t, obj); msg != "Invalid JSON" {
		t.Fatalf("message = %q, want %q", msg, "Invalid JSON")
	}
	// Legacy debug dispatch fills data with the parser message
	// (dispatcher.py:222); Go exposes the encoding/json error derived
	// from the client's bytes.
	if data, ok := errData(t, obj).(string); !ok || data == "" {
		t.Fatalf("data = %v, want a non-empty JSON parse error message", errData(t, obj))
	}
	if obj["id"] != nil {
		t.Fatalf("id = %v, want null (id unknowable)", obj["id"])
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

// TestInvalidRequestVariants covers schema violations that legacy
// jsonrpcserver 4.0.1 rejects wholesale with -32600 "Invalid JSON-RPC"
// and id null.
func TestInvalidRequestVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"wrong version", `{"jsonrpc":"1.0","method":"test.echo","id":1}`},
		{"missing version", `{"method":"test.echo","id":1}`},
		{"version wrong type", `{"jsonrpc":2.0,"method":"test.echo","id":1}`},
		{"missing method", `{"jsonrpc":"2.0","id":1}`},
		{"method wrong type", `{"jsonrpc":"2.0","method":5,"id":1}`},
		{"id wrong type (bool)", `{"jsonrpc":"2.0","method":"test.echo","id":true}`},
		{"id wrong type (array)", `{"jsonrpc":"2.0","method":"test.echo","id":[1]}`},
		{"id wrong type (object)", `{"jsonrpc":"2.0","method":"test.echo","id":{"a":1}}`},
		{"params wrong type", `{"jsonrpc":"2.0","method":"test.echo","id":1,"params":"x"}`},
		{"params null", `{"jsonrpc":"2.0","method":"test.echo","id":1,"params":null}`},
		{"bare string body", `"hello"`},
		{"bare number body", `123`},
		{"null body", `null`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doPost(t, newTestEngine(0), tc.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			obj := decodeObject(t, w)
			if code := errCode(t, obj); code != -32600 {
				t.Fatalf("code = %v, want -32600", code)
			}
			if msg := errMessage(t, obj); msg != "Invalid JSON-RPC" {
				t.Fatalf("message = %q, want %q", msg, "Invalid JSON-RPC")
			}
			if obj["id"] != nil {
				t.Fatalf("id = %v, want null", obj["id"])
			}
		})
	}
}

func TestEmptyBodyIsParseError(t *testing.T) {
	w := doPost(t, newTestEngine(0), ``)
	obj := decodeObject(t, w)
	if code := errCode(t, obj); code != -32700 {
		t.Fatalf("code = %v, want -32700", code)
	}
	if data, ok := errData(t, obj).(string); !ok || data == "" {
		t.Fatalf("data = %v, want a non-empty JSON parse error message", errData(t, obj))
	}
}

// Trailing garbage after an otherwise valid JSON document must be a
// -32700 parse error: legacy json.loads rejects the whole string
// (dispatcher.py:221-222), it must not fall through to dispatch.
func TestTrailingGarbageIsParseError(t *testing.T) {
	w := doPost(t, newTestEngine(0), `{"jsonrpc":"2.0","method":"test.echo","id":1}xyz`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	obj := decodeObject(t, w)
	if code := errCode(t, obj); code != -32700 {
		t.Fatalf("code = %v, want -32700", code)
	}
	if data, ok := errData(t, obj).(string); !ok || data == "" {
		t.Fatalf("data = %v, want a non-empty JSON parse error message", errData(t, obj))
	}
	if obj["id"] != nil {
		t.Fatalf("id = %v, want null", obj["id"])
	}
}

// --- Error-code mapping (legacy handle_exceptions parity) ---------------

func TestMethodNotFound(t *testing.T) {
	engine := newTestEngine(0)
	w := doPost(t, engine, `{"jsonrpc":"2.0","method":"test.nope","id":5}`)

	obj := decodeObject(t, w)
	if code := errCode(t, obj); code != -32601 {
		t.Fatalf("code = %v, want -32601", code)
	}
	if msg := errMessage(t, obj); msg != "Method not found" {
		t.Fatalf("message = %q, want %q", msg, "Method not found")
	}
	// Legacy debug dispatch fills data with the bare requested method
	// name (dispatcher.py:122-124: data=request.method), not a sentence.
	if data := errData(t, obj); data != "test.nope" {
		t.Fatalf("data = %v, want the bare method name \"test.nope\"", data)
	}
	if obj["id"] != float64(5) {
		t.Fatalf("id = %v, want 5 (echo)", obj["id"])
	}
	if _, present := obj["result"]; present {
		t.Fatalf("error response must not carry a result member: %v", obj)
	}
}

// An empty-but-present method name passes the legacy schema and fails
// method lookup with -32601, exactly like any other unknown method.
func TestEmptyMethodNameIsMethodNotFound(t *testing.T) {
	w := doPost(t, newTestEngine(0), `{"jsonrpc":"2.0","method":"","id":1}`)
	obj := decodeObject(t, w)
	if code := errCode(t, obj); code != -32601 {
		t.Fatalf("code = %v, want -32601", code)
	}
}

func TestInvalidParamsMapping(t *testing.T) {
	engine := newTestEngine(0)
	w := doPost(t, engine, `{"jsonrpc":"2.0","method":"test.invalid_params","id":9}`)

	obj := decodeObject(t, w)
	if code := errCode(t, obj); code != -32602 {
		t.Fatalf("code = %v, want -32602 (legacy AssertionError class)", code)
	}
	if msg := errMessage(t, obj); msg != "Invalid parameters" {
		t.Fatalf("message = %q, want %q", msg, "Invalid parameters")
	}
	if data := errData(t, obj); data != "invalid account name length: `x`" {
		t.Fatalf("data = %v, want the public message", data)
	}
	if obj["id"] != float64(9) {
		t.Fatalf("id = %v, want 9", obj["id"])
	}
}

func TestInternalErrorSanitized(t *testing.T) {
	engine := newTestEngine(0)
	w := doPost(t, engine, `{"jsonrpc":"2.0","method":"test.internal_error","id":3}`)

	obj := decodeObject(t, w)
	if code := errCode(t, obj); code != -32000 {
		t.Fatalf("code = %v, want -32000 (legacy never emits -32603)", code)
	}
	if msg := errMessage(t, obj); msg != "Server error" {
		t.Fatalf("message = %q, want %q", msg, "Server error")
	}
	e := asObject(t, obj["error"])
	if _, present := e["data"]; present {
		t.Fatalf("internal error detail must be withheld (#396 sanitizer): %v", e)
	}
	if strings.Contains(w.Body.String(), "sql") {
		t.Fatalf("internal error detail leaked into the response: %q", w.Body.String())
	}
}

// --- Per-item panic isolation (audit P1) ---------------------------------

// A panicking handler is mapped to -32000 "Server error" for that request
// only, with the panic detail withheld, mirroring legacy handle_exceptions
// which converts every raised exception into a per-item response. The zap
// Error log with stack is asserted indirectly: nothing may leak into the
// HTTP response.
func TestSingleRequestPanicIsServerError(t *testing.T) {
	w := doPost(t, newTestEngine(0), `{"jsonrpc":"2.0","method":"test.panic","id":1}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (panic must not escape to gin.Recovery)", w.Code)
	}
	obj := decodeObject(t, w)
	if code := errCode(t, obj); code != -32000 {
		t.Fatalf("code = %v, want -32000", code)
	}
	if msg := errMessage(t, obj); msg != "Server error" {
		t.Fatalf("message = %q, want %q", msg, "Server error")
	}
	e := asObject(t, obj["error"])
	if _, present := e["data"]; present {
		t.Fatalf("panic detail must be withheld: %v", e)
	}
	if strings.Contains(w.Body.String(), "boom") {
		t.Fatalf("panic detail leaked into the response: %q", w.Body.String())
	}
}

// One panicking item must not take the whole batch down: every other item
// keeps its (already computed) response and the panicking item is answered
// with -32000. Without the per-item recover the panic unwinds to
// gin.Recovery, which answers 500 with an empty body and discards all
// responses — the exact regression the audit flagged.
func TestBatchPanicIsolation(t *testing.T) {
	engine := newTestEngine(0)
	body := `[
		{"jsonrpc":"2.0","method":"test.echo","id":1,"params":["a"]},
		{"jsonrpc":"2.0","method":"test.panic","id":2},
		{"jsonrpc":"2.0","method":"test.echo","id":3,"params":["c"]}
	]`
	w := doPost(t, engine, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (batch must survive the panic)", w.Code)
	}
	arr := decodeArray(t, w)
	if len(arr) != 3 {
		t.Fatalf("got %d responses, want 3: %v", len(arr), arr)
	}

	first := asObject(t, arr[0])
	if _, present := first["result"]; !present {
		t.Fatalf("item before the panic must keep its response: %v", first)
	}

	second := asObject(t, arr[1])
	if second["id"] != float64(2) {
		t.Fatalf("panicking item id = %v, want 2", second["id"])
	}
	if code := errCode(t, second); code != -32000 {
		t.Fatalf("panicking item code = %v, want -32000", code)
	}
	if strings.Contains(w.Body.String(), "boom") {
		t.Fatalf("panic detail leaked into the batch response: %q", w.Body.String())
	}

	third := asObject(t, arr[2])
	if _, present := third["result"]; !present {
		t.Fatalf("item after the panic must still be answered: %v", third)
	}
}

// --- Notifications -------------------------------------------------------

func TestSingleNotificationIsEmpty200(t *testing.T) {
	engine := newTestEngine(0)
	w := doPost(t, engine, `{"jsonrpc":"2.0","method":"test.echo","params":[]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("notification must not be answered, got body %q", w.Body.String())
	}
}

// A notification whose method fails is still never answered (legacy
// handle_exceptions replaces any error with NotificationResponse).
func TestFailedNotificationStaysSilent(t *testing.T) {
	engine := newTestEngine(0)
	w := doPost(t, engine, `{"jsonrpc":"2.0","method":"test.nope"}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("failed notification must not be answered, got body %q", w.Body.String())
	}
}

// A notification whose handler panics is still never answered: the
// recovered panic maps to a -32000 response that executeRequest discards
// for notifications (legacy handle_exceptions finally-clause).
func TestPanickingNotificationIsEmpty200(t *testing.T) {
	engine := newTestEngine(0)
	w := doPost(t, engine, `{"jsonrpc":"2.0","method":"test.panic","params":[]}`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Fatalf("panicking notification must not be answered, got body %q", w.Body.String())
	}
}

// An explicit "id": null is NOT a notification (legacy NOID sentinel vs
// None): the request is answered with "id": null.
func TestExplicitNullIDIsNotANotification(t *testing.T) {
	w := doPost(t, newTestEngine(0), `{"jsonrpc":"2.0","method":"test.echo","id":null}`)
	obj := decodeObject(t, w)
	if _, present := obj["result"]; !present {
		t.Fatalf("explicit null id must be answered: %v", obj)
	}
	if _, present := obj["id"]; !present || obj["id"] != nil {
		t.Fatalf("id = %v, want null member present", obj["id"])
	}
}

// --- Batch ---------------------------------------------------------------

func TestBatchMixed(t *testing.T) {
	engine := newTestEngine(0)
	body := `[
		{"jsonrpc":"2.0","method":"test.echo","id":1,"params":["a"]},
		{"jsonrpc":"2.0","method":"test.echo","params":["skipped"]},
		{"jsonrpc":"2.0","method":"test.nope","id":2},
		{"jsonrpc":"2.0","method":"test.invalid_params","id":3}
	]`
	w := doPost(t, engine, body)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	arr := decodeArray(t, w)
	if len(arr) != 3 {
		t.Fatalf("batch of 4 with 1 notification must yield 3 responses, got %d: %v", len(arr), arr)
	}

	// Responses stay in request order (notification omitted).
	first := asObject(t, arr[0])
	if first["id"] != float64(1) {
		t.Fatalf("first response id = %v, want 1", first["id"])
	}
	if _, present := first["result"]; !present {
		t.Fatalf("first response missing result: %v", first)
	}

	second := asObject(t, arr[1])
	if second["id"] != float64(2) {
		t.Fatalf("second response id = %v, want 2", second["id"])
	}
	if code := errCode(t, second); code != -32601 {
		t.Fatalf("second response code = %v, want -32601", code)
	}

	third := asObject(t, arr[2])
	if third["id"] != float64(3) {
		t.Fatalf("third response id = %v, want 3", third["id"])
	}
	if code := errCode(t, third); code != -32602 {
		t.Fatalf("third response code = %v, want -32602", code)
	}

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

func TestBatchWithInternalErrorItem(t *testing.T) {
	w := doPost(t, newTestEngine(0), `[
		{"jsonrpc":"2.0","method":"test.internal_error","id":1}
	]`)
	arr := decodeArray(t, w)
	if len(arr) != 1 {
		t.Fatalf("got %d responses, want 1", len(arr))
	}
	obj := asObject(t, arr[0])
	if code := errCode(t, obj); code != -32000 {
		t.Fatalf("code = %v, want -32000", code)
	}
	if strings.Contains(w.Body.String(), "sql") {
		t.Fatalf("internal error detail leaked into batch response: %q", w.Body.String())
	}
}

// Structural failure of one item answers that item (id null) without
// killing the batch. Legacy rejects the whole batch here; per-item answers
// are the JSON-RPC 2.0 spec behavior (registered deviation).
func TestBatchWithStructurallyInvalidItem(t *testing.T) {
	w := doPost(t, newTestEngine(0), `[
		{"jsonrpc":"2.0","method":"test.echo","id":7},
		42
	]`)
	arr := decodeArray(t, w)
	if len(arr) != 2 {
		t.Fatalf("got %d responses, want 2: %v", len(arr), arr)
	}
	first := asObject(t, arr[0])
	if _, present := first["result"]; !present {
		t.Fatalf("valid item must still be answered: %v", first)
	}
	second := asObject(t, arr[1])
	if code := errCode(t, second); code != -32600 {
		t.Fatalf("invalid item code = %v, want -32600", code)
	}
	if second["id"] != nil {
		t.Fatalf("invalid item id = %v, want null", second["id"])
	}
}

// Nested arrays as batch items are structurally invalid request objects,
// answered per-item with -32600 and id null; the sibling items (here:
// every item) keep being processed.
func TestBatchNestedArrayItems(t *testing.T) {
	w := doPost(t, newTestEngine(0), `[[1],[2]]`)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	arr := decodeArray(t, w)
	if len(arr) != 2 {
		t.Fatalf("got %d responses, want 2: %v", len(arr), arr)
	}
	for i, item := range arr {
		obj := asObject(t, item)
		if code := errCode(t, obj); code != -32600 {
			t.Fatalf("item %d code = %v, want -32600", i, code)
		}
		if obj["id"] != nil {
			t.Fatalf("item %d id = %v, want null", i, obj["id"])
		}
	}
}

// A batch of one still answers with an array (legacy BatchResponse).
func TestBatchOfOneReturnsArray(t *testing.T) {
	w := doPost(t, newTestEngine(0), `[{"jsonrpc":"2.0","method":"test.echo","id":1}]`)
	arr := decodeArray(t, w)
	if len(arr) != 1 {
		t.Fatalf("got %d responses, want 1", len(arr))
	}
}

// All-notification batches yield an empty JSON array with HTTP 200
// (legacy: BatchResponse.wanted is always true, so serve.py serializes
// the empty list instead of the empty notification body).
func TestBatchAllNotificationsYieldsEmptyArray(t *testing.T) {
	w := doPost(t, newTestEngine(0), `[
		{"jsonrpc":"2.0","method":"test.echo","params":[]},
		{"jsonrpc":"2.0","method":"test.nope"}
	]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("body = %q, want []", w.Body.String())
	}
}

// An empty array is a single -32600 error object, not an empty batch
// response (legacy request schema: minItems=1).
func TestEmptyBatchRejected(t *testing.T) {
	w := doPost(t, newTestEngine(0), `[]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	obj := decodeObject(t, w) // object, not array
	if code := errCode(t, obj); code != -32600 {
		t.Fatalf("code = %v, want -32600", code)
	}
	if obj["id"] != nil {
		t.Fatalf("id = %v, want null", obj["id"])
	}
}

func TestBatchOverLimitRejected(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < maxBatchRequests+1; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"jsonrpc":"2.0","method":"test.echo","id":%d}`, i)
	}
	b.WriteByte(']')

	w := doPost(t, newTestEngine(0), b.String())
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	obj := decodeObject(t, w) // single error object, not a per-item array
	if code := errCode(t, obj); code != -32600 {
		t.Fatalf("code = %v, want -32600", code)
	}
	data := errData(t, obj)
	if s, ok := data.(string); !ok || !strings.Contains(s, "exceeds limit") {
		t.Fatalf("data = %v, want a message mentioning the limit", data)
	}
}

func TestBatchAtLimitAccepted(t *testing.T) {
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < maxBatchRequests; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"jsonrpc":"2.0","method":"test.echo","id":%d}`, i)
	}
	b.WriteByte(']')

	w := doPost(t, newTestEngine(0), b.String())
	arr := decodeArray(t, w)
	if len(arr) != maxBatchRequests {
		t.Fatalf("got %d responses, want %d", len(arr), maxBatchRequests)
	}
}

// --- CORS ----------------------------------------------------------------

func TestCORSPreflight(t *testing.T) {
	w := doOptions(t, newTestEngine(0))

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", w.Code)
	}
	want := map[string]string{
		"Access-Control-Allow-Origin":  "*",
		"Access-Control-Allow-Methods": "POST, OPTIONS",
		"Access-Control-Allow-Headers": "Content-Type",
		"Access-Control-Max-Age":       "86400",
	}
	for header, wantVal := range want {
		if got := w.Header().Get(header); got != wantVal {
			t.Fatalf("%s = %q, want %q", header, got, wantVal)
		}
	}
}

func TestCORSOnSuccessAndErrors(t *testing.T) {
	engine := newTestEngine(0)
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"test.echo","id":1}`,
		`{"jsonrpc":"2.0","method":"test.nope","id":2}`,
	} {
		w := doPost(t, engine, body)
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Fatalf("Access-Control-Allow-Origin = %q for %s, want *", got, body)
		}
	}
}

// --- Metrics label cardinality (audit P2) ---------------------------------

// rpcMetricSamples renders the default Prometheus registry to text and
// returns every hivemind_request* sample keyed by identity (metric name +
// label set, value stripped), with its current value.
func rpcMetricSamples(t *testing.T) map[string]float64 {
	t.Helper()
	// hivemind_* metrics live on the unified telemetry registry since the
	// PR#430 registry unification, no longer on DefaultGatherer.
	handler := promhttp.HandlerFor(telemetry.Registry, promhttp.HandlerOpts{})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	samples := make(map[string]float64)
	for _, line := range strings.Split(w.Body.String(), "\n") {
		// Sample lines only: HELP/TYPE comments start with '#'.
		if !strings.HasPrefix(line, "hivemind_request") {
			continue
		}
		idx := strings.LastIndexByte(line, ' ')
		if idx < 0 {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(line[idx+1:]), 64)
		if err != nil {
			t.Fatalf("unparsable metric sample line %q: %v", line, err)
		}
		samples[line[:idx]] = value
	}
	return samples
}

// TestMethodNotFoundMetricsLabelsBounded pins the metrics fix: unknown
// method names are attacker-controlled, so they must never become
// Prometheus label values — each unique probe would otherwise mint a new
// requests_total series plus histogram buckets that stay resident
// forever. Probing with unpredictable method names may bump counters but
// must not create any new series carrying those names; the only series
// the not-found path may touch is the fixed
// hivemind_request_errors_total{namespace="unknown",method="unknown",
// error_type="method_not_found"} counter, incremented once per probe.
func TestMethodNotFoundMetricsLabelsBounded(t *testing.T) {
	const fixedSeries = `hivemind_request_errors_total{error_type="method_not_found",method="unknown",namespace="unknown"}`
	engine := newTestEngine(0)
	probes := []string{
		"zzzprobe.alpha.method",
		"zzzprobe.beta.method",
		"zzzprobe_no_dot_one",
		"zzzprobe_no_dot_two",
	}

	before := rpcMetricSamples(t)
	for _, m := range probes {
		w := doPost(t, engine, fmt.Sprintf(`{"jsonrpc":"2.0","method":%q,"id":1}`, m))
		if code := errCode(t, decodeObject(t, w)); code != -32601 {
			t.Fatalf("probe %q: code = %v, want -32601", m, code)
		}
	}
	after := rpcMetricSamples(t)

	for identity := range after {
		for _, m := range probes {
			if strings.Contains(identity, m) {
				t.Fatalf("metric series %q leaks attacker-controlled method name %q", identity, m)
			}
		}
		if _, existed := before[identity]; !existed && identity != fixedSeries {
			t.Fatalf("probe requests created unexpected new metric series %q", identity)
		}
	}

	got, ok := after[fixedSeries]
	if !ok {
		t.Fatalf("fixed not-found error series %q was not recorded", fixedSeries)
	}
	base := before[fixedSeries] // an absent series reads as zero
	if got-base < float64(len(probes)) {
		t.Fatalf("fixed not-found error series incremented by %v, want at least %d", got-base, len(probes))
	}
}

// --- Request body cap (§5.5) ----------------------------------------------

func TestBodyOverLimitIs413(t *testing.T) {
	const limit = 64
	base := `{"jsonrpc":"2.0","method":"test.echo","id":1}` // 44 bytes
	atLimit := base + strings.Repeat(" ", limit-len(base))
	overLimit := atLimit + " "

	engine := newTestEngine(limit)

	w := doPost(t, engine, atLimit)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d for at-limit body (%d bytes), want 200", w.Code, len(atLimit))
	}
	obj := decodeObject(t, w)
	if _, present := obj["result"]; !present {
		t.Fatalf("at-limit body should be dispatched: %v", obj)
	}

	w = doPost(t, engine, overLimit)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d for over-limit body (%d bytes), want 413", w.Code, len(overLimit))
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

func TestDefaultMaxBodyBytesFallback(t *testing.T) {
	h := NewJSONRPCHandler(0)
	if h.maxBodyBytes != DefaultMaxBodyBytes {
		t.Fatalf("maxBodyBytes = %d, want default %d", h.maxBodyBytes, DefaultMaxBodyBytes)
	}
	if DefaultMaxBodyBytes != 10<<20 {
		t.Fatalf("DefaultMaxBodyBytes = %d, want 10MB", DefaultMaxBodyBytes)
	}
}
