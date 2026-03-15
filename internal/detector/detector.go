package detector

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"golang.org/x/exp/trace"
)

// Kind describes the category of finding.
type Kind string

const (
	KindGoroutineLeak Kind = "goroutine_leak"
	KindDeadlock      Kind = "deadlock"
	KindLongBlock     Kind = "long_block"
	KindLockLeak      Kind = "lock_leak"  // static analysis: lock not released on all paths
	KindLockOrder     Kind = "lock_order" // static analysis: lock ordering cycle (AB-BA potential deadlock)
	KindDataRace      Kind = "data_race"  // race detector: concurrent unsynchronized memory access
)

// Confidence indicates how certain we are about a finding.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Options controls analysis behavior.
type Options struct {
	MinBlock      time.Duration
	DebugFiltered bool // print goroutines filtered out of findings to stderr
}

// Finding represents a single detected concurrency issue.
type Finding struct {
	Kind        Kind
	Confidence  Confidence
	GoroutineID trace.GoID
	BlockedOn   string
	BlockedFor  time.Duration
	Stack       string
	Function    string // top user-code function
	Location    string // file:line of top user-code frame
	// Count is the number of goroutines with the same (kind, location)
	// signature. After deduplication, a Count > 1 means multiple goroutines
	// are exhibiting the same bug from the same call site.
	Count int
}

// Result holds all findings from one analysis pass.
type Result struct {
	TraceFile          string
	DurationMs         int64
	GoroutinesAnalyzed int
	Findings           []Finding
}

// syncHistorySize is the number of recent sync-unblock sites to remember per goroutine.
const syncHistorySize = 5

// syncEntry records one sync-primitive unblock (= lock acquisition site).
type syncEntry struct {
	location string
	endTime  trace.Time
}

// goroutineState is internal parse state per goroutine.
type goroutineState struct {
	reason     string
	stack      string
	function   string
	location   string
	blockStart trace.Time
	isBlocked  bool

	// provenance: filled when the goroutine is first created
	creationStack    string // stack at go func() call site
	creationSeen     bool   // true = we saw GoNotExist→GoRunnable for this goroutine
	creationFunction string // top user-code function at creation site
	creationLocation string // file:line at creation site

	// transient long block: most recent completed block that exceeded threshold
	prevLongBlockReason   string
	prevLongBlockStack    string
	prevLongBlockFunction string
	prevLongBlockLocation string
	prevLongBlockDuration time.Duration

	// Lock sequence history: circular buffer of last syncHistorySize sync unblocks.
	// Used for both AB-BA detection (single prevSyncLocation) and multi-step cycle detection.
	syncHistory    [syncHistorySize]syncEntry
	syncHistoryIdx int // total entries written (not capped at buffer size)

	// prevSyncLocation is the most-recent sync unblock site (kept for
	// chan+lock cycle detection which only needs the single most-recent).
	prevSyncLocation string
	prevSyncEndTime  trace.Time

	// Death tracking: set when goroutine transitions to GoNotExist.
	goroutineDead bool

	// Provenance tree: parent goroutine ID (0 if root or unknown).
	// Set when a GoCreate event is observed while another goroutine is running.
	parentID trace.GoID

	// isTestOwned is set by markTestOwned() after the full trace parse.
	// True if this goroutine is descended from the testing framework.
	// Only test-owned goroutines are reported as findings.
	isTestOwned bool
}

// syncHistoryList returns recent sync unblock entries, most recent first.
func (g *goroutineState) syncHistoryList() []syncEntry {
	n := g.syncHistoryIdx
	if n > syncHistorySize {
		n = syncHistorySize
	}
	result := make([]syncEntry, 0, n)
	for i := 0; i < n; i++ {
		pos := (g.syncHistoryIdx - 1 - i) % syncHistorySize
		if pos < 0 {
			pos += syncHistorySize
		}
		if g.syncHistory[pos].location != "" {
			result = append(result, g.syncHistory[pos])
		}
	}
	return result
}

