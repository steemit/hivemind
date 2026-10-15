package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// stubQuerier drives the handler matrix without a database.
type stubQuerier struct {
	state *HeadState
	err   error
}

func (s *stubQuerier) HeadState(context.Context) (*HeadState, error) {
	return s.state, s.err
}

func doHealth(t *testing.T, c *Checker) (*healthResponse, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	c.HandleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var resp healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode /health body %q: %v", rec.Body.String(), err)
	}
	return &resp, rec.Code
}

func doHeadAge(t *testing.T, c *Checker) (string, int) {
	t.Helper()
	rec := httptest.NewRecorder()
	c.HandleHeadAge(rec, httptest.NewRequest(http.MethodGet, "/head_age", nil))
	return rec.Body.String(), rec.Code
}

// TestHealthDBUnavailable pins the "stop DB -> /health 5xx" acceptance
// rule (05-deploy.md section 1.2) at the handler level: any head-query
// failure must produce 500 + "db not available" + null state, never the
// old always-200 static response.
func TestHealthDBUnavailable(t *testing.T) {
	c := NewChecker(&stubQuerier{err: errors.New("connection refused")}, time.Hour, false)
	resp, code := doHealth(t, c)
	if code != http.StatusInternalServerError {
		t.Fatalf("/health status = %d, want 500 on DB failure", code)
	}
	if resp.Result != "db not available" {
		t.Errorf("result = %q, want db not available", resp.Result)
	}
	if resp.Status != "WARN" {
		t.Errorf("status = %q, want WARN", resp.Status)
	}
	if resp.State != nil {
		t.Errorf("state = %v, want null", resp.State)
	}

	body, code := doHeadAge(t, c)
	if code != http.StatusInternalServerError {
		t.Fatalf("/head_age status = %d, want 500 on DB failure", code)
	}
	if body == "" || isNumeric(body) {
		t.Errorf("/head_age body = %q, want a non-numeric failure message", body)
	}
}

// TestHealthHeadLag pins the legacy max_head_age rule: a reader whose head
// block is older than the threshold answers 500, while a sync service
// (the indexer) stays 200 regardless of lag (legacy is_syncer branch).
func TestHealthHeadLag(t *testing.T) {
	stale := &HeadState{
		DbHeadBlock: 12345,
		DbHeadTime:  time.Now().Add(-2 * time.Hour),
		DbHeadAge:   7200,
	}

	// Reader + stale head -> 500 with the legacy result text.
	c := NewChecker(&stubQuerier{state: stale}, time.Hour, false)
	resp, code := doHealth(t, c)
	if code != http.StatusInternalServerError {
		t.Fatalf("reader /health status = %d, want 500 on head lag", code)
	}
	want := "head block age (7200) > max (3600); head block num: 12345"
	if resp.Result != want {
		t.Errorf("result = %q, want %q", resp.Result, want)
	}
	if resp.State == nil || resp.State.DbHeadAge != 7200 {
		t.Errorf("state = %+v, want the stale head snapshot", resp.State)
	}

	// Sync service + stale head -> 200 (exempt, like legacy is_syncer).
	c = NewChecker(&stubQuerier{state: stale}, time.Hour, true)
	resp, code = doHealth(t, c)
	if code != http.StatusOK {
		t.Fatalf("sync service /health status = %d, want 200 (lag exempt)", code)
	}
	if resp.Status != "OK" {
		t.Errorf("status = %q, want OK", resp.Status)
	}

	// Reader + fresh head -> 200.
	fresh := &HeadState{DbHeadBlock: 12346, DbHeadTime: time.Now().Add(-5 * time.Second), DbHeadAge: 5}
	c = NewChecker(&stubQuerier{state: fresh}, time.Hour, false)
	resp, code = doHealth(t, c)
	if code != http.StatusOK {
		t.Fatalf("reader /health status = %d, want 200 on fresh head", code)
	}
	if resp.SyncService {
		t.Error("sync_service flag not carried into the response")
	}

	// Threshold disabled (0) + stale head -> 200 (catch-up deployment).
	c = NewChecker(&stubQuerier{state: stale}, 0, false)
	if _, code = doHealth(t, c); code != http.StatusOK {
		t.Fatalf("reader /health with lag check disabled status = %d, want 200", code)
	}
}

