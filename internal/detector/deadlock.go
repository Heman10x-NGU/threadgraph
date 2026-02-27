package detector

import (
	"time"

	"golang.org/x/exp/trace"
)

// abbaStaleWindow is how old a sync history entry can be before we ignore it
// for AB-BA detection. Prevents false positives from unrelated lock sequences.
const abbaStaleWindow = 5 * time.Second

// deadlockThreshold is how long a goroutine must be blocked on a mutex
// before it is considered a potential deadlock participant.
const deadlockThreshold = 500 * time.Millisecond

// detectDeadlocks identifies partial deadlocks: groups of goroutines all
// blocked on mutex lock for longer than the threshold.
//
// The Go runtime panics on full deadlock, so we target partial deadlocks
// (a subset of goroutines stuck). We group by call site similarity:
// if 2+ goroutines are stuck at the same mutex-lock call site for a long
// time, that is flagged as a potential deadlock.
func detectDeadlocks(goroutines map[trace.GoID]*goroutineState, lastTime trace.Time, opts Options) []Finding {
	threshold := deadlockThreshold
	if opts.MinBlock > 0 && opts.MinBlock < threshold {
		threshold = opts.MinBlock
	}

	// Group goroutines by (reason, location) — same call site
	type key struct {
		reason   string
		location string
	}
	groups := make(map[key][]struct {
		gid     trace.GoID
		g       *goroutineState
		blocked time.Duration
	})

	for gid, g := range goroutines {
		if !g.isBlocked {
			continue
		}
		if isRuntimeGoroutine(g.stack) {
			continue
		}
		// Go execution traces use "sync" as the reason for all sync primitives.
		if g.reason != "sync" {
			continue
		}

		blocked := time.Duration(lastTime-g.blockStart) * time.Nanosecond
		if blocked < threshold {
			continue
		}

		k := key{reason: g.reason, location: g.location}
		groups[k] = append(groups[k], struct {
			gid     trace.GoID
			g       *goroutineState
			blocked time.Duration
		}{gid, g, blocked})
	}

	var findings []Finding
	for k, members := range groups {
		if len(members) < 1 {
			continue
		}

		// Use the longest-blocked goroutine as the representative
		best := members[0]
		for _, m := range members[1:] {
			if m.blocked > best.blocked {
				best = m
			}
		}

		findings = append(findings, Finding{
			Kind:        KindDeadlock,
			Confidence:  ConfidenceMedium,
			GoroutineID: best.gid,
			BlockedOn:   k.reason,
			BlockedFor:  best.blocked,
			Stack:       best.g.stack,
			Function:    best.g.function,
			Location:    best.g.location,
		})
	}

	return findings
}

// detectABBA detects AB-BA lock order inversions using lock sequence history.
//
// Algorithm: for each goroutine G1 that is blocked on "sync" at location L_wait,
// we look at its recent sync-unblock history (goroutineState.syncHistory) — each
// entry represents a lock site G1 recently acquired. For each such acquired lock
// L_held, we add the directed edge L_held → L_wait ("G1 holds L_held, waits for
// L_wait"). If goroutine G2 has the reverse edge L_wait → L_held, it's an AB-BA
// deadlock: G1 holds A and waits for B, G2 holds B and waits for A.
//
// Using syncHistory instead of only prevSyncLocation allows detecting AB-BA when
// the first lock was acquired multiple operations ago (multi-step lock sequences).
//
// Confidence is Medium because two goroutines at the same call site might
// use different mutex instances.
func detectABBA(goroutines map[trace.GoID]*goroutineState, lastTime trace.Time) []Finding {
	type lockEdge struct {
		from string // lock held (recently unblocked from sync at this site)
		to   string // lock being waited on (current block site)
		gid  trace.GoID
		g    *goroutineState
	}

	var edges []lockEdge
	for gid, g := range goroutines {
		if !g.isBlocked || g.reason != "sync" {
			continue
		}
		if isRuntimeGoroutine(g.stack) {
			continue
		}

		// Use the full sync history to catch multi-step lock sequences.
		for _, entry := range g.syncHistoryList() {
			if entry.location == "" || entry.location == g.location {
				// Same call site: single-goroutine double lock, caught by detectDeadlocks.
				continue
			}
			age := time.Duration(lastTime-entry.endTime) * time.Nanosecond
			if age > abbaStaleWindow {
				// Too old: the lock was likely released long ago.
				continue
			}
			edges = append(edges, lockEdge{
				from: entry.location,
				to:   g.location,
				gid:  gid,
				g:    g,
			})
		}
	}

	var findings []Finding
	seen := make(map[[2]string]bool) // deduplicate by edge pair

	for i := 0; i < len(edges); i++ {
		for j := i + 1; j < len(edges); j++ {
			e1, e2 := edges[i], edges[j]
			if e1.from != e2.to || e1.to != e2.from {
				continue
			}
			// Canonical key: sort the two locations so (A,B) == (B,A)
			k := [2]string{e1.from, e1.to}
			if k[0] > k[1] {
				k[0], k[1] = k[1], k[0]
			}
			if seen[k] {
				continue
			}
			seen[k] = true

			blocked := time.Duration(lastTime-e1.g.blockStart) * time.Nanosecond
			findings = append(findings, Finding{
				Kind:        KindDeadlock,
				Confidence:  ConfidenceMedium,
				GoroutineID: e1.gid,
				BlockedOn:   "sync (AB-BA lock inversion)",
				BlockedFor:  blocked,
				Stack:       e1.g.stack,
				Function:    e1.g.function,
				Location:    e1.g.location,
			})
		}
	}

	return findings
}

// detectChanLockCycle detects the pattern: goroutine G1 holds a mutex (last
// unblocked from "sync" at L_lock) and is now blocked on a channel operation,
// while goroutine G2 is blocked on "sync" at L_lock waiting for the same
// mutex G1 holds. This creates a cycle: G1 waits for channel (which G2 would
// service), G2 waits for G1's lock.
func detectChanLockCycle(goroutines map[trace.GoID]*goroutineState, lastTime trace.Time) []Finding {
	// Build map: sync call site → goroutines blocked there waiting for the lock
	lockWaiters := make(map[string]bool)
	for _, g := range goroutines {
		if !g.isBlocked || g.reason != "sync" {
			continue
		}
		if isRuntimeGoroutine(g.stack) {
			continue
		}
		lockWaiters[g.location] = true
	}

	var findings []Finding
	seen := make(map[string]bool) // deduplicate by prevSyncLocation

	for gid, g := range goroutines {
		if !g.isBlocked {
			continue
		}
		if g.reason != "chan send" && g.reason != "chan receive" {
			continue
		}
		if g.prevSyncLocation == "" {
			continue
		}
		if isRuntimeGoroutine(g.stack) {
			continue
		}
		age := time.Duration(lastTime-g.prevSyncEndTime) * time.Nanosecond
		if age > abbaStaleWindow {
			continue
		}
		if !lockWaiters[g.prevSyncLocation] {
			continue
		}
		if seen[g.prevSyncLocation] {
			continue
		}
		seen[g.prevSyncLocation] = true

		blocked := time.Duration(lastTime-g.blockStart) * time.Nanosecond
		findings = append(findings, Finding{
			Kind:        KindDeadlock,
			Confidence:  ConfidenceMedium,
			GoroutineID: gid,
			BlockedOn:   g.reason + " (holds lock; lock waiter cannot unblock channel)",
			BlockedFor:  blocked,
			Stack:       g.stack,
			Function:    g.function,
			Location:    g.location,
		})
	}

	return findings
}
