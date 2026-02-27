package detector

import (
	"time"

	"golang.org/x/exp/trace"
)

// detectLeaks finds goroutines still blocked at the end of the trace.
// Goroutines blocked on "chan send" or "chan receive" are classified as leaks.
// Others blocked longer than minBlock are classified as long blocks.
func detectLeaks(goroutines map[trace.GoID]*goroutineState, lastTime trace.Time, opts Options) []Finding {
	var findings []Finding

	for gid, g := range goroutines {
		if !g.isBlocked {
			continue
		}
		// Filter by blocking stack (always applies)
		if isRuntimeGoroutine(g.stack) {
			continue
		}
		// Skip the main goroutine sleeping intentionally
		if g.reason == "sleep" {
			continue
		}

		isChanBlock := g.reason == "chan send" || g.reason == "chan receive"
		// Go execution traces use "sync" as the reason for ALL sync primitives
		// (sync.Mutex.Lock, sync.RWMutex.Lock/RLock, sync.Cond.Wait, etc.)
		isSyncBlock := g.reason == "sync"

		// Provenance filter: apply only to channel blocks.
		// For sync blocks (mutex/RWMutex/condvar) we trust the blocking stack
		// check above — test goroutines that deadlock on mutexes have user code
		// in their blocking stack but runtime/testing code in their creation
		// stack, so the creation-stack filter would incorrectly exclude them.
		if isChanBlock {
			if !g.creationSeen {
				continue
			}
			// Only filter goroutines created by non-testing runtime code (e.g.
			// net/http workers). Goroutines created by testing.T.Run have a
			// testing-only creation stack but ARE test-owned and should be reported.
			if isNonTestRuntimeGoroutine(g.creationStack) {
				continue
			}
		}
		// For non-channel, non-sync blocks (select, etc.) still require
		// provenance to avoid background worker false positives.
		if !isChanBlock && !isSyncBlock {
			if !g.creationSeen || isNonTestRuntimeGoroutine(g.creationStack) {
				continue
			}
		}

		blocked := time.Duration(lastTime-g.blockStart) * time.Nanosecond

		var kind Kind
		var conf Confidence

		switch g.reason {
		case "chan send", "chan receive":
			kind = KindGoroutineLeak
			conf = ConfidenceHigh
		default:
			// Only report long blocks if they exceed the threshold
			if blocked < opts.MinBlock {
				continue
			}
			kind = KindLongBlock
			conf = ConfidenceMedium
		}

		findings = append(findings, Finding{
			Kind:        kind,
			Confidence:  conf,
			GoroutineID: gid,
			BlockedOn:   g.reason,
			BlockedFor:  blocked,
			Stack:       g.stack,
			Function:    g.function,
			Location:    g.location,
		})
	}

	return findings
}

// detectOrphans finds goroutines that were created but never ran and never died
// during a very short trace (< 200ms). This targets the "test exits immediately"
// pattern where goroutines are spawned just before the test returns, leaving
// them in limbo. Emits ConfidenceLow findings.
func detectOrphans(goroutines map[trace.GoID]*goroutineState, traceDuration time.Duration) []Finding {
	// Only trigger on very short traces — the "test exits immediately" pattern.
	// On longer traces, goroutines that are alive-but-not-blocked are normal
	// background workers and would produce false positives.
	if traceDuration >= 200*time.Millisecond {
		return nil
	}

	var findings []Finding
	for gid, g := range goroutines {
		if g.goroutineDead {
			continue // normal lifecycle
		}
		if g.isBlocked {
			continue // already caught by other detectors
		}
		if !g.creationSeen {
			continue // pre-test goroutine, not interesting
		}
		if isNonTestRuntimeGoroutine(g.creationStack) {
			continue // non-testing runtime background goroutine
		}
		// Function/location from creation stack (goroutine never blocked, so
		// g.function/g.location from the blocking stack are empty).
		fn := g.creationFunction
		loc := g.creationLocation
		if isRuntimeGoroutine(g.stack) && fn == "" {
			continue
		}

		findings = append(findings, Finding{
			Kind:        KindGoroutineLeak,
			Confidence:  ConfidenceLow,
			GoroutineID: gid,
			BlockedOn:   "never ran (test exited before goroutine was scheduled)",
			BlockedFor:  traceDuration,
			Stack:       g.creationStack,
			Function:    fn,
			Location:    loc,
		})
	}

	return findings
}

// detectTransientBlocks finds goroutines that were blocked on sync primitives
// for a long time during the trace but were eventually unblocked (e.g. by a
// test timeout). These are captured in g.prevLongBlock* when the unblock event
// is processed.
func detectTransientBlocks(goroutines map[trace.GoID]*goroutineState, opts Options) []Finding {
	var findings []Finding

	seen := make(map[string]bool) // deduplicate by location

	for gid, g := range goroutines {
		if g.prevLongBlockDuration < opts.MinBlock {
			continue
		}
		if isRuntimeGoroutine(g.prevLongBlockStack) {
			continue
		}
		if seen[g.prevLongBlockLocation] {
			continue
		}
		seen[g.prevLongBlockLocation] = true

		findings = append(findings, Finding{
			Kind:        KindLongBlock,
			Confidence:  ConfidenceMedium,
			GoroutineID: gid,
			BlockedOn:   g.prevLongBlockReason,
			BlockedFor:  g.prevLongBlockDuration,
			Stack:       g.prevLongBlockStack,
			Function:    g.prevLongBlockFunction,
			Location:    g.prevLongBlockLocation,
		})
	}

	return findings
}
