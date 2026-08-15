package steem

import (
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/steemit/hivemind/pkg/logging"
	"go.uber.org/zap"
)

// Mutes tracks the muted/irredeemable account list, loaded from a remote
// URL and refreshed hourly on access. Port of hive/server/common/mutes.py.
//
// NOTE on faithfulness: the legacy blist (blacklist.usesteem.com) path is
// dead code upstream (fetch commented out; blist always empty), so this
// port implements the live behavior only: the irredeemables list plus the
// reputation-derived blacklist tags.
type Mutes struct {
	url    string
	client *http.Client

	mu        sync.RWMutex
	accounts  map[string]struct{}
	listsMap  map[string][]string // per-account tag cache (legacy blist_map)
	fetchedAt time.Time
}

// mutesRefreshInterval mirrors legacy's hourly reload check (mutes.py:69).
const mutesRefreshInterval = time.Hour

// NewMutes creates a Mutes list; when url is non-empty it loads immediately.
func NewMutes(url string) *Mutes {
	m := &Mutes{
		url:      url,
		client:   &http.Client{Timeout: 10 * time.Second},
		accounts: map[string]struct{}{},
		listsMap: map[string][]string{},
	}
	if url != "" {
		m.Load()
	}
	return m
}

// Load (re)fetches the irredeemables list. On any error the list becomes
// empty — same swallow-and-continue behavior as legacy load().
func (m *Mutes) Load() {
	accounts := map[string]struct{}{}
	if m.url != "" {
		resp, err := m.client.Get(m.url)
		if err == nil {
			body, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if rerr == nil {
				for _, name := range strings.Fields(string(body)) {
					accounts[name] = struct{}{}
				}
			}
		}
	}

	m.mu.Lock()
	m.accounts = accounts
	// New data invalidates the per-account tag cache.
	m.listsMap = map[string][]string{}
	m.fetchedAt = time.Now()
	m.mu.Unlock()

	logging.GetLogger().Warn("mutes list loaded",
		zap.Int("muted", len(accounts)),
		zap.Bool("from_url", m.url != ""))
}

// All returns a sorted snapshot of all muted accounts.
func (m *Mutes) All() []string {
	m.refreshIfStale()
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.accounts))
	for name := range m.accounts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Contains reports whether the account is on the irredeemables list.
func (m *Mutes) Contains(name string) bool {
	m.refreshIfStale()
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.accounts[name]
	return ok
}

// Lists returns the blacklist tags an account belongs to:
// "irredeemables" when muted, plus "reputation-0"/"reputation-1" derived
// from the (truncated) UI reputation. Results are cached per account and
// the underlying list refreshes hourly. Mirrors Mutes.lists() (mutes.py:62).
func (m *Mutes) Lists(name string, rep float64) []string {
	m.refreshIfStale()

	m.mu.RLock()
	cached, ok := m.listsMap[name]
	m.mu.RUnlock()
	if ok {
		return cached
	}

	out := []string{}
	m.mu.RLock()
	_, muted := m.accounts[name]
	m.mu.RUnlock()
	if muted {
		out = append(out, "irredeemables")
	}
	// Legacy uses int(rep) — truncation toward zero.
	switch t := int(rep); {
	case t < 1:
		out = append(out, "reputation-0")
	case t == 1:
		out = append(out, "reputation-1")
	}

	m.mu.Lock()
	m.listsMap[name] = out
	m.mu.Unlock()
	return out
}

func (m *Mutes) refreshIfStale() {
	if m.url == "" {
		return
	}
	m.mu.RLock()
	stale := time.Since(m.fetchedAt) > mutesRefreshInterval
	m.mu.RUnlock()
	if stale {
		m.Load()
	}
}

// Shared instance (mirrors Mutes.set_shared_instance / Mutes.instance).
// SharedMutes never returns nil: an unset instance behaves as an empty list.

var (
	sharedMutesMu sync.RWMutex
	sharedMutes   = NewMutes("") // empty default
)

// SetSharedMutes installs the process-wide Mutes instance.
func SetSharedMutes(m *Mutes) {
	sharedMutesMu.Lock()
	sharedMutes = m
	sharedMutesMu.Unlock()
}

// SharedMutes returns the process-wide Mutes instance (empty behavior when
// never configured).
func SharedMutes() *Mutes {
	sharedMutesMu.RLock()
	defer sharedMutesMu.RUnlock()
	return sharedMutes
}
