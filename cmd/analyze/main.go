package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/exp/trace"
)

type Finding struct {
	Kind        string
	GoroutineID trace.GoID
	Reason      string
	Stack       string
	BlockedFor  time.Duration
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: analyze <trace.out>")
		os.Exit(1)
	}

	findings := analyzeTrace(os.Args[1])

	fmt.Printf("\n=== ThreadGraph Analysis ===\n")
	if len(findings) == 0 {
		fmt.Println("No issues detected in this trace window.")
		return
	}

	fmt.Printf("Confirmed findings: %d\n\n", len(findings))
	for i, f := range findings {
		fmt.Printf("[%d] %s\n", i+1, strings.ToUpper(f.Kind))
		fmt.Printf("    Goroutine: %d\n", f.GoroutineID)
		fmt.Printf("    Blocked on: %q\n", f.Reason)
		if f.BlockedFor > 0 {
			fmt.Printf("    Blocked for: %v\n", f.BlockedFor.Round(time.Millisecond))
		}
		if f.Stack != "" {
			fmt.Printf("    Stack:\n%s", f.Stack)
		}
		fmt.Println()
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		fmt.Println("(Set ANTHROPIC_API_KEY to get LLM explanation)")
		return
	}

	fmt.Println("=== LLM Explanation (Claude) ===")
	explanation := explainWithClaude(findings, apiKey)
	fmt.Println(explanation)
}

func analyzeTrace(path string) []Finding {
	f, err := os.Open(path)
	if err != nil {
		log.Fatal("open trace:", err)
	}
	defer f.Close()

	r, err := trace.NewReader(f)
	if err != nil {
		log.Fatal("parse trace:", err)
	}

	type goroutineState struct {
		reason     string
		stack      string
		blockStart trace.Time
		isBlocked  bool
	}

	goroutines := make(map[trace.GoID]*goroutineState)
	var lastTime trace.Time

	for {
		ev, err := r.ReadEvent()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal("read event:", err)
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

		// Goroutine just blocked
		if from.Executing() && to == trace.GoWaiting {
			g.isBlocked = true
			g.reason = st.Reason
			g.blockStart = ev.Time()

			var sb bytes.Buffer
			for f := range st.Stack.Frames() {
				fmt.Fprintf(&sb, "      %s (%s:%d)\n", f.Func, f.File, f.Line)
			}
			g.stack = sb.String()
		}

		// Goroutine unblocked — no longer a leak candidate
		if from == trace.GoWaiting && to.Executing() {
			g.isBlocked = false
			g.reason = ""
			g.stack = ""
		}
	}

	// Any goroutine still blocked at end of trace = potential leak
	var findings []Finding
	for gid, g := range goroutines {
		if !g.isBlocked {
			continue
		}

		// Skip Go runtime internal goroutines (trace, GC, scheduler)
		if isRuntimeGoroutine(g.stack) {
			continue
		}

		// Skip the main goroutine sleeping intentionally
		if g.reason == "sleep" {
			continue
		}

		kind := "long_block"
		if g.reason == "chan send" || g.reason == "chan receive" {
			kind = "goroutine_leak"
		}

		blocked := time.Duration(lastTime-g.blockStart) * time.Nanosecond

		findings = append(findings, Finding{
			Kind:        kind,
			GoroutineID: gid,
			Reason:      g.reason,
			Stack:       g.stack,
			BlockedFor:  blocked,
		})
	}

	return findings
}

// isRuntimeGoroutine returns true if ALL stack frames are in Go's runtime packages.
// These are internal goroutines (GC, trace, scheduler) — not user code leaks.
func isRuntimeGoroutine(stack string) bool {
	if stack == "" {
		return true
	}
	for _, line := range strings.Split(stack, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// If any frame is in user code (not runtime/internal packages), it's not a runtime goroutine
		if !strings.Contains(line, "runtime.") &&
			!strings.Contains(line, "runtime/") &&
			!strings.Contains(line, "runtime2.") &&
			!strings.Contains(line, "/runtime/trace") {
			return false
		}
	}
	return true
}

func explainWithClaude(findings []Finding, apiKey string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("I analyzed a Go execution trace and found %d concurrency issues.\n\n", len(findings)))

	for i, f := range findings {
		sb.WriteString(fmt.Sprintf("Issue %d: %s\n", i+1, f.Kind))
		sb.WriteString(fmt.Sprintf("  Goroutine %d is blocked on: %q\n", f.GoroutineID, f.Reason))
		if f.BlockedFor > 0 {
			sb.WriteString(fmt.Sprintf("  Blocked for: %v\n", f.BlockedFor.Round(time.Millisecond)))
		}
		if f.Stack != "" {
			sb.WriteString(fmt.Sprintf("  Stack trace:\n%s", f.Stack))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("For each issue:\n")
	sb.WriteString("1. Explain the root cause in plain English (1-2 sentences)\n")
	sb.WriteString("2. Give a specific code fix a Go developer should apply\n")
	sb.WriteString("3. Keep the explanation concise and actionable\n")

	body, _ := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": sb.String()},
		},
	})

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "LLM call failed: " + err.Error()
	}
	defer resp.Body.Close()

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "decode error: " + err.Error()
	}
	if len(result.Content) > 0 {
		return result.Content[0].Text
	}
	return "no response from Claude"
}
