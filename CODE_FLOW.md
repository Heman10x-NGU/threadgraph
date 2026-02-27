# ThreadGraph — Code Flow & Architecture

> Read this to understand exactly what the tool does today, what the LLM is (and isn't) doing,
> and how to stress test it. Edit freely — this is your design document.

---

## What ThreadGraph Does (One-Line Summary)

**It runs your Go tests, records every goroutine state change, then finds goroutines that are
still blocked when the program ends — those are leaks, deadlocks, or stalls.**

The LLM is NOT reading your source code. It is NOT building a graph. It is a plain-English
explainer that receives the findings AFTER detection is already done.

---

## The Two Entry Points

### 1. `threadgraph run ./...`
Fully automated. You point it at a Go package, it does everything.

```
User runs:  threadgraph run ./...
               │
               ▼
         tracer/tracer.go
         Runs: go test -trace /tmp/threadgraph-123.out -timeout 10s ./...
               │
               │  (Go's built-in trace facility records ALL goroutine events)
               ▼
         trace file written to /tmp/threadgraph-123.out
               │
               ▼
         detector/detector.go  (same path as analyze, see below)
```

### 2. `threadgraph analyze trace.out`
You already have a trace file (e.g. from `go test -trace trace.out` yourself).

```
User runs:  threadgraph analyze trace.out
               │
               ▼
         cmd/analyze.go  →  detector.Analyze("trace.out", minBlock)
```

---

## The Detection Pipeline (The Real Brain)

This is the core logic. No LLM involved here.

```
trace.out (binary file)
    │
    ▼
golang.org/x/exp/trace.NewReader()
    │  Parses Go's binary trace format — every goroutine event
    │
    ▼
Event loop  [detector/detector.go:77-126]
    │
    │  For every EventStateTransition on a goroutine:
    │
    │  IF goroutine goes Executing → GoWaiting:
    │     record: reason (e.g. "chan send"), stack, timestamp
    │
    │  IF goroutine goes GoWaiting → Executing:
    │     clear it (it unblocked — not a leak)
    │
    ▼
End of trace: snapshot of every goroutine's state
    │
    ▼
filter.go: isRuntimeGoroutine()
    │  Removes: runtime.*, testing.*, GC goroutines, scheduler
    │  Keeps:   user code only
    │
    ▼
leaks.go: detectLeaks()
    │  Still blocked on "chan send" or "chan receive"?  → goroutine_leak  (high confidence)
    │  Still blocked on anything else for > minBlock?  → long_block      (medium confidence)
    │
    ▼
deadlock.go: detectDeadlocks()
    │  2+ goroutines blocked on sync.Mutex.Lock at the SAME call site for > 500ms?
    │  → deadlock  (medium confidence)
    │
    ▼
[]Finding{
    Kind, Confidence, GoroutineID, BlockedOn, BlockedFor, Stack, Function, Location
}
```

**Key insight:** The detector never reads your source code. It reads a runtime trace —
a recording of what actually happened at nanosecond precision. This is why it has 0 false
positives on channel leaks: it literally observed the goroutine block and never unblock.

---

## The LLM Step (Optional — Needs ANTHROPIC_API_KEY)

**What it actually does:**

```
findings  ([]Finding from detector)
    │
    ▼
llm/claude.go: buildPrompt()
    │
    │  Builds a text prompt like:
    │  ┌────────────────────────────────────────────────────────┐
    │  │ I analyzed a Go trace and found 5 concurrency issues.  │
    │  │                                                         │
    │  │ Issue 1: goroutine_leak (confidence: high)              │
    │  │   Goroutine 42 is blocked on: "chan send"               │
    │  │   Blocked for: 301ms                                    │
    │  │   Location: main.go:15                                  │
    │  │   Function: main.leakyHandler.func1                     │
    │  │   Stack:                                                 │
    │  │     runtime.chansend1 (chan.go:161)                     │
    │  │     main.leakyHandler.func1 (main.go:15)               │
    │  │                                                         │
    │  │ For each issue:                                         │
    │  │   1. Explain root cause in plain English                │
    │  │   2. Give a specific code fix                           │
    │  │   3. Show before/after diff if possible                 │
    │  └────────────────────────────────────────────────────────┘
    │
    ▼
POST https://api.anthropic.com/v1/messages
    model: claude-sonnet-4-6
    │
    ▼
Plain-English explanation returned
    │
    ▼
Appended to terminal output or embedded in JSON under "llm_explanation"
```

**What the LLM does NOT do:**
- Read your `.go` source files
- Build any graph
- Run any analysis of its own
- Make detection decisions

**What the LLM DOES do:**
- Takes the already-detected findings (goroutine ID, blocked reason, stack, duration)
- Explains WHY this is a problem in plain English
- Suggests a fix
- Shows a before/after diff

The LLM is a **translator**: detection engine → human-readable explanation.

---

## Output Layer

```
Result{Findings, DurationMs, GoroutinesAnalyzed, TraceFile}
    │
    ├── --format terminal (default)
    │       reporter/terminal.go
    │       Colored output: red for leaks/deadlocks, yellow for long blocks
    │
    └── --format json
            reporter/json.go
            Structured JSON for CI pipelines / downstream tools
```

---

## Full File Map

| File | What it does |
|------|-------------|
| `main.go` | Entry point, calls `cmd.Execute()` |
| `cmd/root.go` | Global flags: `--format`, `--no-llm`, `--output`, `--min-block` |
| `cmd/analyze.go` | `threadgraph analyze <trace.out>` command |
| `cmd/run.go` | `threadgraph run [args...]` command |
| `internal/detector/detector.go` | Reads trace file, event loop, orchestrates detection |
| `internal/detector/leaks.go` | Finds goroutines still blocked at end of trace |
| `internal/detector/deadlock.go` | Groups mutex-blocked goroutines by call site |
| `internal/detector/filter.go` | Removes runtime/testing goroutines from results |
| `internal/tracer/tracer.go` | Shells out to `go test -trace` |
| `internal/llm/claude.go` | Builds prompt from findings, calls Claude API |
| `internal/reporter/terminal.go` | Colored terminal output |
| `internal/reporter/json.go` | JSON output |
| `testdata/buggy/main.go` | Intentionally buggy program (5 goroutine leaks) |
| `testdata/buggy/leaky_test.go` | Test wrapper so `go test` can run it |

---

## What You Could Change

### Option A: Add source code to the LLM prompt
Right now the prompt only has the stack trace + metadata.
You could add the actual source file contents:

```
llm/claude.go: buildPrompt()
    Currently sends:  stack trace, location, blocked reason
    Could also send:  contents of the file at f.Location (read it from disk)
```

This would let Claude say "on line 15, your `ch := make(chan int)` has no receiver" instead
of just "goroutine blocked on chan send at main.go:15".

### Option B: Build a goroutine graph before the LLM
You could build a graph of goroutine relationships from the trace:
- Which goroutine spawned which (parent/child edges)
- Which goroutines are waiting on the same channel
- Which goroutines are competing for the same mutex

Then send the graph to the LLM. This is the "graph-based analysis" idea.
The trace file contains all this data — it's just not being extracted yet.

### Option C: Skip the LLM entirely
The LLM is 100% optional today. `--no-llm` works perfectly.
For CI use, JSON output without LLM is the right choice:
```bash
threadgraph run ./... --no-llm --format json | jq '.findings | length'
```

---

## How to Stress Test It

### Level 1: Known-good baseline
```bash
# Should always find exactly 5 leaks, 0 false positives
./threadgraph run ./testdata/buggy/ --no-llm
```

### Level 2: Clean package (should find 0)
```bash
# Any well-written package — should produce 0 findings
./threadgraph run ./internal/detector/ --no-llm
./threadgraph run ./internal/reporter/ --no-llm
```

### Level 3: Write targeted stress test programs
Create `testdata/` programs that test specific scenarios:

```
testdata/
├── buggy/          ← exists: 5 goroutine leaks (chan send)
├── clean/          ← TODO: should produce 0 findings
├── many_leaks/     ← TODO: 50+ leaks, test scale
├── leak_receive/   ← TODO: leak on chan receive (not just send)
├── long_block/     ← TODO: goroutine blocked on mutex for 2s
└── deadlock/       ← TODO: 2 goroutines deadlocked on same mutex
```

### Level 4: Real-world packages
```bash
# Test against popular Go packages that might have real issues
./threadgraph run github.com/some/package/... --duration 30s --no-llm
```

### Level 5: False positive hunting
The hardest test. Find a case where threadgraph reports a finding on correct code.
The most common false positive risk: goroutines that are *intentionally* long-lived
(e.g. a background worker that listens on a channel forever).
Current gap: we flag ALL channel-blocked goroutines — a server's `select { case msg := <-ch }`
loop would be flagged. This needs a way to distinguish "program exited while goroutine was
mid-work" from "goroutine will never unblock."

---

## Current Gaps / Known Limitations

| Gap | Impact | Fix idea |
|-----|--------|----------|
| Long-lived worker goroutines flagged as leaks | False positives on servers | Track goroutine creation stack; if created before test started, it's a background worker |
| Deadlock detection requires 2+ goroutines at same call site | Misses single-goroutine self-deadlock | Full deadlock = runtime panic anyway, so low priority |
| LLM doesn't see source code | Explanations are generic | Read source file at the reported location and include it in prompt |
| No goroutine relationship graph | Can't explain "goroutine A is waiting for goroutine B" | Build parent/child and channel-pair graph from trace |
| `--race` flag not wired up | Can't detect data races | Pass `-race` to `go test`, parse race detector stderr |
