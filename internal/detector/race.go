package detector

import (
	"fmt"
	"strings"

	"golang.org/x/exp/trace"
)

// ParseRaceOutput parses the text output of `go test -race` and converts
// each "WARNING: DATA RACE" block into a Finding with Kind=KindDataRace.
//
// The Go race detector emits sections in this format:
//
//	==================
//	WARNING: DATA RACE
//	Read at 0x00c... by goroutine N:
//	  pkg.Func()
//	      /path/file.go:L +0x...
//	Previous write at 0x00c... by goroutine M:
//	  pkg.OtherFunc()
//	      /path/file.go:L +0x...
//	==================
//
// Each unique (location, operation) pair yields one finding. Duplicates are
// deduplicated by the "file:line|blockedOn" key.
func ParseRaceOutput(output string) []Finding {
	var findings []Finding
	seen := make(map[string]bool)

	const marker = "WARNING: DATA RACE"
	idx := 0
	for {
		pos := strings.Index(output[idx:], marker)
		if pos < 0 {
			break
		}
		pos += idx

		// Locate the closing "==================" delimiter.
		closeIdx := strings.Index(output[pos:], "\n==================")
		var section string
		if closeIdx < 0 {
			section = output[pos:]
			idx = len(output)
		} else {
			section = output[pos : pos+closeIdx]
			idx = pos + closeIdx + 1
		}

		f := parseRaceSection(section)
		if f == nil {
			continue
		}
		key := f.Location + "|" + f.BlockedOn
		if seen[key] {
			continue
		}
		seen[key] = true
		findings = append(findings, *f)
	}
	return findings
}

// opBlock holds the parsed state for one read/write operation within a race report.
type opBlock struct {
	operation string // "read" or "write"
	function  string // first non-runtime function name
	location  string // first non-runtime file:line
	stack     strings.Builder
}

// parseRaceSection extracts a Finding from a single "WARNING: DATA RACE" section.
func parseRaceSection(section string) *Finding {
	var blocks []*opBlock
	var cur *opBlock

	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "===") || trimmed == "WARNING: DATA RACE" {
			continue
		}

		lwr := strings.ToLower(trimmed)

		// New operation block: "Read at ..." / "Write at ..." / "Previous read/write at ..."
		if isOpLine(lwr) {
			if cur != nil {
				blocks = append(blocks, cur)
			}
			op := "read"
			if strings.Contains(lwr, "write") {
				op = "write"
			}
			cur = &opBlock{operation: op}
			continue
		}

		// "Goroutine N (state) created at:" ends the current op block.
		if strings.HasPrefix(trimmed, "Goroutine ") && strings.Contains(trimmed, " created at:") {
			if cur != nil {
				blocks = append(blocks, cur)
				cur = nil
			}
			continue
		}

		if cur == nil {
			continue
		}

		cur.stack.WriteString(trimmed)
		cur.stack.WriteByte('\n')

		// File:line lines contain ".go:" and "+0x".
		if strings.Contains(trimmed, ".go:") && strings.Contains(trimmed, "+0x") {
			if cur.location == "" {
				loc := strings.Fields(trimmed)[0] // "/path/file.go:L"
				if !isRaceRuntimePath(loc) {
					cur.location = loc
				}
			}
		} else if cur.function == "" && strings.Contains(trimmed, ".") && !strings.Contains(trimmed, "+0x") {
			// Function name line: "pkg.Func(...)" — no "+0x"
			fn := trimmed
			if i := strings.Index(fn, "("); i > 0 {
				fn = fn[:i]
			}
			if !isRaceRuntimeFunc(fn) {
				cur.function = fn
			}
		}
	}
	if cur != nil {
		blocks = append(blocks, cur)
	}

	if len(blocks) == 0 {
		return nil
	}

	// Pick the first block with a location (current op preferred over previous).
	var chosen *opBlock
	for _, b := range blocks {
		if b.location != "" {
			chosen = b
			break
		}
	}
	if chosen == nil {
		chosen = blocks[0]
	}

	// Determine the conflict description.
	blockedOn := fmt.Sprintf("data race: concurrent %s", chosen.operation)
	if len(blocks) >= 2 {
		other := blocks[1].operation
		if other != chosen.operation {
			blockedOn = fmt.Sprintf("data race: %s/%s conflict", chosen.operation, other)
		}
	}

	// Combine stacks for all op blocks (skip Goroutine-created-at blocks
	// which we didn't capture above).
	var stackParts []string
	for i, b := range blocks {
		s := strings.TrimRight(b.stack.String(), "\n")
		if s == "" {
			continue
		}
		if i == 0 {
			stackParts = append(stackParts, s)
		} else {
			stackParts = append(stackParts, "--- (conflicting access) ---\n"+s)
		}
	}
	stack := strings.Join(stackParts, "\n")

	return &Finding{
		Kind:        KindDataRace,
		Confidence:  ConfidenceHigh,
		GoroutineID: trace.GoID(0),
		BlockedOn:   blockedOn,
		Location:    chosen.location,
		Function:    chosen.function,
		Stack:       stack,
	}
}

// isOpLine returns true if the lowercased line starts a new race operation block.
func isOpLine(lwr string) bool {
	return strings.HasPrefix(lwr, "read at ") ||
		strings.HasPrefix(lwr, "write at ") ||
		strings.HasPrefix(lwr, "previous read at ") ||
		strings.HasPrefix(lwr, "previous write at ")
}

// isRaceRuntimePath returns true if a file path belongs to the Go runtime or stdlib.
// Race detector paths use the full absolute path to the Go installation.
func isRaceRuntimePath(path string) bool {
	return isRuntimeFrame("", path) ||
		strings.Contains(path, "/go/src/sync/") ||
		strings.Contains(path, "/go/src/runtime/") ||
		strings.Contains(path, "GOROOT")
}

// isRaceRuntimeFunc returns true if a function name is a Go runtime/sync function.
func isRaceRuntimeFunc(fn string) bool {
	return strings.HasPrefix(fn, "runtime.") ||
		strings.HasPrefix(fn, "sync.") ||
		strings.HasPrefix(fn, "testing.")
}
