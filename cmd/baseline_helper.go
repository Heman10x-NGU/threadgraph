package cmd

import (
	"fmt"
	"os"

	"github.com/Heman10x-NGU/threadgraph/internal/baseline"
	"github.com/Heman10x-NGU/threadgraph/internal/detector"
)

// applyBaseline handles --save-baseline and --baseline logic.
//
// If --save-baseline is set, it writes the current findings to the given file.
// If --baseline is set, it loads the file, filters out known findings, updates
// result.Findings to only the new ones, and returns a non-nil error if any new
// findings were found (so the process exits 1 in CI).
//
// The two flags can be combined: save first, then compare (no new findings).
func applyBaseline(result *detector.Result) error {
	if flagSaveBaseline != "" {
		if err := baseline.Save(result.Findings, flagSaveBaseline); err != nil {
			fmt.Fprintf(os.Stderr, "warn: save baseline: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "Baseline saved to %s (%d finding(s))\n", flagSaveBaseline, len(result.Findings))
		}
	}

	if flagBaseline != "" {
		b, err := baseline.Load(flagBaseline)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: load baseline: %v\n", err)
		} else {
			newFindings := baseline.FilterNew(result.Findings, b)
			known := len(result.Findings) - len(newFindings)
			if known > 0 {
				fmt.Fprintf(os.Stderr, "Baseline: %d known finding(s) suppressed, %d new\n", known, len(newFindings))
			}
			result.Findings = newFindings
			if len(newFindings) > 0 {
				// Defer the exit-1 signal: output is written before the caller returns this error.
				return fmt.Errorf("%d new finding(s) detected (not in baseline %s)", len(newFindings), flagBaseline)
			}
		}
	}

	return nil
}
