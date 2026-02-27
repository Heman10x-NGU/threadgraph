// Package chanreceiveleak demonstrates a goroutine blocked forever on chan receive.
//
// This pattern appears across many bugs in the dataset: a goroutine waits for
// a result on a channel, but the sender exits early (on error, timeout, or
// panic) without ever sending. The receiver is permanently abandoned.
//
// Seen in: etcd kv_test.go (commit 1d8813052), multiple Docker and k8s bugs.
// Common pattern in concurrent request/response code where the response
// channel is created but the responder exits before sending.
package chanreceiveleak

import (
	"errors"
	"time"
)

var errTimeout = errors.New("operation timed out")

// requestWithResultBuggy sends a "request" and waits for a response on a
// channel. If the processor goroutine encounters an error and exits without
// sending on resultCh, the caller goroutine blocks forever.
func requestWithResultBuggy(shouldFail bool) (string, error) {
	resultCh := make(chan string) // unbuffered

	go func() {
		// Simulate processing
		time.Sleep(20 * time.Millisecond)
		if shouldFail {
			// BUG: returns without sending to resultCh.
			// The goroutine calling requestWithResultBuggy is now blocked forever.
			return
		}
		resultCh <- "success"
	}()

	result := <-resultCh // GOROUTINE LEAK when shouldFail=true: blocks forever
	return result, nil
}

// requestWithResultFixed uses a buffered channel or always sends before returning.
func requestWithResultFixed(shouldFail bool) (string, error) {
	resultCh := make(chan string, 1) // buffered — or use select with default

	go func() {
		time.Sleep(20 * time.Millisecond)
		if shouldFail {
			resultCh <- "" // always send, even on failure (caller checks error)
			return
		}
		resultCh <- "success"
	}()

	result := <-resultCh
	if result == "" && shouldFail {
		return "", errTimeout
	}
	return result, nil
}
