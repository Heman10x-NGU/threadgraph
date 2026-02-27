// Package etcdleasehttp reproduces the goroutine leak from etcd commit 7b7feb46.
//
// Real bug: https://github.com/etcd-io/etcd/commit/7b7feb46fcf13da75f93797740ffc6034bb585ff
// Title: "leasehttp: buffer error channel to prevent goroutine leak"
// Comment in fix: "buffer errc channel so that errc don't block inside the go routine"
//
// Root cause: errc was unbuffered. A goroutine sends the HTTP response/error
// to errc. The caller uses select with a context timeout. If the context
// expires before the goroutine finishes, the caller exits the select —
// but the goroutine is now permanently blocked trying to send to errc
// (nobody is reading it anymore).
//
// Fix: make(chan error, 2) — buffered so the goroutine can send and exit.
package etcdleasehttp

import (
	"context"
	"fmt"
	"time"
)

// timeToLiveBuggy simulates the buggy TimeToLiveHTTP function.
// It starts a goroutine to make a (simulated) HTTP request and tries to
// return the result. If the context expires, the goroutine leaks.
func timeToLiveBuggy(ctx context.Context, delay time.Duration) error {
	// BUG: unbuffered channel
	errc := make(chan error) // should be make(chan error, 2)

	go func() {
		// Simulate slow HTTP request
		time.Sleep(delay)
		// Try to send result — if caller already left due to timeout, this BLOCKS FOREVER
		errc <- fmt.Errorf("lease not found") // GOROUTINE LEAK when caller timed out
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		// Caller gives up — goroutine above is now permanently blocked on errc <-
		return ctx.Err()
	}
}

// timeToLiveFixed simulates the fixed version with a buffered error channel.
func timeToLiveFixed(ctx context.Context, delay time.Duration) error {
	// FIX: buffered channel — goroutine can always send and exit cleanly
	errc := make(chan error, 2)

	go func() {
		time.Sleep(delay)
		errc <- fmt.Errorf("lease not found") // always succeeds, goroutine exits
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
