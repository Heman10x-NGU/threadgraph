// Package grpcroundrobin reproduces the goroutine leak from grpc-go commit 27b2052c.
//
// Real bug: https://github.com/grpc/grpc-go/commit/27b2052c9524abc45ae991d6a402ddb91f06ba03
// Title: "fix deadlock of roundrobin balancer (#1353)"
//
// Root cause: addrCh was created as an unbuffered channel.
// The watchAddrUpdates() function sends to addrCh while holding no lock.
// When the consumer goroutine exits (e.g. on error), any in-flight or
// subsequent send to the unbuffered channel blocks forever — goroutine leak.
//
// Fix: make(chan []string, 1) — buffered channel absorbs the send.
package grpcroundrobin

import "errors"

var errClosing = errors.New("client conn closing")

type roundRobin struct {
	addrCh chan []string
	done   bool
}

// watchAddrUpdates sends a new address list to the consumer.
// BUG: if the consumer has already exited, this blocks forever.
func (rr *roundRobin) watchAddrUpdates(addrs []string) error {
	if rr.done {
		return errClosing
	}
	rr.addrCh <- addrs // GOROUTINE LEAK: blocks when no reader
	return nil
}

// newBuggy creates a roundRobin with an unbuffered channel (the bug).
func newBuggy() *roundRobin {
	return &roundRobin{
		addrCh: make(chan []string), // BUG: should be make(chan []string, 1)
	}
}

// newFixed creates a roundRobin with a buffered channel (the fix).
func newFixed() *roundRobin {
	return &roundRobin{
		addrCh: make(chan []string, 1), // FIX: buffered
	}
}
