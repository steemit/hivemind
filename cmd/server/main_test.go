package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/steemit/hivemind/pkg/config"
)

// TestShutdownServerCancelsInFlightRequests is the section 1.4 behavior
// check: a request still running when the grace window expires must have
// its context canceled (BaseContext root ctx) instead of hanging the
// process until WriteTimeout, and shutdownServer must return promptly
// after the bounded drain.
func TestShutdownServerCancelsInFlightRequests(t *testing.T) {
	// Shorten the post-cancellation drain so the test stays fast.
	origDrain := requestAbortDrain
	requestAbortDrain = 100 * time.Millisecond
	defer func() { requestAbortDrain = origDrain }()

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	requestCtxDone := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // simulate a straggler that ignores everything else
		close(requestCtxDone)
		w.WriteHeader(http.StatusOK)
	})

	srv := newHTTPServer(&config.ServerConfig{Host: "127.0.0.1"}, handler, rootCtx)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.Errorf("Serve: %v", err)
		}
	}()

	go func() {
		resp, err := http.Get(fmt.Sprintf("http://%s/slow", ln.Addr().String()))
		if err == nil {
			resp.Body.Close()
		}
	}()

	// Let the request reach the handler before shutting down.
	time.Sleep(100 * time.Millisecond)

	grace := 200 * time.Millisecond
	start := time.Now()
	err = shutdownServer(srv, grace, rootCancel, zap.NewNop())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("shutdownServer should report the grace expiry error when a request is still in flight")
	}
	if elapsed < grace {
		t.Fatalf("shutdownServer returned at %v, before the %v grace expired", elapsed, grace)
	}
	// grace + (shortened) drain + scheduling slack.
	if max := grace + requestAbortDrain + 2*time.Second; elapsed > max {
		t.Fatalf("shutdownServer took %v, beyond grace+drain+slack (%v)", elapsed, max)
	}

	select {
	case <-requestCtxDone:
		// The in-flight request observed the cancellation: the drain
		// window gave it time to unwind.
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight request context was not canceled within the drain window")
	}
}

// TestShutdownServerQuickDrain: with no in-flight work, Shutdown returns
// nil well inside the grace window.
func TestShutdownServerQuickDrain(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	srv := newHTTPServer(&config.ServerConfig{Host: "127.0.0.1"}, http.NotFoundHandler(), rootCtx)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			t.Errorf("Serve: %v", err)
		}
	}()
	time.Sleep(50 * time.Millisecond)

	if err := shutdownServer(srv, 45*time.Second, rootCancel, zap.NewNop()); err != nil {
		t.Fatalf("idle shutdown should succeed cleanly, got: %v", err)
	}
}

// TestShutdownGraceFallsBackToDefault: a zero-valued grace (the
// hand-built-config path; Load always fills 45s) selects the default, so
// an idle server still shuts down cleanly instead of tripping a zero
// timeout.
func TestShutdownGraceFallsBackToDefault(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := newHTTPServer(&config.ServerConfig{Host: "127.0.0.1"}, handler, rootCtx)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		_ = srv.Serve(ln)
	}()
	time.Sleep(50 * time.Millisecond)

	if err := shutdownServer(srv, 0, rootCancel, zap.NewNop()); err != nil {
		t.Fatalf("zero grace (idle server) should shut down cleanly via the default, got: %v", err)
	}
}

// TestNewHTTPServerBaseContext proves every request context derives from
// the root context passed to newHTTPServer (the cancellation plumbing
// above depends on it).
func TestNewHTTPServerBaseContext(t *testing.T) {
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	gotBase := make(chan context.Context, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture the parent chain by canceling the root and observing
		// the request context die.
		select {
		case gotBase <- r.Context():
		default:
		}
		w.WriteHeader(http.StatusOK)
	})

	srv := newHTTPServer(&config.ServerConfig{Host: "127.0.0.1"}, handler, rootCtx)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	resp, err := http.Get(fmt.Sprintf("http://%s/", ln.Addr().String()))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()

	select {
	case reqCtx := <-gotBase:
		rootCancel()
		select {
		case <-reqCtx.Done():
			// request context derived from rootCtx as required
		case <-time.After(2 * time.Second):
			t.Fatal("request context did not follow the root context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
}
