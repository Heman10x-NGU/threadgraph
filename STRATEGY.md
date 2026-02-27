# ThreadGraph — Honest Limitations, Fixes, and Why It Can Be Defensible

---

## Part 1: Current Limitations (Honest Assessment)

### L1. False positives on intentional long-lived goroutines
**The problem:** A goroutine doing `for { select { case msg := <-ch: } }` in an HTTP server
is permanently blocked on chan receive — identical to a leak at the trace level.
We currently flag ALL channel-blocked goroutines.

**Impact:** Run ThreadGraph on any real server codebase → false positives everywhere.
This kills trust immediately. A tool with false positives gets turned off.

**Fix:** Track goroutine provenance. If a goroutine was spawned before the "unit of work"
started (e.g. before a test function ran), it is a background worker, not a leak.
The trace includes parent goroutine IDs — build a goroutine tree.

---

### L2. Trace window problem — we only see what happens during the test
**The problem:** `go test -trace` records what happens while the test runs.
If a leak accumulates over 1000 requests, a 10-second trace might not show it.

**Impact:** Slow leaks (e.g. 1 leaked goroutine per HTTP request) are invisible unless
the test hammers the system hard enough.

**Fix:** Two things:
1. `--duration` flag (already exists) — run tests longer to accumulate leaks
2. Baseline comparison — run before/after a commit, diff the goroutine count.
   "Your commit added 3 permanently-blocked goroutines" is unambiguous.

---

### L3. Deadlock detection is weak (heuristic, not exact)
**The problem:** We detect deadlocks by grouping goroutines blocked on mutex at the same
call site. But we cannot get the mutex memory address from the trace without instrumentation.
Two goroutines waiting at `mu.Lock()` on line 42 might not be waiting on the SAME mutex.

