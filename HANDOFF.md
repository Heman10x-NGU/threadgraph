# ThreadGraph Handoff

## Score: 66/68 = 97% GoBench. Missed: cockroach/1462, serving/2137

## Binary: `go build -o /tmp/threadgraph .` from `/Users/heman10x/Downloads/threadGraphs/threadgraph/`

## GoBench: `/tmp/gobench2/` (or clone GoBench if missing)

## Phase 5 COMPLETED

### Session 1 (Claude):

1. Added `KindDataRace` to `internal/detector/detector.go`
2. Created `internal/detector/race.go` — ParseRaceOutput() parser for `go test -race` output
3. Added `RunRace()` to `internal/tracer/tracer.go`
4. Added `--race` flag to `cmd/root.go` (var `flagRace bool`)

### Session 2 (Antigravity):

5. Wired `--race` into `cmd/run.go` — after static block, calls `tracer.RunRace()` + `detector.ParseRaceOutput()`
6. Updated `internal/reporter/terminal.go`:
   - Added data race count to summary (red, like leaks)
   - Added `● DATA RACE` case to `printFinding`
   - Fixed `Goroutine 0` display for static/race findings (shows "Issue:" instead)
7. Implemented `detectWaitGroupDeadlock` in `internal/detector/deadlock.go`:
   - Finds goroutines blocked on `sync.(*WaitGroup).Wait`
   - BFS descendants via parentID tree
   - If ALL live descendants are blocked → `KindDeadlock`, `ConfidenceMedium`
   - Wired into `Analyze()` in `detector.go`
8. Created test cases:
   - `testdata/race-condition/race_test.go` — deliberate data race
   - `testdata/waitgroup-deadlock/wg_test.go` — WaitGroup deadlock pattern

### Verification Results:

- `testdata/buggy/` → 5 goroutine leaks ✅ (regression OK)
- `testdata/waitgroup-deadlock/` → 3 leaks + 1 deadlock (WaitGroup) + 1 long block ✅
- `testdata/race-condition/ --race` → 2 data races ✅
- JSON output validates ✅

## Key file locations:

- `internal/detector/detector.go` — Finding types, Analyze() orchestrator
- `internal/detector/deadlock.go` — detectDeadlocks, detectLockCycles, detectChanLockCycle, detectWaitGroupDeadlock (NEW)
- `internal/detector/leaks.go` — detectLeaks, detectOrphans, detectTransientBlocks
- `internal/detector/filter.go` — isRuntimeGoroutine, isNonTestRuntimeGoroutine, extractStack
- `internal/detector/race.go` — ParseRaceOutput
- `internal/tracer/tracer.go` — Run, RunRace
- `internal/static/lockorder.go` — AnalyzeLockOrder, AnalyzeChanLockHolding (Tarjan SCC)
- `internal/static/lockrelease.go` — AnalyzeLockRelease (go/ssa CFG analysis)
- `cmd/run.go` — main run command, wires all analyses
- `cmd/root.go` — global flags incl. flagRace
- `internal/reporter/terminal.go` — colored output (now handles data races)
- `internal/llm/claude.go` — Claude API explanations with readCodeContext

## Architecture: trace-based runtime + go/ssa static analysis + race detector + WaitGroup deadlock + optional LLM

## Module: `github.com/Heman10x-NGU/threadgraph`

## NEXT PRIORITIES (from STRATEGY.md):

1. **Goroutine provenance tree grouping** — group findings by common ancestor for cleaner output
2. **Baseline comparison in CI** — `--baseline` already implemented, needs CI workflow template
3. **HTML report** — goroutine timeline visualization
4. **VS Code extension** — inline annotations (the real moat)
5. **Hybrid static + dynamic confirmation** — cross-reference `go/ssa` predictions with runtime trace
