# ThreadGraph — Architecture, Code Flow & Algorithms

## What It Does

ThreadGraph is a CLI tool that detects **goroutine leaks**, **deadlocks**, and **long-blocking operations** in Go programs. It works by capturing and analyzing the Go runtime's execution trace — a structured binary log of every goroutine state transition — and applying a set of detection algorithms over it.

**Current benchmark score: 62/68 = 91% on GoBench GoKer** (research-grade dataset of real-world Go concurrency bugs extracted from production projects like Kubernetes, etcd, CockroachDB, gRPC, and Docker).

---

## High-Level Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                        CLI Entry Point                          │
│                         main.go                                 │
│                    cmd/  (cobra commands)                       │
│          ┌─────────────┐         ┌──────────────┐              │
│          │ run command │         │analyze command│              │
│          │  cmd/run.go │         │cmd/analyze.go │              │
│          └──────┬──────┘         └──────┬────────┘             │
│                 │                        │                      │
│         ┌───────▼──────┐                │                      │
│         │    Tracer    │                │ (existing trace file) │
│         │  (go test    │                │                      │
│         │  -trace)     │                │                      │
│         └───────┬──────┘                │                      │
│                 │ trace.out             │                      │
│                 └────────────┬──────────┘                      │
│                              │                                  │
│                    ┌─────────▼─────────┐                       │
│                    │  Dynamic Detector │                        │
│                    │  (trace parser +  │                        │
│                    │  6 algorithms)    │                        │
│                    └─────────┬─────────┘                       │
│                              │                                  │
│              ┌───────────────┼──────────────────┐              │
│              │               │                  │              │
│    ┌─────────▼──────┐  ┌────▼─────┐  ┌────────▼───────┐      │
│    │ Static Analyzer│  │   LLM    │  │   Reporters    │      │
│    │ (go/ssa, opt.) │  │ (Claude) │  │ terminal / JSON│      │
│    └────────────────┘  └──────────┘  └────────────────┘      │
└─────────────────────────────────────────────────────────────────┘
```

---

## Package Map

```
threadgraph/
├── main.go                         Entry point — calls cmd.Execute()
│
├── cmd/
│   ├── root.go                     Global flags: --format, --no-llm, --output,
│   │                               --min-block, --debug-filtered, --static
│   ├── run.go                      `threadgraph run` — captures + analyzes
│   └── analyze.go                  `threadgraph analyze` — analyzes existing trace
│
├── internal/
│   ├── tracer/
│   │   └── tracer.go               Wraps `go test -trace`; handles multi-package
│   │
│   ├── detector/
│   │   ├── detector.go             Core: trace parser + orchestrator (Analyze())
│   │   ├── leaks.go                3 detectors: leaks, orphans, transient blocks
│   │   ├── deadlock.go             3 detectors: deadlocks, AB-BA, chan+lock cycle
│   │   └── filter.go               Stack classification utilities
│   │
│   ├── static/
│   │   └── lockrelease.go          go/ssa CFG analysis for lock leaks (--static)
│   │
│   ├── llm/
│   │   └── claude.go               Optional Claude API for plain-English explanations
│   │
│   └── reporter/
│       ├── terminal.go             Colored terminal output
│       └── json.go                 Structured JSON output
│
└── testdata/                       Real-bug reproductions for regression testing
    ├── buggy/                      Synthetic: 5 goroutine leaks
    ├── grpc-roundrobin/            Real bug: grpc-go goroutine leak
    ├── etcd-leasehttp/             Real bug: etcd lease goroutine leak
    ├── grpc-dialcontext/           Real bug: grpc dial context leak
    └── chan-receive-leak/          Real bug: channel receive leak (etcd kv_test pattern)
```

---

## Command Flow

### `threadgraph run ./pkg/...`

```
1. Parse flags (--duration, --min-block, --no-llm, --static, --debug-filtered)
2. tracer.Run(args, duration)
   └── go test -trace /tmp/threadgraph-XXXX.out -timeout <duration> <args>
3. detector.Analyze(traceFile, opts)
   └── (see Trace Analysis Pipeline below)
4. RETRY LOOP — Schedule Diversity
   if len(findings) == 0:
     retry with GOMAXPROCS=1   (serialized scheduling)
     retry with GOMAXPROCS=2   (light concurrency)
     retry with GOMAXPROCS=4   (moderate concurrency)
     take best (most findings) result