// TestHealthNoHeadBlock pins the empty-index branch: a reader on a fresh
// database is NOT healthy; a sync service that has not written anything
// yet is (initial sync in progress, must not be recycled by EB).
func TestHealthNoHeadBlock(t *testing.T) {
	reader := NewChecker(&stubQuerier{err: ErrNoHeadBlock}, time.Hour, false)
	resp, code := doHealth(t, reader)
	if code != http.StatusInternalServerError {
		t.Fatalf("reader /health on empty index = %d, want 500", code)
	}
	if resp.Result != "no head block (database not indexed)" {
		t.Errorf("result = %q, want the empty-index text", resp.Result)
	}

	writer := NewChecker(&stubQuerier{err: ErrNoHeadBlock}, time.Hour, true)
	if _, code = doHealth(t, writer); code != http.StatusOK {
		t.Fatalf("sync service /health on empty index = %d, want 200", code)
	}

	if _, code = doHeadAge(t, reader); code != http.StatusInternalServerError {
		t.Fatalf("/head_age on empty index = %d, want 500", code)
	}
}

// TestHeadAgeValue pins the /head_age contract: the plain numeric
// seconds value with HTTP 200 whenever the database answers -- including
// while badly lagged (the KR5 monitor samples the value during catch-up;
// see the package comment for the deviation from legacy's 15s rule).
func TestHeadAgeValue(t *testing.T) {
	stale := &HeadState{DbHeadBlock: 1, DbHeadTime: time.Now().Add(-90 * time.Minute), DbHeadAge: 5400}
	c := NewChecker(&stubQuerier{state: stale}, time.Hour, false)

	body, code := doHeadAge(t, c)
	if code != http.StatusOK {
		t.Fatalf("/head_age status = %d, want 200 while lagged", code)
	}
	if !isNumeric(body) {
		t.Fatalf("/head_age body %q is not numeric", body)
	}
	if got, err := strconv.Atoi(body); err != nil || got != 5400 {
		t.Errorf("/head_age body = %q, want 5400", body)
	}
}

func isNumeric(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

// TestHealthResponseShape pins the legacy JSON keys so external probes
// (EB health check, legacy-style dashboards) keep working.
func TestHealthResponseShape(t *testing.T) {
	t.Setenv("SOURCE_COMMIT", "deadbeef")
	fresh := &HeadState{DbHeadBlock: 42, DbHeadTime: time.Now().Add(-3 * time.Second), DbHeadAge: 3}
	c := NewChecker(&stubQuerier{state: fresh}, time.Hour, false)

	rec := httptest.NewRecorder()
	c.HandleHealth(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{"state", "result", "status", "sync_service", "source_commit", "schema_hash", "docker_tag", "timestamp"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("response missing legacy key %q", key)
		}
	}
	var state headStateJSON
	if err := json.Unmarshal(raw["state"], &state); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	if state.DbHeadBlock != 42 || state.DbHeadAge != 3 {
		t.Errorf("state = %+v, want block 42 age 3", state)
	}
	var commit string
	if err := json.Unmarshal(raw["source_commit"], &commit); err != nil || commit != "deadbeef" {
		t.Errorf("source_commit = %q, want deadbeef from env", commit)
	}
}

// TestHealthDBHeadQuerierUnreachable is the end-to-end DB-down case with a
// real (bad) DSN: nothing listens on 127.0.0.1:1, so the isolated health
// pool must fail the query and the handler must answer 500 "db not
// available" within the query timeout bound.
func TestHealthDBHeadQuerierUnreachable(t *testing.T) {
	q, err := NewDBHeadQuerier("postgresql://nobody:nopass@127.0.0.1:1/hive?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("NewDBHeadQuerier: %v", err)
	}
	defer q.Close()

	c := NewChecker(q, time.Hour, false)
	resp, code := doHealth(t, c)
	if code != http.StatusInternalServerError {
		t.Fatalf("/health against unreachable DB = %d, want 500", code)
	}
	if resp.Result != "db not available" {
		t.Errorf("result = %q, want db not available", resp.Result)
	}
}

// TestHealthDBHeadQuerierClosedPool: after Close() the querier reports
// failures (the "stop DB" variant without needing a database to stop).
func TestHealthDBHeadQuerierClosedPool(t *testing.T) {
	q, err := NewDBHeadQuerier("postgresql://nobody:nopass@127.0.0.1:1/hive?sslmode=disable")
	if err != nil {
		t.Fatalf("NewDBHeadQuerier: %v", err)
	}
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := q.HeadState(context.Background()); err == nil {
		t.Fatal("HeadState on closed pool should fail")
	}
}
