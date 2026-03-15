// Package baseline provides save/load/compare functionality for ThreadGraph
// findings baselines. A baseline is a snapshot of known findings; subsequent
// runs can be compared against it to surface only NEW findings, which is
// useful for CI regression detection.
package baseline

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/Heman10x-NGU/threadgraph/internal/detector"
)

const currentVersion = 1

// Entry is a single finding captured in a baseline, keyed by kind and location.
// Goroutine IDs and blocked durations are intentionally omitted because they
// change between runs.
type Entry struct {
	Kind     string `json:"kind"`
	Function string `json:"function"`
	Location string `json:"location"`
}

// Baseline is the on-disk format for a saved set of findings.
type Baseline struct {
	Version int     `json:"version"`
	Created string  `json:"created"`
	Entries []Entry `json:"entries"`
}

// Save writes findings to a baseline file at path.
// Duplicate (kind, location) pairs are deduplicated before saving.
func Save(findings []detector.Finding, path string) error {
	b := &Baseline{
		Version: currentVersion,
		Created: time.Now().UTC().Format(time.RFC3339),
	}
	seen := make(map[[2]string]bool)
	for _, f := range findings {
		key := [2]string{string(f.Kind), f.Location}
		if seen[key] {
			continue
		}
		seen[key] = true
		b.Entries = append(b.Entries, Entry{
			Kind:     string(f.Kind),
			Function: f.Function,
			Location: f.Location,
		})
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Load reads a baseline file from path.
func Load(path string) (*Baseline, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b Baseline
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, err
	}
	if b.Version != currentVersion {
		return nil, fmt.Errorf("unsupported baseline version %d (expected %d)", b.Version, currentVersion)
	}
	return &b, nil
}

// FilterNew returns only the findings that are not present in b.
// Matching is by (kind, location) — goroutine IDs and timings are ignored.
func FilterNew(findings []detector.Finding, b *Baseline) []detector.Finding {
	known := make(map[[2]string]bool, len(b.Entries))
	for _, e := range b.Entries {
		known[[2]string{e.Kind, e.Location}] = true
	}
	var out []detector.Finding
	for _, f := range findings {
		if !known[[2]string{string(f.Kind), f.Location}] {
			out = append(out, f)
		}
	}
	return out
}