5. Optional: static.AnalyzeLockRelease(args)  [if --static]
6. Optional: llm.Explain(findings, apiKey)     [if ANTHROPIC_API_KEY set]
7. reporter.WriteTerminal() or reporter.WriteJSON()
```

### `threadgraph analyze trace.out`

```
1. Parse flags (--min-block, --no-llm, --debug-filtered)
2. detector.Analyze(trace.out, opts)
3. Optional: llm.Explain(findings, apiKey)
4. reporter.WriteTerminal() or reporter.WriteJSON()
```

---

## Trace Analysis Pipeline

The Go runtime emits a binary execution trace (`go test -trace out.bin`). The `golang.org/x/exp/trace` package decodes this into a stream of `Event` objects.

ThreadGraph cares exclusively about `EventStateTransition` events on goroutines, which record every goroutine state change:

```
GoNotExist → GoRunnable   (goroutine created)
Executing  → GoWaiting    (goroutine blocked)
GoWaiting  → Executing    (goroutine unblocked, direct resume)
GoWaiting  → GoRunnable   (goroutine unblocked, woken by close/signal)
GoRunnable → Executing    (goroutine scheduled)
Executing  → GoNotExist   (goroutine exited)
```

### State Machine Per Goroutine (`goroutineState`)

For each goroutine ID seen in the trace, we maintain:

| Field | Set When | Purpose |
|-------|----------|---------|
| `isBlocked` | `Executing → GoWaiting` | Is goroutine currently blocked? |
| `reason` | same | "chan send", "chan receive", "sync", "sleep", … |
| `stack` | same | Formatted stack trace at block site |
| `function`, `location` | same | Top user-code frame (file:line) |
| `blockStart` | same | Timestamp when blocking started |
| `creationStack` | `GoNotExist → GoRunnable` | Stack at `go func()` call site |
| `creationSeen` | same | Whether we saw this goroutine's birth |
| `creationFunction`, `creationLocation` | same | Top user-code frame at creation |
| `goroutineDead` | `→ GoNotExist` | Goroutine exited normally |
| `prevLongBlockDuration/Stack/…` | `GoWaiting → *` | Peak sync-block info for transient detection |
| `prevSyncLocation` | unblock from "sync" | Most recent lock acquisition site (for chan+lock cycle) |
| `prevSyncEndTime` | same | Timestamp of acquisition (for staleness) |
| `syncHistory[5]` | unblock from "sync" | Circular buffer of last 5 lock acquisitions (for AB-BA) |
| `syncHistoryIdx` | same | Write pointer into circular buffer |

### Trace Parse Loop

```go
for each event:
    if GoNotExist → GoRunnable:
        record creationStack, creationFunction, creationLocation
    if → GoNotExist:
        set goroutineDead = true
    if Executing → GoWaiting:
        set isBlocked, reason, stack, blockStart
    if GoWaiting → (Executing | GoRunnable):
        if reason == "sync":
            update prevLongBlock if longest so far
            push location to syncHistory circular buffer
            update prevSyncLocation, prevSyncEndTime
        clear isBlocked, reason, stack, location
```

After all events are consumed, six detection algorithms run over the final goroutine map.

---

## Detection Algorithms (6 Total)

### 1. `detectLeaks` — Goroutine Leak Detection

**What it catches**: Goroutines permanently blocked on channel operations at trace end.

**Algorithm**:
```
for each goroutine G at trace end:
    if G.isBlocked == false → skip
    if G.stack is ALL runtime frames → skip           [filter 1]
    if G.reason == "sleep" → skip                     [filter 2]
    if G.reason == "chan send" or "chan receive":
        if !G.creationSeen → skip                     [filter 3]
        if G.creationStack is non-testing runtime → skip [filter 4]
        → emit KindGoroutineLeak (high confidence)
    else (sync, select, etc.):
        if G.blockDuration < minBlock → skip
        → emit KindLongBlock (medium confidence)
