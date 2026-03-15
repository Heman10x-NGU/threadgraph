package reporter

import (
	"fmt"
	"io"
	"strings"

	"github.com/Heman10x-NGU/threadgraph/internal/detector"
	"github.com/fatih/color"
)

var (
	bold      = color.New(color.Bold)
	red       = color.New(color.FgRed, color.Bold)
	yellow    = color.New(color.FgYellow, color.Bold)
	cyan      = color.New(color.FgCyan)
	green     = color.New(color.FgGreen)
	dim       = color.New(color.Faint)
	separator = strings.Repeat("━", 40)
)

// WriteTerminal writes a human-readable colored report to w.
func WriteTerminal(w io.Writer, result *detector.Result, explanation string) {
	leaks := countKind(result.Findings, detector.KindGoroutineLeak)
	deadlocks := countKind(result.Findings, detector.KindDeadlock)
	longBlocks := countKind(result.Findings, detector.KindLongBlock)
	lockLeaks := countKind(result.Findings, detector.KindLockLeak)
	lockOrders := countKind(result.Findings, detector.KindLockOrder)
	races := countKind(result.Findings, detector.KindDataRace)

	bold.Fprintln(w, "\nThreadGraph Analysis")
	fmt.Fprintln(w, separator)
	fmt.Fprintln(w)

	// Summary
	leakStr := pluralize(leaks, "goroutine leak")
	deadStr := pluralize(deadlocks, "deadlock")
	blockStr := pluralize(longBlocks, "long block")
	lockStr := pluralize(lockLeaks, "lock leak (static)")

	if leaks > 0 {
		red.Fprintf(w, "  %s\n", leakStr)
	} else {
		green.Fprintf(w, "  %s\n", leakStr)
	}
	if deadlocks > 0 {
		red.Fprintf(w, "  %s\n", deadStr)
	} else {
		green.Fprintf(w, "  %s\n", deadStr)
	}
	if longBlocks > 0 {
		yellow.Fprintf(w, "  %s\n", blockStr)
	} else {
		green.Fprintf(w, "  %s\n", blockStr)
	}
	if lockLeaks > 0 {
		yellow.Fprintf(w, "  %s\n", lockStr)
	}
	if lockOrders > 0 {
		lockOrderStr := pluralizeWith(lockOrders, "lock ordering cycle (static)", "lock ordering cycles (static)")
		red.Fprintf(w, "  %s\n", lockOrderStr)
	}
	if races > 0 {
		raceStr := pluralizeWith(races, "data race", "data races")
		red.Fprintf(w, "  %s\n", raceStr)
	}

	if len(result.Findings) == 0 {
		fmt.Fprintln(w)
		green.Fprintln(w, "  No concurrency issues detected.")
	}

	// Individual findings
	for _, f := range result.Findings {
		fmt.Fprintln(w)
		printFinding(w, f)
	}

	// LLM explanation
	if explanation != "" {
		fmt.Fprintln(w)
		bold.Fprintln(w, "  Claude's Analysis")
		fmt.Fprintln(w)
		for _, line := range strings.Split(strings.TrimSpace(explanation), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}

	// Footer
	fmt.Fprintln(w)
	fmt.Fprintln(w, separator)
	dim.Fprintf(w, "  Analyzed %d goroutines · %dms window · %s\n",
		result.GoroutinesAnalyzed, result.DurationMs, result.TraceFile)
	fmt.Fprintln(w)
}

func printFinding(w io.Writer, f detector.Finding) {
	// Header line
	switch f.Kind {
	case detector.KindGoroutineLeak:
		red.Fprintf(w, "● GOROUTINE LEAK")
	case detector.KindDeadlock:
		red.Fprintf(w, "● DEADLOCK")
	case detector.KindLongBlock:
		yellow.Fprintf(w, "● LONG BLOCK")
	case detector.KindLockLeak:
		yellow.Fprintf(w, "● LOCK LEAK (static)")
	case detector.KindLockOrder:
		red.Fprintf(w, "● LOCK ORDER CYCLE (static)")
	case detector.KindDataRace:
		red.Fprintf(w, "● DATA RACE")
	}
	dim.Fprintf(w, "  (%s confidence)\n", f.Confidence)

	// Details — skip misleading "Goroutine 0" for static/race findings
	if f.GoroutineID != 0 {
		fmt.Fprintf(w, "  Goroutine %d blocked on: ", f.GoroutineID)
		cyan.Fprintf(w, "%s\n", f.BlockedOn)
	} else {
		fmt.Fprintf(w, "  Issue: ")
		cyan.Fprintf(w, "%s\n", f.BlockedOn)
	}

	if f.BlockedFor > 0 {
		fmt.Fprintf(w, "  Blocked for: ")
		cyan.Fprintf(w, "%v\n", f.BlockedFor.Round(1000000)) // round to ms
	}

	if f.Location != "" {
		fmt.Fprintf(w, "  Location: ")
		cyan.Fprintf(w, "%s\n", f.Location)
	}

	if f.Count > 1 {
		dim.Fprintf(w, "  × %d goroutines affected\n", f.Count)
	}

	if f.Stack != "" {
		fmt.Fprintln(w, "  Stack:")
		for _, line := range strings.Split(strings.TrimRight(f.Stack, "\n"), "\n") {
			dim.Fprintf(w, "  %s\n", line)
		}
	}
}

func countKind(findings []detector.Finding, kind detector.Kind) int {
	n := 0
	for _, f := range findings {
		if f.Kind == kind {
			if f.Count > 1 {
				n += f.Count
			} else {
				n++
			}
		}
	}
	return n
}

func pluralize(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func pluralizeWith(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}
