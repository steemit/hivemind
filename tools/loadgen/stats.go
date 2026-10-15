// stats.go: latency aggregation for the load generator. Latencies are
// recorded per method (contention is bounded: --rps is at most a few
// hundred and each record is a slice append under a per-method mutex-free
// collector fed by a channel).
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// sample is one completed request.
type sample struct {
	method  string
	latency time.Duration
	// httpOK: transport succeeded with HTTP 200.
	httpOK bool
	// rpcErr: transport OK but the envelope carries a JSON-RPC error.
	rpcErr bool
}

// collector merges samples into per-method and global aggregates.
type collector struct {
	in        chan sample
	perMethod map[string]*methodStats
	total     methodStats
}

type methodStats struct {
	count     int
	errs      int // transport errors + non-200 + JSON-RPC errors
	latencies []time.Duration
}

func newCollector(buffer int) *collector {
	return &collector{
		in:        make(chan sample, buffer),
		perMethod: make(map[string]*methodStats),
	}
}

// run drains the channel until it is closed; then it is safe to summarize.
func (c *collector) run() {
	for s := range c.in {
		c.total.count++
		if !s.httpOK || s.rpcErr {
			c.total.errs++
		}
		c.total.latencies = append(c.total.latencies, s.latency)
		ms := c.perMethod[s.method]
		if ms == nil {
			ms = &methodStats{}
			c.perMethod[s.method] = ms
		}
		ms.count++
		if !s.httpOK || s.rpcErr {
			ms.errs++
		}
		ms.latencies = append(ms.latencies, s.latency)
	}
}

func (c *collector) record(s sample) { c.in <- s }
func (c *collector) close()          { close(c.in) }

// percentile returns the p-th percentile (0-100) of a sorted copy of lat.
func percentile(lat []time.Duration, p float64) time.Duration {
	if len(lat) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), lat...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)-1) * p / 100)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// summary renders the final report: achieved RPS, error rate, latency
// distribution and a per-method table.
func (c *collector) summary(elapsed time.Duration) string {
	var b strings.Builder
	n := c.total.count
	fmt.Fprintf(&b, "requests: %d over %s (%.1f rps achieved)\n", n, elapsed.Round(time.Millisecond),
		float64(n)/elapsed.Seconds())
	errRate := 0.0
	if n > 0 {
		errRate = float64(c.total.errs) / float64(n) * 100
	}
	fmt.Fprintf(&b, "errors: %d (%.2f%%)\n", c.total.errs, errRate)
	fmt.Fprintf(&b, "latency: min %s p50 %s p90 %s p95 %s p99 %s max %s\n",
		percentile(c.total.latencies, 0),
		percentile(c.total.latencies, 50),
		percentile(c.total.latencies, 90),
		percentile(c.total.latencies, 95),
		percentile(c.total.latencies, 99),
		percentile(c.total.latencies, 100))
	fmt.Fprintf(&b, "%-45s %8s %8s %8s %8s\n", "method", "count", "errors", "p50", "p99")
	names := make([]string, 0, len(c.perMethod))
	for name := range c.perMethod {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ms := c.perMethod[name]
		fmt.Fprintf(&b, "%-45s %8d %8d %8s %8s\n", name, ms.count, ms.errs,
			percentile(ms.latencies, 50), percentile(ms.latencies, 99))
	}
	return b.String()
}
