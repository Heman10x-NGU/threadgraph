package etcdleasehttp

import (
	"context"
	"testing"
	"time"
)

// TestLeaseHTTPLeak reproduces the goroutine leak.
// The simulated HTTP request takes 200ms. The context times out in 50ms.
// The caller exits the select on timeout, leaving the goroutine permanently
// blocked on the unbuffered errc channel.
// ThreadGraph should detect 1 goroutine leak (chan send).
func TestLeaseHTTPLeak(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Request takes 200ms, timeout is 50ms — caller will time out first.
	timeToLiveBuggy(ctx, 200*time.Millisecond)

	// Give the leaked goroutine time to reach its blocked state.
	time.Sleep(300 * time.Millisecond)
}

// TestLeaseHTTPFixed verifies the fix: goroutine exits cleanly even on timeout.
// ThreadGraph should detect 0 goroutine leaks.
func TestLeaseHTTPFixed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	timeToLiveFixed(ctx, 200*time.Millisecond)

	time.Sleep(300 * time.Millisecond)
}