**Impact:** Medium confidence at best. Could miss single-goroutine self-deadlocks entirely.
(Though full deadlock = runtime panic, so that's usually obvious.)

**Fix:** Instrument the binary. Add a small runtime hook that records mutex addresses.
This is what race detectors do — they patch the binary at build time.
Alternatively: use `go/ssa` static analysis to find lock-unlock patterns and verify
at runtime that they match what the trace shows.

---

### L4. LLM doesn't see your source code
**The problem:** Claude gets a stack trace and says "goroutine blocked on chan send."
It doesn't see the actual `ch := make(chan int)` line in your code.
The explanation is generic, not specific to your codebase.

**Impact:** The LLM output is useful but imprecise. It can't show you the exact
5-line fix in your actual code.

**Fix:** Read the source file at `finding.Location` from disk.
Pass the relevant 20-line window of code to the prompt.
Claude can then say: "On line 15, `ch := make(chan int)` creates an unbuffered channel.
Change it to `make(chan int, 1)` or add a receiver before returning."
This is a 10-line code change. See CODE_FLOW.md Option A.

---

### L5. No goroutine relationship graph
**The problem:** We report 5 individual goroutines blocked.
We don't say "all 5 were spawned by the same HTTP handler, called from the same request path."

**Impact:** For 5 goroutines it's fine. For 500 goroutines (real production leak) you
get a wall of identical findings — impossible to triage.

**Fix:** Build a goroutine spawn tree from the trace. Parent goroutine IDs are in the
trace events. Group findings by their common ancestor.
Output: "1 unique leak pattern → 500 affected goroutines, root: ServeHTTP → leakyHandler"

---

### L6. No data race detection
**The problem:** The `-race` flag is not wired up.
Data races are the other major Go concurrency bug class.

**Fix:** Already planned. Pass `-race` to `go test`, parse the race detector's stderr.
The race detector output is structured text — easy to parse.
```
WARNING: DATA RACE
Read at 0x00c0001b4010 by goroutine 7:
  main.racyIncrement(...)
```
This is a 1-day implementation.

---

### L7. No CI baseline / regression tracking
**The problem:** We detect bugs in a single snapshot. We don't know if a bug is new.
"5 goroutine leaks" means nothing if those same 5 existed before your PR.

**Impact:** Developers will ignore alerts they can't attribute to a specific change.

**Fix:** Store a baseline (JSON file committed to repo).
Compare current run against baseline. Report only REGRESSIONS (new leaks since last clean run).
```
threadgraph run ./... --baseline .threadgraph-baseline.json
→ 3 NEW goroutine leaks since last baseline (commit a3f2c1d)
→ 0 resolved
```

---

## Part 2: What Would Actually Make This Good

Here is the upgrade path from "neat prototype" to "tool people actually use":

### Tier 1 — Required to ship (fix false positives first)
1. **Goroutine provenance tree** (fix L1, L5)
   - Build parent/child goroutine map from trace events
   - A goroutine is a background worker if its parent was spawned before test started
   - Group findings by common ancestor for clean output

2. **Source code context in LLM prompt** (fix L4)
   - Read the 20-line window around `finding.Location` from disk
   - Include it in the prompt
   - Output becomes: "Line 15: change `make(chan int)` to `make(chan int, 1)`"

3. **Baseline comparison** (fix L7)
   - `threadgraph run ./... --save-baseline` → writes .threadgraph-baseline.json
   - `threadgraph run ./... --baseline .threadgraph-baseline.json` → only report new issues
   - CI workflow: commit the baseline, fail builds on regression

### Tier 2 — Makes it significantly better
4. **Data race detection** (fix L6)
   - Pass `-race` flag through, parse output, add RaceCondition to Finding types

5. **Goroutine leak rate** (fix L2)
   - Run test N times, measure goroutine count delta per run
   - "Leaks 1 goroutine per test invocation" is a different severity than "leaks 5 at startup"

6. **HTML report** (new)
   - Goroutine timeline visualization
   - Click a goroutine → see its full lifecycle
   - Much easier to understand than a terminal dump

### Tier 3 — Where the real moat is (see Part 3)
7. **Hybrid static + dynamic analysis**
8. **Continuous monitoring mode**
9. **VS Code extension with inline annotations**

---

## Part 3: The Honest Answer on Defensibility

You asked the right question. Here is the unfiltered answer.

### What anyone can copy in a weekend

```
1. go test -trace trace.out
2. Parse trace with golang.org/x/exp/trace
3. Find goroutines still in GoWaiting at end
4. Send to Claude
5. Print output
```

Yes. Anyone can do this. You just proved it. This is not the product.

### What is actually hard to copy

**Hard thing #1: The false positive rate at production scale**

Getting 0 false positives on "hello world" is easy.
Getting 0 false positives on a 500k-line Go microservice with:
- 80 background goroutines
- gRPC connection pools
- database connection pools
- cache refresh workers
- health check goroutines

...is genuinely hard. It requires running against HUNDREDS of real Go projects,
seeing what gets falsely flagged, and tuning the filter logic for each case.

That corpus of real-world labeled data — "this goroutine is a worker, this one is a leak" —
is the moat. No one else has it because building it takes months of real usage.
They can copy the code in a day. They cannot copy 6 months of production tuning.

---

**Hard thing #2: VS Code inline annotations**

Snyk, SonarLint, and Codecov all built VS Code extensions.
They became dominant not because their algorithms are unique,
but because their UX is the best in class.

When a developer sees a red squiggle on the EXACT LINE where a goroutine leaks —
before the code even runs — that is a different product than a terminal printout.

That UX depth takes months to build right:
- Language Server Protocol integration
- Debounced analysis on file save
- CodeLens for "X goroutines blocked here across last 10 test runs"
- Quick-fix actions that apply the suggested fix in one click

This is months of engineering. Not a weekend clone.

---

**Hard thing #3: The CI data flywheel**

Once teams run ThreadGraph in CI, you (optionally, with consent) see:
- Which Go patterns cause leaks in real codebases
- Which fixes actually work
- Which libraries are commonly involved

This data feeds back into better detection.
Example: "We've seen this exact pattern in 200 repos — it's always a leak."
You can ship a named rule: `LEAK-HTTP-HANDLER-UNBUFFERED-CHANNEL`
with confidence: CONFIRMED, not just heuristic.

No one can copy this data because it doesn't exist yet. You build it by shipping first.

---

**Hard thing #4: Hybrid static + dynamic analysis**

This is where a real technical moat lives.
The current approach is PURE DYNAMIC (runtime trace only).
A pure static approach (AST analysis only) would be like existing linters.

The gap nobody has filled:

```
Static analysis:
  "This goroutine spawn pattern at handler.go:42 could leak
   IF the channel is unbuffered AND the caller returns early"
                    ↓
Runtime trace:
  "Goroutine from handler.go:42 DID block on chan send for 301ms
   in 5 out of 10 test runs"
                    ↓
Combined output:
  CONFIRMED LEAK (static + runtime agree)
  handler.go:42 — pattern: unbuffered channel with early return
  Affected: 5/10 test runs
  Fix: make(chan int, 1) on line 42
```

No tool does this for Go today. Building it requires:
- `go/ast` + `go/ssa` for static analysis
- Trace parsing for runtime confirmation
- A matching layer that connects static call sites to runtime goroutine IDs

This is 2-3 months of serious engineering, not a weekend project.

---

**Hard thing #5: Distribution and trust**

golangci-lint is trusted because 50,000 Go projects use it.
Not because it has better algorithms than a fresh rewrite.

Once you are in someone's `Makefile` and `.github/workflows/ci.yml`,
you are sticky. The switching cost is real.

The first mover who gets into CI pipelines wins the distribution.
Speed of shipping matters more than algorithm quality at this stage.

---

## Part 4: The Actual Roadmap to Being Defensible

```
NOW (done):
├── Pure dynamic detection (trace parsing) ✓
├── Goroutine leak detection: 5/5, 0 false positives ✓
├── CLI with analyze + run commands ✓
└── JSON output for CI ✓

NEXT (makes it shippable to real projects):
├── Goroutine provenance tree → kill false positives on servers
├── Source code context in LLM → specific fixes, not generic advice
├── Baseline comparison → CI regression detection
└── Data race detection (-race passthrough)

AFTER THAT (builds the moat):
├── VS Code extension → inline annotations at the exact leaking line
├── GitHub PR integration → "this PR introduced 3 leaks" as a PR check
└── Named leak patterns → LEAK-001: unbuffered channel in HTTP handler

THE REAL MOAT:
└── Hybrid static + dynamic analysis
    ├── No tool does this for Go today
    ├── Confirms bugs with two independent signals
    └── Eliminates the remaining false positives entirely
```

---

## Summary

| What you have | What it gets you |
|---------------|-----------------|
| Trace-based detection | Works. But copyable. |
| Low false positive rate on simple cases | Good start. Not enough. |
| LLM explanations | Nice to have. Differentiator is weak. |

| What you need | Why it's defensible |
|---------------|---------------------|
| Near-zero false positives on real servers | Requires real-world data no one else has |
| VS Code inline annotations | Months of UX work, sticky once installed |
| CI baseline regression detection | Once in the pipeline, you're sticky |
| Hybrid static + dynamic analysis | No one has built this for Go. 2-3 months. |
| Distribution + trust | First mover advantage. Ship fast. |

**The real answer:** The deterministic detection layer is NOT the product.
The product is: the best UX for finding concurrency bugs in Go,
backed by the only tool that combines static and runtime analysis,
trusted by enough Go teams that switching away has a real cost.

You get there by shipping fast, getting real usage, and iterating.
A copycat building from scratch is always 6 months behind.
