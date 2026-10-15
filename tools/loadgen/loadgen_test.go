package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadCasesDefault(t *testing.T) {
	cases, err := loadCases("")
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("default case set is empty")
	}
	for _, c := range cases {
		if c.API == "" {
			t.Fatal("default case with empty api")
		}
	}
}

func TestLoadCasesFromComparisonYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cases.yaml")
	content := `config:
  tolerance:
    float_epsilon: 0.001
test_cases:
  - id: one
    name: "positional params"
    api: "condenser_api.get_content"
    priority: "P0"
    params: ["steemit", "firstpost"]
  - id: two
    api: "bridge.get_profile"
    params:
      account: "steemit"
  - id: no-api-entry
    name: "skipped (no api key)"
    params: []
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cases, err := loadCases(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cases) != 2 {
		t.Fatalf("want 2 usable cases, got %d", len(cases))
	}
	if cases[0].API != "condenser_api.get_content" {
		t.Errorf("case 0 api: %s", cases[0].API)
	}
	// Body must carry the params verbatim (array form preserved).
	body, err := cases[0].body(7)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":7,"jsonrpc":"2.0","method":"condenser_api.get_content","params":["steemit","firstpost"]}`
	if string(body) != want {
		t.Errorf("body mismatch:\n got %s\nwant %s", body, want)
	}
	// Object form (bridge kwargs) must survive the round trip too.
	body2, err := cases[1].body(1)
	if err != nil {
		t.Fatal(err)
	}
	if want2 := `{"id":1,"jsonrpc":"2.0","method":"bridge.get_profile","params":{"account":"steemit"}}`; string(body2) != want2 {
		t.Errorf("object body mismatch:\n got %s\nwant %s", body2, want2)
	}
}

func TestLoadCasesMissingFile(t *testing.T) {
	if _, err := loadCases("/nonexistent/cases.yaml"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadCasesNoUsableEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.yaml")
	if err := os.WriteFile(path, []byte("test_cases: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCases(path); err == nil {
		t.Fatal("expected error for case file with no usable entries")
	}
}

func TestPercentile(t *testing.T) {
	lat := make([]time.Duration, 100)
	for i := range lat {
		lat[i] = time.Duration(i+1) * time.Millisecond // 1ms..100ms
	}
	if got := percentile(lat, 50); got != 50*time.Millisecond && got != 51*time.Millisecond {
		t.Errorf("p50 = %s", got)
	}
	if got := percentile(lat, 100); got != 100*time.Millisecond {
		t.Errorf("max = %s", got)
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("empty = %s", got)
	}
	// percentile must not reorder the input slice (caller may reuse it).
	if lat[0] != 1*time.Millisecond || lat[99] != 100*time.Millisecond {
		t.Error("percentile mutated its input")
	}
}

func TestCollectorSummary(t *testing.T) {
	c := newCollector(16)
	done := make(chan struct{})
	go func() { c.run(); close(done) }()
	c.record(sample{method: "a", latency: 10 * time.Millisecond, httpOK: true})
	c.record(sample{method: "a", latency: 20 * time.Millisecond, httpOK: true})
	c.record(sample{method: "b", latency: 5 * time.Millisecond, httpOK: true, rpcErr: true})
	c.close()
	<-done

	s := c.summary(2 * time.Second)
	// 3 requests over 2s = 1.5 rps; 1 error of 3 = 33.33%.
	for _, want := range []string{"requests: 3", "1.5 rps", "errors: 1 (33.33%)", "a", "b"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary missing %q:\n%s", want, s)
		}
	}
}