// Analyze reads a trace file and returns all findings.
func Analyze(path string, opts Options) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r, err := trace.NewReader(f)
	if err != nil {
		return nil, err
	}

	goroutines := make(map[trace.GoID]*goroutineState)
	var firstTime, lastTime trace.Time
	first := true

	for {
		ev, err := r.ReadEvent()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("warn: read event: %v", err)
			break
		}

		if first {
			firstTime = ev.Time()
			first = false
		}
		lastTime = ev.Time()

		if ev.Kind() != trace.EventStateTransition {
			continue
		}

		st := ev.StateTransition()
		if st.Resource.Kind != trace.ResourceGoroutine {
			continue
		}

		gid := st.Resource.Goroutine()
		from, to := st.Goroutine()

		if goroutines[gid] == nil {
			goroutines[gid] = &goroutineState{}
		}
		g := goroutines[gid]

		// Goroutine created — record provenance (creation stack + parent ID).
		// ev.Goroutine() is the goroutine that executed the 'go' statement;
		// st.Resource.Goroutine() (= gid) is the newly created child.
		if from == trace.GoNotExist {
			g.parentID = ev.Goroutine()
			g.creationStack, g.creationFunction, g.creationLocation = extractStack(st.Stack)
			g.creationSeen = true
		}

		// Goroutine died — record for orphan detection
		if to == trace.GoNotExist {
			g.goroutineDead = true
		}

		// Goroutine just blocked
		if from.Executing() && to == trace.GoWaiting {
			g.isBlocked = true
			g.reason = st.Reason
			g.blockStart = ev.Time()
			g.stack, g.function, g.location = extractStack(st.Stack)
		}

		// Goroutine unblocked — clear blocked state on any transition away from GoWaiting.
		// This covers both:
		//   GoWaiting → Executing  (goroutine resumed directly)
		//   GoWaiting → GoRunnable (goroutine woken by close(ch), signal, etc.)
		if from == trace.GoWaiting && (to.Executing() || to == trace.GoRunnable) {
			// Before clearing, capture long-duration sync blocks.
			if g.isBlocked && g.reason == "sync" {
				dur := time.Duration(ev.Time()-g.blockStart) * time.Nanosecond
				if dur > g.prevLongBlockDuration {
					g.prevLongBlockReason = g.reason
					g.prevLongBlockStack = g.stack
					g.prevLongBlockFunction = g.function
					g.prevLongBlockLocation = g.location
					g.prevLongBlockDuration = dur
				}
				// Push to sync history (circular buffer).
				pos := g.syncHistoryIdx % syncHistorySize
				g.syncHistory[pos] = syncEntry{
					location: g.location,
					endTime:  ev.Time(),
				}
				g.syncHistoryIdx++
				// Keep single-entry shortcut for chan+lock cycle detection.
				g.prevSyncLocation = g.location
				g.prevSyncEndTime = ev.Time()
			}
			g.isBlocked = false
			g.reason = ""
			g.stack = ""
			g.function = ""
			g.location = ""
		}
	}

	traceDuration := time.Duration(lastTime-firstTime) * time.Nanosecond

	// Walk the goroutine parent-child tree to mark all goroutines descended
	// from the testing framework as test-owned. Only test-owned goroutines
	// are eligible for findings.
	markTestOwned(goroutines)

	if opts.DebugFiltered {
		printDebugFiltered(goroutines, lastTime, traceDuration)
	}

	findings := detectLeaks(goroutines, lastTime, traceDuration, opts)
	findings = append(findings, detectDeadlocks(goroutines, lastTime, opts)...)
	findings = append(findings, detectTransientBlocks(goroutines, opts)...)
	findings = append(findings, detectLockCycles(goroutines, lastTime)...)
	findings = append(findings, detectChanLockCycle(goroutines, lastTime)...)
	findings = append(findings, detectOrphans(goroutines, traceDuration)...)
	findings = append(findings, detectWaitGroupDeadlock(goroutines, lastTime)...)

	// Deduplicate: collapse N goroutines with the same (kind, location) into
	// one finding with Count = N. Reduces noise on leaks that affect many
	// goroutines simultaneously from the same call site.
	findings = deduplicateFindings(findings)

	return &Result{
		TraceFile:          path,
		DurationMs:         traceDuration.Milliseconds(),
		GoroutinesAnalyzed: len(goroutines),
		Findings:           findings,
	}, nil
}

// deduplicateFindings collapses findings with the same (kind, location) into a
// single representative, setting Count to the total number of affected
// goroutines. The goroutine blocked the longest is kept as the representative.
// Insertion order is preserved so output is deterministic.
func deduplicateFindings(findings []Finding) []Finding {
	type key struct {
		kind     Kind
		location string
	}
	type group struct {
		rep   Finding
		count int
	}

	groups := make(map[key]*group)
	var order []key

	for _, f := range findings {
		k := key{f.Kind, f.Location}
		if g, ok := groups[k]; ok {
			g.count++
			if f.BlockedFor > g.rep.BlockedFor {
				saved := g.count
				g.rep = f
				g.count = saved
			}
		} else {
			cp := f
			groups[k] = &group{rep: cp, count: 1}
			order = append(order, k)
		}
	}

	out := make([]Finding, 0, len(order))
	for _, k := range order {
		g := groups[k]
		g.rep.Count = g.count
		out = append(out, g.rep)
	}
	return out
}

