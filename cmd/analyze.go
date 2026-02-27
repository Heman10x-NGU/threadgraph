package cmd

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/Heman10x-NGU/threadgraph/internal/detector"
	"github.com/Heman10x-NGU/threadgraph/internal/llm"
	"github.com/Heman10x-NGU/threadgraph/internal/reporter"
)

var analyzeCmd = &cobra.Command{
	Use:   "analyze <trace.out>",
	Short: "Analyze an existing Go execution trace file",
	Example: `  threadgraph analyze ./trace.out
  threadgraph analyze ./trace.out --format json --output findings.json
  threadgraph analyze ./trace.out --no-llm
  threadgraph analyze ./trace.out --debug-filtered`,
	Args: cobra.ExactArgs(1),
	RunE: runAnalyze,
}

func init() {
	rootCmd.AddCommand(analyzeCmd)
}

func runAnalyze(cmd *cobra.Command, args []string) error {
	tracePath := args[0]

	minBlock, err := time.ParseDuration(flagMinBlock)
	if err != nil {
		return fmt.Errorf("--min-block: %w", err)
	}

	opts := detector.Options{
		MinBlock:      minBlock,
		DebugFiltered: flagDebugFiltered,
	}

	result, err := detector.Analyze(tracePath, opts)
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
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

// outputWriter returns a writer for the output destination (file or stdout).
func outputWriter() (io.Writer, func(), error) {
	if flagOutput == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(flagOutput)
	if err != nil {
		return nil, nil, fmt.Errorf("create output file: %w", err)
	}
	return f, func() { f.Close() }, nil
}
