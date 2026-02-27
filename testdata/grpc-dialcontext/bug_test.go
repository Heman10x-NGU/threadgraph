package grpcdialcontext

import (
	"testing"
	"time"
)

// TestDialContextLeak reproduces the goroutine leak from grpc-go commit 4e56696c.
//
// The balancer channel (notify) closes without sending any valid addresses.
// lbWatcherBuggy exits the loop without closing doneChan.
// The goroutine waiting on <-doneChan blocks forever.
// ThreadGraph should detect 1 goroutine leak (chan receive).
//
// Fix (see lbWatcherFixed in bug.go): defer close(doneChan) at the start of
// lbWatcher so it always closes doneChan regardless of how the loop exits.
func TestDialContextLeak(t *testing.T) {
	notify := make(chan []string)
	doneChan := make(chan struct{})

	go lbWatcherBuggy(notify, doneChan)

	// Balancer sends empty update (no valid addresses), then closes.
	notify <- []string{} // empty — lbWatcherBuggy does not close doneChan
	close(notify)        // notify closed — lbWatcherBuggy exits loop, doneChan still open

	// This goroutine waits for doneChan — blocks forever (the goroutine leak).
	go func() {
		<-doneChan // GOROUTINE LEAK: doneChan is never closed
	}()

	// Give the leaked goroutine time to reach its blocked state.
	time.Sleep(300 * time.Millisecond)
}
