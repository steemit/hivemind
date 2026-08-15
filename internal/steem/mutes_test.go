package steem

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMutes_LoadAndAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("badguy1 badguy2\nspamking\t threat\n"))
	}))
	defer srv.Close()

	m := NewMutes(srv.URL)
	all := m.All()
	want := []string{"badguy1", "badguy2", "spamking", "threat"}
	if len(all) != len(want) {
		t.Fatalf("All = %v, want %v", all, want)
	}
	for i, name := range want {
		if all[i] != name {
			t.Errorf("All[%d] = %q, want %q", i, all[i], name)
		}
	}
	if !m.Contains("badguy1") {
		t.Error("Contains(badguy1) = false")
	}
	if m.Contains("innocent") {
		t.Error("Contains(innocent) = true")
	}
}

func TestMutes_UnreachableURLEmpty(t *testing.T) {
	// Legacy swallows fetch errors into an empty list.
	m := NewMutes("http://127.0.0.1:1/none")
	if len(m.All()) != 0 {
		t.Errorf("unreachable URL should yield empty list, got %v", m.All())
	}
}

func TestMutes_EmptyURLDisabled(t *testing.T) {
	m := NewMutes("")
	if len(m.All()) != 0 {
		t.Error("empty URL should be disabled")
	}
	// Lists still derives reputation tags without any remote data.
	got := m.Lists("anyone", 0)
	if len(got) != 1 || got[0] != "reputation-0" {
		t.Errorf("Lists = %v, want [reputation-0]", got)
	}
}

func TestMutes_Lists(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("badguy badguy2"))
	}))
	defer srv.Close()

	m := NewMutes(srv.URL)

	cases := []struct {
		name string
		rep  float64
		want []string
	}{
		{"badguy", 50, []string{"irredeemables"}},
		// Distinct names: results are cached per name (legacy blist_map),
		// so the same name with a different rep would return the cached one.
		{"badguy2", 0.5, []string{"irredeemables", "reputation-0"}}, // int(0.5)=0 < 1
		{"newbie", 1, []string{"reputation-1"}},
		{"newbie2", 1.9, []string{"reputation-1"}}, // int(1.9)=1
		{"regular", 42, []string{}},                // no tags
	}
	for _, c := range cases {
		got := m.Lists(c.name, c.rep)
		if len(got) != len(c.want) {
			t.Fatalf("Lists(%q,%v) = %v, want %v", c.name, c.rep, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("Lists(%q,%v)[%d] = %q, want %q", c.name, c.rep, i, got[i], c.want[i])
			}
		}
	}
}

func TestMutes_CacheAndRefresh(t *testing.T) {
	body := "firstguy"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	m := NewMutes(srv.URL)
	if !m.Contains("firstguy") {
		t.Fatal("initial load missing firstguy")
	}

	// Cached per-account result survives a reload until cache clear.
	cached := m.Lists("firstguy", 50)
	if len(cached) != 1 || cached[0] != "irredeemables" {
		t.Fatalf("Lists = %v", cached)
	}

	// Serve new content and force a reload; cache must be invalidated.
	body = "secondguy"
	m.Load()
	if m.Contains("firstguy") {
		t.Error("stale firstguy survived reload")
	}
	if !m.Contains("secondguy") {
		t.Error("secondguy missing after reload")
	}
	if got := m.Lists("secondguy", 50); len(got) != 1 || got[0] != "irredeemables" {
		t.Errorf("Lists(secondguy) = %v", got)
	}
}

func TestSharedMutes(t *testing.T) {
	// Default shared instance is non-nil and disabled.
	if SharedMutes() == nil {
		t.Fatal("SharedMutes should never be nil")
	}
	if len(SharedMutes().All()) != 0 {
		t.Error("default shared mutes should be empty")
	}

	// Installing an instance swaps it.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("sharedguy"))
	}))
	defer srv.Close()
	SetSharedMutes(NewMutes(srv.URL))
	defer SetSharedMutes(NewMutes(""))

	if !SharedMutes().Contains("sharedguy") {
		t.Error("shared instance not installed")
	}
}
