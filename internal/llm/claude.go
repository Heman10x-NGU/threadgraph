package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
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
		sb.WriteString("\n")
	}

	sb.WriteString("For each issue:\n")
	sb.WriteString("1. Explain the root cause in plain English (1-2 sentences)\n")
	sb.WriteString("2. Give a specific code fix a Go developer should apply\n")
	sb.WriteString("3. Show a before/after code diff if possible\n")
	sb.WriteString("4. Keep explanations concise and actionable\n")

	return sb.String()
}