```

**Key filter — `isNonTestRuntimeGoroutine`**: Distinguishes goroutines created by non-testing runtime code (e.g., `net/http` server workers — legitimate background goroutines) from goroutines created by the testing framework's `testing.T.Run` (which run user test function bodies and CAN leak). This was the fix that unlocked +4 bugs on GoBench.

**Confidence**: `high` for channel blocks (definitive: the goroutine will never unblock), `medium` for sync/select (heuristic based on duration).

---

### 2. `detectDeadlocks` — Mutex Contention Groups

**What it catches**: Multiple goroutines all blocked on the same mutex lock call site, indicating a partial deadlock.

**Algorithm**:
```
Group goroutines by (reason="sync", location=file:line)
For each group where blockDuration > deadlockThreshold (500ms):
    emit KindDeadlock with the longest-blocked goroutine as representative
```

**Why call-site grouping?** The Go execution trace doesn't expose mutex addresses — it only says "goroutine blocked on sync". Two goroutines waiting at the same `file:line` for the lock are almost certainly competing for the same mutex.

**Threshold**: 500ms default (configurable via `--min-block`). Shorter than the main `minBlock` to catch deadlocks even with short traces.

---

### 3. `detectTransientBlocks` — Timed-Out Deadlocks

**What it catches**: Goroutines that were blocked on a mutex for a long time but were forcibly resumed by the test timeout (so their final state is "running", not "blocked").

**Why needed**: If a test deadlocks for 9 seconds and the test framework's 10-second timeout kills it, the goroutine's final state is "running" (the timeout panicked it back to life). `detectLeaks` would miss this because `isBlocked` is false.

**Algorithm**:
```
for each goroutine G:
    if G.prevLongBlockDuration >= minBlock:
        if G.prevLongBlockStack is not runtime-only:
            emit KindLongBlock (medium confidence)
            (deduplicated by location — avoid duplicate reports)
```

The `prevLongBlock*` fields are updated inside the unblock handler whenever a sync block is longer than any previous one.

---

### 4. `detectABBA` — Lock Order Inversion (AB-BA Deadlock)

**What it catches**: Classic AB-BA deadlock — Goroutine 1 holds lock A and waits for lock B, while Goroutine 2 holds lock B and waits for lock A.

**Algorithm**:
```
Build a set of directed "lock edges":
  For each goroutine G currently blocked on "sync" at location L_wait:
    For each entry E in G.syncHistory (last 5 sync unblocks):
      if E.endTime is recent enough (< abbaStaleWindow = 5s):
        add edge: E.location → L_wait
        (meaning: G holds the lock at E.location, waits for L_wait)

For every pair of edges (e1, e2):
  if e1.from == e2.to AND e1.to == e2.from:
    → AB-BA inversion detected
    → emit KindDeadlock (medium confidence)
```

**Why `syncHistory[5]` instead of just `prevSyncLocation`?** Using only the most recent lock acquisition misses cases where the first lock was acquired multiple operations ago (multi-step lock sequences). The circular buffer of size 5 extends the detection window.

**False positive guard**: The `abbaStaleWindow` (5 seconds) prevents matching lock acquisitions that happened too long ago — the lock was likely released by then.

**Confidence is Medium** because two goroutines at the same call site might use different mutex *instances* (e.g., two independent objects of the same type). The heuristic is still low false-positive in practice because AB-BA requires BOTH goroutines to be simultaneously blocked.

---

### 5. `detectChanLockCycle` — Channel-Lock Deadlock Cycle

**What it catches**: G1 holds a mutex and blocks on a channel send/receive; G2 is blocked trying to acquire the same mutex that G1 holds. Cycle: G1 → waiting for channel (that G2 would service) → G2 → waiting for G1's lock.

**Algorithm**:
```
Build lockWaiters: set of call sites where goroutines are blocked on "sync"

For each goroutine G blocked on "chan send" or "chan receive":
  if G.prevSyncLocation is in lockWaiters:  (G holds a lock someone is waiting for)
    if G.prevSyncEndTime is recent (< 5s):
      → emit KindDeadlock (medium confidence)
```

**Uses `prevSyncLocation`** (not full history) because we only need to know the lock G1 currently holds, not its full acquisition history. Deduplication is by `prevSyncLocation` — one report per lock site.

---

### 6. `detectOrphans` — Never-Started Goroutines

**What it catches**: Goroutines spawned just before a test exits, which were never scheduled and never ran.

**Algorithm**:
```
Only active if traceDuration < 200ms  (test-exits-immediately pattern)

