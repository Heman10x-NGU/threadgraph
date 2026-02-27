package chanreceiveleak

import (
	"testing"
	"time"
)

// TestChanReceiveLeak shows a goroutine permanently blocked on chan receive.
//
// The processing goroutine exits without sending (error path), leaving the
// caller goroutine blocked on <-resultCh forever.
// ThreadGraph should detect 1 goroutine leak (chan receive).
//
// Fix (see requestWithResultFixed in bug.go): use a buffered channel, or
// always send on the channel before returning (even on error paths).
func TestChanReceiveLeak(t *testing.T) {
	// Call from a goroutine because requestWithResultBuggy itself will block.
	go func() {
		requestWithResultBuggy(true) // caller goroutine blocks here on chan receive
	}()

	// Give the leak time to materialize.
	time.Sleep(300 * time.Millisecond)
}