// markTestOwned performs a BFS from "test root" goroutines (those created by
// the testing framework or that pre-existed the trace as the main goroutine)
// and marks every reachable descendant as isTestOwned = true.
//
// Only test-owned goroutines are reported as findings; this eliminates false
// positives from pre-test global background workers and goroutines spawned by
// unrelated infrastructure that happens to be running during the test.
func markTestOwned(goroutines map[trace.GoID]*goroutineState) {
	// Build parent→children adjacency list from captured parentIDs.
	children := make(map[trace.GoID][]trace.GoID)
	for gid, g := range goroutines {
		if g.parentID != 0 {
			children[g.parentID] = append(children[g.parentID], gid)
		}
	}

	owned := make(map[trace.GoID]bool)
	var queue []trace.GoID

	for gid, g := range goroutines {
		if isTestRoot(g) {
			owned[gid] = true
			queue = append(queue, gid)
		}
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, child := range children[cur] {
			if !owned[child] {
				owned[child] = true
				queue = append(queue, child)
			}
		}
	}

	for gid, g := range goroutines {
		g.isTestOwned = owned[gid]
	}
}

// isTestRoot returns true if g should be treated as a root of the test goroutine
// tree, meaning all its descendants are considered test-owned.
//
// Three categories qualify:
//  1. Goroutines directly created by testing.tRunner (the per-test goroutine).
//  2. Goroutines currently executing testing.tRunner (same, seen mid-trace).
//  3. Pre-trace goroutines (!creationSeen): in 'go test', goroutine 1 (main)
//     always pre-exists and is the ancestor of all testing goroutines. We
//     include all pre-trace goroutines as roots; runtime-only descendants are
//     still excluded by the isRuntimeGoroutine(g.stack) check in detectors.
func isTestRoot(g *goroutineState) bool {
	return strings.Contains(g.creationStack, "testing.tRunner") ||
		strings.Contains(g.creationStack, "testing.runTests") ||
		strings.Contains(g.stack, "testing.tRunner") ||
		!g.creationSeen
}

// printDebugFiltered prints all blocked goroutines with their filter status.
// Useful for diagnosing why specific goroutines are not being reported.
func printDebugFiltered(goroutines map[trace.GoID]*goroutineState, lastTime trace.Time, traceDuration time.Duration) {
	fmt.Fprintln(os.Stderr, "=== --debug-filtered: blocked goroutines at trace end ===")
	for gid, g := range goroutines {
		if !g.isBlocked {
			continue
		}
		blocked := time.Duration(lastTime-g.blockStart) * time.Nanosecond
		runtimeBlocking := isRuntimeGoroutine(g.stack)
		runtimeCreation := !g.creationSeen || isRuntimeGoroutine(g.creationStack)
		isChan := g.reason == "chan send" || g.reason == "chan receive"

		filterReason := ""
		if runtimeBlocking {
			filterReason = "blocking-stack=runtime"
		} else if g.reason == "sleep" {
			filterReason = "reason=sleep"
		} else if isChan && runtimeCreation {
			filterReason = "chan-block+creation-stack=runtime"
		} else if !isChan && g.reason != "sync" && runtimeCreation {
			filterReason = "non-chan-sync-block+creation-stack=runtime"
		}

		status := "REPORTED"
		if filterReason != "" {
			status = "FILTERED(" + filterReason + ")"
		}

		fmt.Fprintf(os.Stderr, "  G%-6d %-40s reason=%-12s blocked=%-10v %s\n",
			gid, truncate(g.location, 40), g.reason, blocked, status)
		if g.stack != "" {
			for _, line := range splitLines(g.stack) {
				fmt.Fprintf(os.Stderr, "            %s\n", line)
			}
		}
		if runtimeCreation && g.creationStack != "" {
			fmt.Fprintf(os.Stderr, "          created at (runtime):\n")
			for _, line := range splitLines(g.creationStack) {
				fmt.Fprintf(os.Stderr, "            %s\n", line)
			}
		}
	}

	fmt.Fprintln(os.Stderr, "=== alive non-blocked goroutines ===")
	for gid, g := range goroutines {
		if g.isBlocked || g.goroutineDead || !g.creationSeen {
			continue
		}
		runtimeCreation := isRuntimeGoroutine(g.creationStack)
		fmt.Fprintf(os.Stderr, "  G%-6d %-40s runtimeCreation=%v\n",
			gid, truncate(g.creationLocation, 40), runtimeCreation)
	}
	fmt.Fprintln(os.Stderr, "=== end debug-filtered ===")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n+3:]
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			line := s[start:i]
			if line != "" {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
