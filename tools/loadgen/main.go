// main.go: fixed-rate dispatch loop + worker pool.
//
// Usage (05-deploy.md section 2 note 4 profiles):
//
//	low pressure, all day:   loadgen --url http://go-api:8080 --rps 10 --duration 24h
//	daily 1h spike:          loadgen --url http://go-api:8080 --rps 50 --duration 1h
//	full comparison set:     loadgen --cases tests/api-comparison/test_cases.yaml ...
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// schedDrops counts ticks that found every worker busy; reported in the
// summary so a saturated harness is visible (achieved RPS < target RPS).
var schedDrops atomic.Int64

type rpcEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func main() {
	var (
		url         = flag.String("url", "http://localhost:8080", "target hivemind JSON-RPC base URL")
		rps         = flag.Float64("rps", 10, "target requests per second")
		duration    = flag.Duration("duration", time.Minute, "run duration")
		concurrency = flag.Int("concurrency", 8, "worker count")
		casesPath   = flag.String("cases", "", "case set: path to an api-comparison YAML (tests/api-comparison/test_cases.yaml format); empty = embedded defaults")
		timeout     = flag.Duration("timeout", 30*time.Second, "per-request HTTP timeout")
	)
	flag.Parse()

	if err := run(*url, *rps, *duration, *concurrency, *casesPath, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

func run(url string, rps float64, duration time.Duration, concurrency int, casesPath string, timeout time.Duration) error {
	cases, err := loadCases(casesPath)
	if err != nil {
		return err
	}
	if rps <= 0 || concurrency <= 0 {
		return fmt.Errorf("--rps and --concurrency must be positive")
	}
	fmt.Printf("loadgen: %d case(s) x %.1f rps, %s, %d workers -> %s\n",
		len(cases), rps, duration, concurrency, url)

	client := &http.Client{Timeout: timeout}
	coll := newCollector(int(rps) * 4)
	collDone := make(chan struct{})
	go func() { coll.run(); close(collDone) }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	// Fixed-rate dispatch: one token per tick. The work channel is
	// unbuffered ON PURPOSE: a slow target back-pressures dispatch
	// (counted as a scheduling drop) instead of queueing an unbounded
	// backlog - the harness must MEASURE lag, not create it.
	ticks := time.NewTicker(time.Duration(float64(time.Second) / rps))
	defer ticks.Stop()

	work := make(chan int) // case index
	// nextIdx drives round-robin case selection at dispatch time; nextID
	// numbers request envelopes. Separate counters: worker id allocation
	// must not disturb the dispatch sequence.
	var nextIdx, nextID atomic.Int64
	var workersDone sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		workersDone.Add(1)
		go func() {
			defer workersDone.Done()
			for idx := range work {
				c := cases[idx]
				coll.record(fire(client, url, c, int(nextID.Add(1))))
			}
		}()
	}

	start := time.Now()
dispatch:
	for {
		select {
		case <-ctx.Done():
			break dispatch
		case <-ticks.C:
			w := int(nextIdx.Add(1)) % len(cases)
			select {
			case work <- w:
			default:
				schedDrops.Add(1)
			}
		}
	}
	close(work)
	workersDone.Wait()
	coll.close()
	<-collDone

	elapsed := time.Since(start)
	fmt.Println("--- loadgen summary ---")
	if d := schedDrops.Load(); d > 0 {
		fmt.Printf("scheduling drops (workers saturated): %d\n", d)
	}
	fmt.Print(coll.summary(elapsed))
	return nil
}

// fire sends one JSON-RPC request and classifies the outcome:
//   - transport error or non-200 -> httpOK=false (counted as error)
//   - 200 with a JSON-RPC error envelope -> rpcErr=true (counted as error;
//     "method not found" against a trimmed server is a real signal)
func fire(client *http.Client, url string, c Case, id int) sample {
	body, err := c.body(id)
	if err != nil {
		return sample{method: c.API}
	}
	start := time.Now()
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	lat := time.Since(start)
	if err != nil {
		return sample{method: c.API, latency: lat}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	s := sample{method: c.API, latency: lat, httpOK: resp.StatusCode == http.StatusOK}
	if s.httpOK {
		var env rpcEnvelope
		if err := json.Unmarshal(raw, &env); err == nil && env.Error != nil {
			s.rpcErr = true
		}
	}
	return s
}
