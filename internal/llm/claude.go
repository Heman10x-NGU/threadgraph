package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Heman10x-NGU/threadgraph/internal/detector"
)

const apiURL = "https://api.anthropic.com/v1/messages"

// Explain sends findings to Claude and returns a plain-English explanation.
func Explain(findings []detector.Finding, apiKey string) (string, error) {
	prompt := buildPrompt(findings)

	body, err := json.Marshal(map[string]any{
		"model":      "claude-sonnet-4-6",
		"max_tokens": 1024,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var errBody map[string]any
		json.NewDecoder(resp.Body).Decode(&errBody)
		return "", fmt.Errorf("API returned %d: %v", resp.StatusCode, errBody)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	if len(result.Content) > 0 {
		return result.Content[0].Text, nil
	}
	return "", fmt.Errorf("empty response from Claude")
}

func buildPrompt(findings []detector.Finding) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("I analyzed a Go execution trace and found %d concurrency issue(s).\n\n", len(findings)))

	for i, f := range findings {
		sb.WriteString(fmt.Sprintf("Issue %d: %s (confidence: %s)\n", i+1, f.Kind, f.Confidence))
		if f.Count > 1 {
			sb.WriteString(fmt.Sprintf("  Affects %d goroutines simultaneously\n", f.Count))
		}
		sb.WriteString(fmt.Sprintf("  Goroutine %d is blocked on: %q\n", f.GoroutineID, f.BlockedOn))
		if f.BlockedFor > 0 {
			sb.WriteString(fmt.Sprintf("  Blocked for: %v\n", f.BlockedFor.Round(time.Millisecond)))
		}
		if f.Location != "" {
			sb.WriteString(fmt.Sprintf("  Location: %s\n", f.Location))
		}
		if f.Function != "" {
			sb.WriteString(fmt.Sprintf("  Function: %s\n", f.Function))
		}
		if f.Stack != "" {
			sb.WriteString(fmt.Sprintf("  Stack trace:\n%s", f.Stack))
		}
		// Attach source code context if readable (20-line window centred on the bug line).
		if ctx := readCodeContext(f.Location, 10); ctx != "" {
			sb.WriteString(fmt.Sprintf("  Source code context:\n%s\n", ctx))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("For each issue:\n")
	sb.WriteString("1. Explain the root cause in plain English (1-2 sentences)\n")
	sb.WriteString("2. Give a specific code fix a Go developer should apply\n")
	sb.WriteString("3. Show a before/after code diff if possible\n")
	sb.WriteString("4. Keep explanations concise and actionable\n")

	return sb.String()
}

// readCodeContext reads a ±radius line window around the line indicated by a
// "file:line" location string. Returns an empty string if the file cannot be
// read or the location is not parseable (e.g. runtime internals).
func readCodeContext(location string, radius int) string {
	if location == "" {
		return ""
	}
	// Split "file:line" — last colon separates the line number.
	lastColon := strings.LastIndex(location, ":")
	if lastColon < 0 {
		return ""
	}
	filePath := location[:lastColon]
	lineNo, err := strconv.Atoi(location[lastColon+1:])
	if err != nil || lineNo <= 0 {
		return ""
	}

	f, err := os.Open(filePath)
	if err != nil {
		return "" // file not accessible (e.g. stdlib, vendor)
	}
	defer f.Close()

	start := lineNo - radius
	if start < 1 {
		start = 1
	}
	end := lineNo + radius

	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	current := 1
	for scanner.Scan() {
		if current > end {
			break
		}
		if current >= start {
			marker := "  "
			if current == lineNo {
				marker = "→ "
			}
			sb.WriteString(fmt.Sprintf("%s%4d: %s\n", marker, current, scanner.Text()))
		}
		current++
	}
	return sb.String()
}
