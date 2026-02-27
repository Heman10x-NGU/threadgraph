package cmd

import (
	"github.com/spf13/cobra"
)

var (
	flagFormat        string
	flagOutput        string
	flagNoLLM         bool
	flagMinBlock      string
	flagDebugFiltered bool
	flagStatic        bool
)

var rootCmd = &cobra.Command{
	Use:   "threadgraph",
	Short: "Detect goroutine leaks, deadlocks, and concurrency bugs in Go programs",
	Long: `ThreadGraph analyzes Go execution traces to find concurrency issues:
  - Goroutine leaks (permanently blocked goroutines)
  - Deadlocks (goroutines stuck on mutex)
  - Long-blocking operations

Run 'threadgraph analyze <trace.out>' or 'threadgraph run ./...' to get started.`,
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagFormat, "format", "terminal", "Output format: terminal or json")
	rootCmd.PersistentFlags().StringVar(&flagOutput, "output", "", "Write output to file instead of stdout")
	rootCmd.PersistentFlags().BoolVar(&flagNoLLM, "no-llm", false, "Skip LLM explanation (faster, works without API key)")
	rootCmd.PersistentFlags().StringVar(&flagMinBlock, "min-block", "1s", "Minimum block duration to flag as a long block (e.g. 500ms, 2s)")
	rootCmd.PersistentFlags().BoolVar(&flagDebugFiltered, "debug-filtered", false, "Print goroutines filtered from findings to stderr (diagnostic)")
	rootCmd.PersistentFlags().BoolVar(&flagStatic, "static", false, "Also run go/ssa static lock-release analysis (requires package source)")
}
