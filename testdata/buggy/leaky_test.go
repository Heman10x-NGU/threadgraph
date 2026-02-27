package main

import (
	"testing"
	"time"
)

// TestLeakyGoroutines intentionally creates goroutine leaks for ThreadGraph to detect.
// Each call to leakyHandler spawns an anonymous goroutine that blocks forever on
// an unbuffered channel send with no reader.
func TestLeakyGoroutines(t *testing.T) {
	for i := 0; i < 5; i++ {
		leakyHandler(i)
	}
	// Let the goroutines settle into their blocked state before the trace ends.
	time.Sleep(300 * time.Millisecond)
}
