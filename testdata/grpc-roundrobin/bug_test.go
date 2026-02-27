package grpcroundrobin

import (
	"testing"
	"time"
)

// TestRoundRobinLeak reproduces the goroutine leak from grpc-go commit 27b2052c.
//
// The consumer goroutine reads exactly one address update then exits.
// The producer then tries to send a second update to the now-drained unbuffered
// channel — it blocks forever. ThreadGraph should detect 1 goroutine leak.
//
// Fix (see newFixed in bug.go): make(chan []string, 1) — buffered channel
// absorbs the second send without needing a reader.
func TestRoundRobinLeak(t *testing.T) {
	rr := newBuggy()

	// Consumer: reads exactly one address update then exits.
	go func() {
		<-rr.addrCh
		// consumer exits — no longer reading
	}()

	// Producer: sends two updates.
	// First send succeeds (consumer reads it).
	// Second send BLOCKS FOREVER — consumer has already exited.
	go func() {
		rr.addrCh <- []string{"addr1:443"} // first: succeeds
		rr.addrCh <- []string{"addr2:443"} // second: GOROUTINE LEAK
	}()

	// Give goroutines time to reach their blocked state.
	time.Sleep(300 * time.Millisecond)
}
