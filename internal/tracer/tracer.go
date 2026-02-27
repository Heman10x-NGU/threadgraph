package tracer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// RunResult holds the output of a traced test run.
type RunResult struct {
	TraceFile string
	Output    string
	ExitCode  int
}

// Run executes `go test -trace <tmpfile> -timeout <duration> <args...>` and
// returns the path to the generated trace file.
//
// If args contain a wildcard pattern like ./..., we enumerate packages first
// (go list) and run each package separately, merging their traces into one.
//
// extraEnv is a list of additional environment variable assignments (e.g.
// "GOMAXPROCS=1") prepended to the process environment.
func Run(args []string, duration time.Duration, extraEnv ...string) (*RunResult, error) {
	pkgs, err := expandPackages(args)
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}
	if len(pkgs) == 1 {
		return runSingle(pkgs[0:], duration, extraEnv)
	}
	return runMulti(pkgs, duration, extraEnv)
}

// runSingle runs go test -trace on a single package.
func runSingle(args []string, duration time.Duration, extraEnv []string) (*RunResult, error) {
	traceFile, err := tempTraceFile()
	if err != nil {
		return nil, fmt.Errorf("create trace file: %w", err)
	}

	timeout := fmt.Sprintf("%.0fs", duration.Seconds())
	cmdArgs := []string{"test", "-trace", traceFile, "-timeout", timeout}
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command("go", cmdArgs...)
	cmd.Env = append(os.Environ(), extraEnv...)

	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			os.Remove(traceFile)
			return nil, fmt.Errorf("go test: %w", err)
		}
	}

	if _, err := os.Stat(traceFile); os.IsNotExist(err) {
		return nil, fmt.Errorf("trace file not created — did `go test` run? output:\n%s", strings.TrimSpace(string(out)))
	}

	return &RunResult{
		TraceFile: traceFile,
		Output:    string(out),
		ExitCode:  exitCode,
	}, nil
}

// runMulti runs each package separately, picks the trace with the most events
// (the most interesting one). For multi-package runs, the user sees per-package
// output, and we report on the combined results printed to stderr.
func runMulti(pkgs []string, duration time.Duration, extraEnv []string) (*RunResult, error) {
	var allOutput strings.Builder
	var bestTrace string
	var bestSize int64
	var worstExit int
	var traceFiles []string

	for _, pkg := range pkgs {
		r, err := runSingle([]string{pkg}, duration, extraEnv)
		if err != nil {
			// Package may have no test files — skip silently
			continue
		}
		traceFiles = append(traceFiles, r.TraceFile)
		allOutput.WriteString(r.Output)
		if r.ExitCode > worstExit {
			worstExit = r.ExitCode
		}
		// Pick the largest trace file (most events = most interesting)
		if fi, err := os.Stat(r.TraceFile); err == nil {
			if fi.Size() > bestSize {
				bestSize = fi.Size()
				bestTrace = r.TraceFile
			}
		}
	}

	// Clean up all traces except the best one
	for _, f := range traceFiles {
		if f != bestTrace {
			os.Remove(f)
		}
	}

	if bestTrace == "" {
		return nil, fmt.Errorf("no packages produced a trace (no test files?)")
	}

	return &RunResult{
		TraceFile: bestTrace,
		Output:    allOutput.String(),
		ExitCode:  worstExit,
	}, nil
}

// expandPackages runs `go list <args>` to resolve package patterns to a list
// of import paths. If args have no wildcards, returns args unchanged.
func expandPackages(args []string) ([]string, error) {
	hasWildcard := false
	for _, a := range args {
		if strings.Contains(a, "...") {
			hasWildcard = true
			break
		}
	}
	if !hasWildcard {
		return args, nil
	}

	cmdArgs := append([]string{"list"}, args...)
	out, err := exec.Command("go", cmdArgs...).Output()
	if err != nil {
		return nil, fmt.Errorf("go list: %w", err)
	}

	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			pkgs = append(pkgs, line)
		}
	}
	return pkgs, nil
}

func tempTraceFile() (string, error) {
	dir := os.TempDir()
	f, err := os.CreateTemp(dir, "threadgraph-*.out")
	if err != nil {
		return "", err
	}
	name := f.Name()
	f.Close()
	return filepath.Clean(name), nil
}
