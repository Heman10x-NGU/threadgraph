package reporter

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/Heman10x-NGU/threadgraph/internal/detector"
)

type jsonFinding struct {
	Kind          string `json:"kind"`
	Confidence    string `json:"confidence"`
	GoroutineID   uint64 `json:"goroutine_id"`
	BlockedOn     string `json:"blocked_on"`
	BlockedForMs  int64  `json:"blocked_for_ms"`
	Function      string `json:"function,omitempty"`
	Location      string `json:"location,omitempty"`
	Stack         string `json:"stack,omitempty"`
	Explanation   string `json:"explanation,omitempty"`
}

type jsonReport struct {
	TraceFile          string        `json:"trace_file"`
	DurationMs         int64         `json:"duration_ms"`
	GoroutinesAnalyzed int           `json:"goroutines_analyzed"`
	Findings           []jsonFinding `json:"findings"`
}

// WriteJSON writes findings as JSON to the given writer.
func WriteJSON(w io.Writer, result *detector.Result, explanation string) error {
	report := jsonReport{
		TraceFile:          result.TraceFile,
		DurationMs:         result.DurationMs,
		GoroutinesAnalyzed: result.GoroutinesAnalyzed,
		Findings:           make([]jsonFinding, 0, len(result.Findings)),
	}

	for _, f := range result.Findings {
		jf := jsonFinding{
			Kind:         string(f.Kind),
			Confidence:   string(f.Confidence),
			GoroutineID:  uint64(f.GoroutineID),
			BlockedOn:    f.BlockedOn,
			BlockedForMs: f.BlockedFor.Round(time.Millisecond).Milliseconds(),
			Function:     f.Function,
			Location:     f.Location,
			Stack:        f.Stack,
		}
		if explanation != "" && len(report.Findings) == 0 {
			jf.Explanation = explanation
		}
		report.Findings = append(report.Findings, jf)
	}

	// Attach explanation to the first finding, or as a top-level note
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")

	// If we have an explanation, embed it differently — add as a top-level field
	type jsonReportWithExplanation struct {
		jsonReport
		LLMExplanation string `json:"llm_explanation,omitempty"`
	}
	out := jsonReportWithExplanation{
		jsonReport:     report,
		LLMExplanation: explanation,
	}
	// Reset individual finding explanations (we put it top-level)
	for i := range out.Findings {
		out.Findings[i].Explanation = ""
	}

	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}
