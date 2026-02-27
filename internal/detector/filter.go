package detector

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/exp/trace"
)

// extractStack formats a trace.Stack into a human-readable string,
// and returns the top user-code function name and file:line location.
func extractStack(s trace.Stack) (stack, function, location string) {
	var sb bytes.Buffer
	for f := range s.Frames() {
		fmt.Fprintf(&sb, "      %s (%s:%d)\n", f.Func, f.File, f.Line)
		// Pick the first non-runtime frame as the "location"
		if location == "" && !isRuntimeFrame(f.Func, f.File) {
			function = f.Func
			location = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
	}
	return sb.String(), function, location
}

// isRuntimeGoroutine returns true if ALL stack frames are Go runtime internals
// (including the testing framework).
func isRuntimeGoroutine(stack string) bool {
	if stack == "" {
		return true
	}
	for _, line := range strings.Split(stack, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Extract function/file portion (after the leading spaces)
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 1 {
			continue
		}
		funcName := parts[0]
		if !isRuntimeFrame(funcName, line) {
			return false
		}
	}
	return true
}

// isNonTestRuntimeGoroutine returns true if the stack is entirely runtime
// frames AND those frames are NOT from the testing framework.
//
// This distinction matters for goroutines created by testing.T.Run: those
// goroutines run user test code and should be treated as test-owned even
// though their creation stack is "runtime-only". By contrast, goroutines
// created by net/http or other library code (which have no testing frames)
// are genuine background workers and should be filtered.
func isNonTestRuntimeGoroutine(stack string) bool {
	if !isRuntimeGoroutine(stack) {
		return false // has user code — definitely not a non-test runtime goroutine
	}
	// It's a runtime-only stack. Check if it involves the testing framework.
	// If it does (e.g., created by testing.T.Run), it's test-owned and should
	// NOT be classified as a "non-test runtime goroutine".
	return !strings.Contains(stack, "testing.")
}

func isRuntimeFrame(funcName, line string) bool {
	return strings.HasPrefix(funcName, "runtime.") ||
		strings.Contains(line, "runtime/") ||
		strings.HasPrefix(funcName, "runtime2.") ||
		strings.Contains(line, "/runtime/trace") ||
		// Filter Go test framework internals
		strings.HasPrefix(funcName, "testing.") ||
		strings.Contains(line, "_testmain.go")
}
