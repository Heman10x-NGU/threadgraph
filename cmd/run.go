package cmd

import (
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"github.com/Heman10x-NGU/threadgraph/internal/detector"
	"github.com/Heman10x-NGU/threadgraph/internal/llm"
	"github.com/Heman10x-NGU/threadgraph/internal/reporter"
	"github.com/Heman10x-NGU/threadgraph/internal/static"
	"github.com/Heman10x-NGU/threadgraph/internal/tracer"
)

var (
	flagDuration string
)

var runCmd = &cobra.Command{
	Use:   "run [go test args...]",
	Short: "Auto-instrument a Go package, capture trace, and analyze",
	Long: `Run executes 'go test -trace <tmpfile> -timeout <duration> <args...>',
then analyzes the captured trace for concurrency issues.`,
	Example: `  threadgraph run ./...
  threadgraph run ./... --duration 30s
  threadgraph run ./pkg/server/... --duration 60s --no-llm
  threadgraph run ./... --static`,
	Args: cobra.MinimumNArgs(1),
	RunE: runRun,
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().StringVar(&flagDuration, "duration", "10s", "Test timeout / trace duration (e.g. 10s, 30s, 60s)")
}

func runRun(cmd *cobra.Command, args []string) error {
	duration, err := time.ParseDuration(flagDuration)
	if err != nil {
		return fmt.Errorf("--duration: %w", err)
	}

	minBlock, err := time.ParseDuration(flagMinBlock)
	if err != nil {
		return fmt.Errorf("--min-block: %w", err)
	}

	opts := detector.Options{
		MinBlock:      minBlock,
		DebugFiltered: flagDebugFiltered,
	}

	fmt.Fprintf(os.Stderr, "Running: go test -trace <tmpfile> -timeout %s %s\n", flagDuration, joinArgs(args))

	runResult, err := tracer.Run(args, duration)
	if err != nil {
		return fmt.Errorf("trace: %w", err)
	}
	traceToClean := runResult.TraceFile

	if runResult.Output != "" {
		fmt.Fprintln(os.Stderr, "--- go test output ---")
		fmt.Fprint(os.Stderr, runResult.Output)
		fmt.Fprintln(os.Stderr, "--- end output ---")
	}

	result, err := detector.Analyze(runResult.TraceFile, opts)
	if err != nil {
		os.Remove(traceToClean)
		return fmt.Errorf("analyze: %w", err)
	}

	// Schedule diversity retry loop.
	// If no findings on the first pass, try increasingly constrained GOMAXPROCS values
	// to expose scheduling-dependent bugs. Order: GOMAXPROCS=1, GOMAXPROCS=2, GOMAXPROCS=4.
	// Each retry serializes goroutine scheduling differently, catching different interleavings.
	gomaxprocsRetries := scheduleDiversityValues()
	for _, gmp := range gomaxprocsRetries {
		if len(result.Findings) > 0 {
			break
		}
		env := fmt.Sprintf("GOMAXPROCS=%d", gmp)
		fmt.Fprintf(os.Stderr, "No findings; retrying with %s...\n", env)
		r2, err2 := tracer.Run(args, duration, env)
		if err2 != nil {
			continue
		}
		if runResult.Output != "" {
			fmt.Fprintln(os.Stderr, "--- go test output ---")
			fmt.Fprint(os.Stderr, r2.Output)
			fmt.Fprintln(os.Stderr, "--- end output ---")
		}
		res2, err2 := detector.Analyze(r2.TraceFile, opts)
		if err2 == nil && len(res2.Findings) > 0 {
			os.Remove(traceToClean)
			traceToClean = r2.TraceFile
			result = res2
		} else {
			os.Remove(r2.TraceFile)
		}
	}
	defer os.Remove(traceToClean)

	// Optional: go/ssa static lock-release analysis.
	if flagStatic {
		fmt.Fprintln(os.Stderr, "Running static lock-release analysis...")
		staticFindings, serr := static.AnalyzeLockRelease(args)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "warn: static analysis: %v\n", serr)
		} else {
			for _, sf := range staticFindings {
				result.Findings = append(result.Findings, detector.Finding{
					Kind:        detector.KindLockLeak,
					Confidence:  detector.ConfidenceLow,
					GoroutineID: 0,
					BlockedOn:   sf.Message,
					Function:    sf.Function,
					Location:    sf.Location,
				})
			}
		}
	}

	explanation := ""
	if !flagNoLLM {
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey != "" && len(result.Findings) > 0 {
			exp, err := llm.Explain(result.Findings, apiKey)
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: LLM explanation failed: %v\n", err)
			} else {
				explanation = exp
			}
		}
	}

	out, cleanup, err := outputWriter()
	if err != nil {
		return err
	}
	defer cleanup()

	switch flagFormat {
	case "json":
		return reporter.WriteJSON(out, result, explanation)
	default:
		reporter.WriteTerminal(out, result, explanation)
		return nil
	}
}

// scheduleDiversityValues returns the GOMAXPROCS values to retry with when no
// findings are found on the first pass. We try 1 (fully serialized), 2 (light
// concurrency), and 4 (moderate concurrency) to expose different scheduling
// interleavings. Values larger than runtime.NumCPU() are skipped.
func scheduleDiversityValues() []int {
	numCPU := runtime.NumCPU()
	candidates := []int{1, 2, 4}
	var result []int
	for _, v := range candidates {
		if v <= numCPU || v == 1 {
			result = append(result, v)
		}
	}
	return result
}

func joinArgs(args []string) string {
	result := ""
	for i, a := range args {
		if i > 0 {
			result += " "
		}
		result += a
	}
	return result
}
