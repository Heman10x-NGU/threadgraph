// Package grpcdialcontext reproduces the goroutine leak from grpc-go commit 4e56696c.
//
// Real bug: https://github.com/grpc/grpc-go/commit/4e56696c6c5a8cd7b711a1eb8936463559361da5
// Title: "Fix a goroutine leak in DialContext (#1424)"
// Description: "A leak happens when DialContext times out before a balancer
// returns any addresses or before a successful connection is established.
// The loop in ClientConn.lbWatcher breaks and doneChan never gets closed."
//
// Root cause: lbWatcher() iterates over a channel with range. If the outer
// DialContext times out, lbWatcher() breaks out of the loop early without
// closing doneChan. The goroutine that was waiting on doneChan then hangs
// forever on chan receive.
//
// Fix: defer close(doneChan) at the top of lbWatcher so it always closes.
package grpcdialcontext

// lbWatcherBuggy simulates the buggy lbWatcher.
// It reads address updates from notify and closes doneChan on first success.
// BUG: if notify closes before doneChan is closed, doneChan is never closed
// and any goroutine waiting on <-doneChan will block forever.
func lbWatcherBuggy(notify <-chan []string, doneChan chan struct{}) {
	for addrs := range notify {
		if len(addrs) > 0 {
			close(doneChan) // only closed on success
			return
		}
	}
	// BUG: notify closed with no successful addresses → doneChan never closed
}

// lbWatcherFixed adds defer close(doneChan) so it always closes.
func lbWatcherFixed(notify <-chan []string, doneChan chan struct{}) {
	defer func() {
		// Always close doneChan so callers never hang.
		select {
		case <-doneChan:
		default:
			close(doneChan)
		}
	}()
	for addrs := range notify {
		if len(addrs) > 0 {
			close(doneChan)
			return
		}
	}
}
