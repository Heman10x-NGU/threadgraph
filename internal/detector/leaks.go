package detector

import (
	"time"

	"golang.org/x/exp/trace"
)

// detectLeaks finds goroutines still blocked at the end of the trace.
// Goroutines blocked on "chan send" or "chan receive" are classified as leaks.
// Others blocked longer than minBlock are classified as long blocks.
//
// traceDuration is the full trace window used to compute the lifetime ratio
// (blockDuration / traceDuration). A ratio near 1.0 means the goroutine was
// blocked for nearly the entire trace — higher confidence of a real leak.
func detectLeaks(goroutines map[trace.GoID]*goroutineState, lastTime trace.Time, traceDuration time.Duration, opts Options) []Finding {
	var findings []Finding

	for gid, g := range goroutines {
		if !g.isBlocked {
			continue
		}
		// Only report goroutines that are part of the test's goroutine tree.
		// This eliminates pre-test global workers and unrelated infrastructure.
		if !g.isTestOwned {
			continue
		}
		// Filter by blocking stack: pure runtime goroutines are never findings.
		if isRuntimeGoroutine(g.stack) {
			continue
		}
		// Skip the main goroutine sleeping intentionally (e.g. time.Sleep in test).
		if g.reason == "sleep" {
			continue
		}

		blocked := time.Duration(lastTime-g.blockStart) * time.Nanosecond

		// Lifetime ratio: fraction of the trace window this goroutine has been
		// continuously blocked. ratio → 1.0 = blocked since trace start (definite
		// leak); ratio → 0 = just started blocking (might be transient).
		blockRatio := 1.0
		if traceDuration > 0 {
			blockRatio = float64(blocked) / float64(traceDuration)
		}

		var kind Kind
		var conf Confidence

		switch g.reason {
		case "chan send", "chan receive":
			kind = KindGoroutineLeak
			// Confidence scaled by lifetime ratio.
			switch {
			case blockRatio >= 0.85:
				conf = ConfidenceHigh
			case blockRatio >= 0.40:
				conf = ConfidenceMedium
			default:
				conf = ConfidenceLow
			}
		default:
			// Only report long blocks if they exceed the threshold
			if blocked < opts.MinBlock {
				continue
			}
			kind = KindLongBlock
			switch {
			case blockRatio >= 0.85:
				conf = ConfidenceMedium // long blocks are at most Medium
			default:
				conf = ConfidenceLow
			}
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
		if !g.isTestOwned {
			continue // pre-test or unrelated goroutine
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