For each goroutine G:
  if G.goroutineDead → skip (normal lifecycle)
  if G.isBlocked → skip (caught by other detectors)
  if !G.creationSeen → skip (pre-test goroutine)
  if G.creationStack is non-testing runtime → skip
  if G has no function/location → skip
  → emit KindGoroutineLeak (low confidence)
```

**Why only on short traces?** On long traces, alive non-blocked goroutines are normal background workers. Only very short traces (< 200ms) indicate "test exited before goroutines could run."

---

## Static Analysis (`--static` flag)

`internal/static/lockrelease.go` implements **go/ssa CFG-based lock release analysis**. This is a compile-time analysis that doesn't require the bug to manifest at runtime.

### Algorithm

```
1. Load package source via go/packages
2. Build SSA (Static Single Assignment) form via golang.org/x/tools/go/ssa
3. For each function F in the SSA program:
   a. Find all Lock() / RLock() call sites (lockSites)
   b. Find all Unlock() / RUnlock() call sites per basic block (unlockIdx)
   c. If any defer Unlock() exists → skip (defer covers all paths)
   d. For each lockSite L in block B:
      - If Unlock() follows Lock() in the same block B → safe, skip
      - BFS over successor blocks:
          - If a block contains Unlock() → stop propagating (lock released)
          - If BFS reaches a block with no successors (function exit)
            without finding Unlock() → emit KindLockLeak (low confidence)
```

**Why BFS over the CFG?** Lock release analysis is path-sensitive — the lock may be released on the "normal" path but not the "error" path. A simple count of Lock vs Unlock calls would miss this. BFS over the Control Flow Graph (which SSA provides as basic blocks with successor edges) correctly identifies whether ANY path from the lock site to a function exit lacks an Unlock().

**Limitation**: Interprocedural analysis (Unlock in a called function) is not tracked. Only direct `sync.Mutex.Lock` / `sync.Mutex.Unlock` on concrete types (not interface calls) are detected.

---

## Filter System (`internal/detector/filter.go`)

Filtering is critical — the raw trace contains hundreds of goroutines from the runtime, testing framework, and background workers that are NOT bugs.

### `isRuntimeFrame(funcName, line)`
Returns true for a single stack frame if it belongs to:
- `runtime.*` (Go scheduler, GC, etc.)
- `testing.*` (test framework)
- `_testmain.go` (generated test runner)
- Any path containing `runtime/` or `/runtime/trace`

### `isRuntimeGoroutine(stack)`
Returns true if **ALL** frames in a stack are runtime frames. Used to classify whether a goroutine's blocking stack or creation stack is entirely internal.

### `isNonTestRuntimeGoroutine(stack)` ← Key Phase 3 Addition
Returns true if the stack is runtime-only **AND** has no `testing.*` frames.

**Why this distinction matters**:
- `net/http` worker goroutine: creation stack = `net/http.*` (no testing frames) → `isNonTestRuntimeGoroutine = true` → **filtered** ✓
- `testing.T.Run` subtest goroutine: creation stack = `testing.*` → `isNonTestRuntimeGoroutine = false` → **kept** ✓

Without this distinction, goroutines running inside `t.Run()` subtests were incorrectly filtered because `testing.(*T).Run` is a testing-framework function, making their creation stacks look "runtime-only."

### `extractStack(trace.Stack)`
Formats a raw trace stack into a human-readable string and picks the **first non-runtime frame** as `function` and `location` (file:line). This is what gets shown in reports and used for call-site grouping in deadlock detection.

---

## Schedule Diversity Retry (`cmd/run.go`)

Many concurrency bugs are **scheduling-dependent** — they only deadlock when goroutines execute in a specific interleaving. To increase the chance of triggering them:

```
Run 1: default GOMAXPROCS (uses all CPU cores)
  if 0 findings:
    Run 2: GOMAXPROCS=1  (fully serialized — one goroutine at a time)
    Run 3: GOMAXPROCS=2  (light concurrency)
    Run 4: GOMAXPROCS=4  (moderate concurrency, if numCPU >= 4)
    use whichever run produced findings
