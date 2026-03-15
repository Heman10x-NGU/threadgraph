package racecondition

import (
	"sync"
	"testing"
)

// TestRacyCounter demonstrates a classic data race: multiple goroutines
// incrementing a shared counter without synchronization.
// The race detector should flag concurrent read/write access to `counter`.
func TestRacyCounter(t *testing.T) {
	counter := 0
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Deliberate data race: unsynchronized read-modify-write
			counter++
		}()
	}

	wg.Wait()
	t.Logf("Counter: %d (expected 10, may be less due to race)", counter)
}
