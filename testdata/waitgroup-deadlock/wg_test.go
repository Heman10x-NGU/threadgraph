package waitgroupdeadlock

import (
	"sync"
	"testing"
	"time"
)

// TestWaitGroupDeadlock demonstrates a WaitGroup deadlock: the main goroutine
// calls wg.Wait(), but the spawned goroutines are all blocked on a channel
// that nobody will ever write to, so wg.Done() is never called.
//
// Timeline:
//  1. Main goroutine calls wg.Add(3) and spawns 3 goroutines
//  2. Each goroutine tries to receive from `ch` (unbuffered, no sender)
//  3. Main goroutine calls wg.Wait() — blocks forever
//  4. All 4 goroutines are stuck: 3 on chan receive, 1 on WaitGroup.Wait
//
// ThreadGraph should detect this as either:
//   - 3 goroutine leaks (blocked on chan receive), or
//   - A WaitGroup deadlock (WaitGroup.Wait with all descendants blocked)
func TestWaitGroupDeadlock(t *testing.T) {
	ch := make(chan int)
	var wg sync.WaitGroup

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// This blocks forever — no goroutine ever sends to ch
			val := <-ch
			_ = val
		}(i)
	}

	// Give goroutines time to start and block
	time.Sleep(50 * time.Millisecond)

	// This will block forever because the goroutines above
	// can never call wg.Done() (they're stuck on chan receive)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Should never happen
	case <-time.After(2 * time.Second):
		t.Log("WaitGroup deadlock confirmed: wg.Wait() never returned")
	}
}