```

`GOMAXPROCS=1` is the most impactful: it forces goroutines to run sequentially, which exposes bugs that only manifest when operations interleave in a specific order. Different `GOMAXPROCS` values explore different scheduling spaces.

---

## Tracer (`internal/tracer/tracer.go`)

Wraps the Go toolchain's built-in execution tracer:

```
go test -trace /tmp/threadgraph-XXXX.out -timeout <duration> <pkg>
```

Key behaviors:
- **No binary modification**: uses `go test -trace`, which is a standard Go feature. No binary instrumentation needed.
- **Multi-package**: if the pattern is `./...`, uses `go list` to expand into individual packages, runs each separately, and picks the largest trace file (most events = most interesting).
- **Temp file lifecycle**: trace files are created in `os.TempDir()` and cleaned up after analysis.

---

## LLM Integration (`internal/llm/claude.go`)

Optional post-processing step. If `ANTHROPIC_API_KEY` is set and `--no-llm` is not passed:

```
findings → buildPrompt() → Claude API (claude-sonnet-4-6) → plain-English explanation
```

The prompt includes: finding kind, goroutine ID, blocked-on reason, block duration, file:line location, and stack trace. Claude is asked to:
1. Explain the root cause in 1-2 sentences
2. Give a specific code fix
3. Show a before/after diff if possible

The explanation is appended to terminal output and embedded as `llm_explanation` in JSON output.

**LLM does NOT affect detection** — it only explains findings after they are found. Detection is entirely deterministic.

---

## Output Formats

### Terminal (default)
Color-coded with `fatih/color`:
- Red: goroutine leaks, deadlocks
- Yellow: long blocks
- Green: zero count (no issue)
- Dimmed: stack traces, metadata footer

### JSON (`--format json`)
Structured output suitable for CI pipelines:
```json
{
  "trace_file": "/tmp/threadgraph-1234.out",
  "duration_ms": 10001,
  "goroutines_analyzed": 45,
  "findings": [...],
  "llm_explanation": "..."
}
```

---

## Data Flow Summary

```
go test -trace out.bin
         │
         ▼
golang.org/x/exp/trace.Reader
  → stream of EventStateTransition events
         │
         ▼
Single-pass trace parse (O(N) in events)
  → map[GoID]*goroutineState  (live state machine per goroutine)
         │
         ▼
6 detection algorithms (each O(G) or O(G²) in goroutine count)
  detectLeaks()            O(G)
  detectDeadlocks()        O(G)
  detectTransientBlocks()  O(G)
  detectABBA()             O(G²) in goroutines with sync history
  detectChanLockCycle()    O(G)
  detectOrphans()          O(G)
         │
         ▼
[]Finding  (deduplicated)
         │
    ┌────┴────┐
    │         │
  LLM      Reporter
(optional) (terminal/JSON)
```

---

## Benchmark Results

Tested against **GoBench GoKer** — 68 real blocking bugs from production Go projects (Kubernetes, etcd, CockroachDB, gRPC, Docker/Moby, Hugo, Istio, Syncthing, Knative Serving).

| Phase | Score | Key Improvement |
|-------|-------|-----------------|
| Phase 1 | 31/68 = 45% | Basic leak detection |
| Phase 2 | 59/68 = 86% | AB-BA, chan+lock, orphans, GOMAXPROCS=1 retry |
| Phase 3 | **62/68 = 91%** | `isNonTestRuntimeGoroutine`, GOMAXPROCS diversity, syncHistory |

Remaining 6 missed bugs:
- **Scheduling lottery** (2): require extremely specific goroutine interleaving
- **Lock leaks** (2): require static analysis (`--static` flag) — bug never manifests at runtime
- **Flaky** (1): deadlock resolves in <1ms, race against test completion
- **3-goroutine cycle** (1): requires rare 3-way interleaving across mutex+channel+mutex

**False positive rate on `net/http/httptest`**: 0 findings across all retry passes.

---

## Key Design Constraints

| Constraint | Implication |
|-----------|-------------|
| Trace only records "goroutine blocked on sync" — NOT which mutex address | Cannot match lock holders to waiters precisely; use call-site heuristics |
| Trace has NO lock release events | Cannot track when a lock is freed; use "last sync unblock" as proxy for "currently held" |
| Channel identity is opaque in the trace | Cannot match senders to receivers; detect by goroutine state at trace end |
| Only executed paths reveal bugs | Non-deterministic bugs need multiple runs with different scheduling (GOMAXPROCS retry) |
| Single-pass streaming parse | Memory efficient; O(N) in trace events, O(G) state retained |
