package detector

import (
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"golang.org/x/exp/trace"
)

// Kind describes the category of finding.
type Kind string

const (
	KindGoroutineLeak Kind = "goroutine_leak"
	KindDeadlock      Kind = "deadlock"
	KindLongBlock     Kind = "long_block"
	KindLockLeak      Kind = "lock_leak" // static analysis: lock not released on all paths
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
	prevLongBlockReason    string
	prevLongBlockStack     string
	prevLongBlockFunction  string
	prevLongBlockLocation  string
	prevLongBlockDuration  time.Duration

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

		// Goroutine created — record provenance (creation stack)
		if from == trace.GoNotExist {
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

	if opts.DebugFiltered {
		printDebugFiltered(goroutines, lastTime, traceDuration)
	}

	findings := detectLeaks(goroutines, lastTime, opts)
	findings = append(findings, detectDeadlocks(goroutines, lastTime, opts)...)
	findings = append(findings, detectTransientBlocks(goroutines, opts)...)
	findings = append(findings, detectABBA(goroutines, lastTime)...)
	findings = append(findings, detectChanLockCycle(goroutines, lastTime)...)
	findings = append(findings, detectOrphans(goroutines, traceDuration)...)

	return &Result{
		TraceFile:          path,
		DurationMs:         traceDuration.Milliseconds(),
		GoroutinesAnalyzed: len(goroutines),
		Findings:           findings,
	}, nil
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
