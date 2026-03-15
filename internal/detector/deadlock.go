package detector

import (
	"fmt"
	"sort"
	"strings"
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
		if !g.isTestOwned {
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

// detectLockCycles replaces the O(n²) AB-BA heuristic with a full
// Tarjan Strongly-Connected-Component (SCC) search on the lock-wait graph.
//
// Graph construction:
//   - Node  = a lock call site (file:line string)
//   - Edge A→B = "some goroutine recently held a lock at A and is now
//     waiting for a lock at B"
//
// Any SCC with ≥ 2 nodes represents a lock-ordering cycle:
//   - Size 2: classic AB-BA (G1 holds A waits B, G2 holds B waits A)
//   - Size N: multi-goroutine cycle (catches 3-way, 4-way, …)
//
// Complexity: O(V+E) — Tarjan's algorithm is linear in graph size.
// The previous AB-BA check was O(E²) in the number of edges.
func detectLockCycles(goroutines map[trace.GoID]*goroutineState, lastTime trace.Time) []Finding {
	type graphEdge struct {
		to  string
		gid trace.GoID
		g   *goroutineState
	}

	adj := make(map[string][]graphEdge)

	for gid, g := range goroutines {
		if !g.isBlocked || g.reason != "sync" {
			continue
		}
		if !g.isTestOwned {
			continue
		}
		if isRuntimeGoroutine(g.stack) {
			continue
		}
		for _, entry := range g.syncHistoryList() {
			if entry.location == "" || entry.location == g.location {
				continue
			}
			age := time.Duration(lastTime-entry.endTime) * time.Nanosecond
			if age > abbaStaleWindow {
				continue
			}
			adj[entry.location] = append(adj[entry.location], graphEdge{
				to: g.location, gid: gid, g: g,
			})
		}
	}

	if len(adj) == 0 {
		return nil
	}

	// Collect all nodes (both endpoints of every edge).
	nodeSet := make(map[string]bool)
	for from, edges := range adj {
		nodeSet[from] = true
		for _, e := range edges {
			nodeSet[e.to] = true
		}
	}

	// --- Tarjan's SCC ---
	type nodeState struct {
		index   int
		lowlink int
		onStack bool
		visited bool
	}
	states := make(map[string]*nodeState, len(nodeSet))
	var sccStack []string
	var sccs [][]string
	nextIndex := 0

	var strongConnect func(v string)
	strongConnect = func(v string) {
		s := &nodeState{index: nextIndex, lowlink: nextIndex, onStack: true, visited: true}
		states[v] = s
		nextIndex++
		sccStack = append(sccStack, v)

		for _, edge := range adj[v] {
			w := edge.to
			ws, visited := states[w]
			if !visited {
				strongConnect(w)
				if states[w].lowlink < s.lowlink {
					s.lowlink = states[w].lowlink
				}
			} else if ws.onStack {
				if ws.index < s.lowlink {
					s.lowlink = ws.index
				}
			}
		}

		// v is the root of a completed SCC.
		if s.lowlink == s.index {
			var scc []string
			for {
				w := sccStack[len(sccStack)-1]
				sccStack = sccStack[:len(sccStack)-1]
				states[w].onStack = false
				scc = append(scc, w)
				if w == v {
					break
				}
			}
			if len(scc) >= 2 {
				sccs = append(sccs, scc)
			}
		}
	}

	for node := range nodeSet {
		if s, ok := states[node]; !ok || !s.visited {
			strongConnect(node)
		}
	}

	// --- Map each SCC to a Finding ---
	var findings []Finding
	seenSCC := make(map[string]bool)

	for _, scc := range sccs {
		sccSet := make(map[string]bool, len(scc))
		for _, node := range scc {
			sccSet[node] = true
		}

		// Representative = goroutine blocked the longest within the cycle.
		var bestGID trace.GoID
		var bestG *goroutineState
		var bestBlocked time.Duration

		for _, from := range scc {
			for _, edge := range adj[from] {
				if !sccSet[edge.to] {
					continue
				}
				blocked := time.Duration(lastTime-edge.g.blockStart) * time.Nanosecond
				if blocked > bestBlocked {
					bestBlocked = blocked
					bestGID = edge.gid
					bestG = edge.g
				}
			}
		}
		if bestG == nil {
			continue
		}

		// Canonical dedup key: sort SCC node list and join.
		sorted := make([]string, len(scc))
		copy(sorted, scc)
		sort.Strings(sorted)
		key := strings.Join(sorted, "|")
		if seenSCC[key] {
			continue
		}
		seenSCC[key] = true

		cycleDesc := "sync (AB-BA lock inversion)"
		if len(scc) > 2 {
			cycleDesc = fmt.Sprintf("sync (%d-way lock cycle)", len(scc))
		}

		findings = append(findings, Finding{
			Kind:        KindDeadlock,
			Confidence:  ConfidenceMedium,
			GoroutineID: bestGID,
			BlockedOn:   cycleDesc,
			BlockedFor:  bestBlocked,
			Stack:       bestG.stack,
			Function:    bestG.function,
			Location:    bestG.location,
		})
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
		if !g.isTestOwned {
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
		if !g.isTestOwned {
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

// detectWaitGroupDeadlock finds goroutines blocked on sync.WaitGroup.Wait()
// whose ALL live descendant goroutines are also blocked — meaning wg.Done()
// will never be called and the Wait() will never return.
//
// Algorithm:
//  1. Find goroutines blocked on "sync" whose stack contains WaitGroup.Wait
//  2. For each such goroutine, BFS its descendant tree (via parentID)
//  3. If every live (non-dead) descendant is blocked → deadlock confirmed
//
// This catches the case Claude's HANDOFF.md identified: cockroach/1462-style
// WaitGroup + Channel deadlocks where wg.Wait() hangs because children are
// all stuck on unbuffered channels.
func detectWaitGroupDeadlock(goroutines map[trace.GoID]*goroutineState, lastTime trace.Time) []Finding {
	// Build parent → children adjacency list.
	children := make(map[trace.GoID][]trace.GoID)
	for gid, g := range goroutines {
		if g.parentID != 0 {
			children[g.parentID] = append(children[g.parentID], gid)
		}
	}

	var findings []Finding
	seen := make(map[trace.GoID]bool) // avoid duplicate reports

	for gid, g := range goroutines {
		if !g.isBlocked {
			continue
		}
		if !g.isTestOwned {
			continue
		}
		if g.reason != "sync" {
			continue
		}
		// Check if this goroutine is blocked specifically on WaitGroup.Wait
		if !strings.Contains(g.stack, "sync.(*WaitGroup).Wait") {
			continue
		}
		if seen[gid] {
			continue
		}

		// BFS all descendants of this goroutine
		allDescendantsBlocked := true
		hasDescendants := false
		queue := children[gid]
		visited := map[trace.GoID]bool{gid: true}

		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if visited[cur] {
				continue
			}
			visited[cur] = true

			desc, ok := goroutines[cur]
			if !ok {
				continue
			}
			// Skip dead goroutines — they completed normally (called wg.Done())
			if desc.goroutineDead {
				continue
			}

			hasDescendants = true

			if !desc.isBlocked {
				// A live, unblocked descendant exists — it might still call wg.Done()
				allDescendantsBlocked = false
				break
			}

			// Continue BFS into this descendant's children
			queue = append(queue, children[cur]...)
		}

		if hasDescendants && allDescendantsBlocked {
			seen[gid] = true
			blocked := time.Duration(lastTime-g.blockStart) * time.Nanosecond
			findings = append(findings, Finding{
				Kind:        KindDeadlock,
				Confidence:  ConfidenceMedium,
				GoroutineID: gid,
				BlockedOn:   "sync.WaitGroup.Wait (all descendants blocked — wg.Done() unreachable)",
				BlockedFor:  blocked,
				Stack:       g.stack,
				Function:    g.function,
				Location:    g.location,
			})
		}
	}

	return findings
}
